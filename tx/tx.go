// Package tx defines the binary wire format for SealStore transactions.
//
// SealStore's commit/reveal transactions are transmitted as typed structs
// marshaled to a pure binary form with the standard library's encoding/binary
// package. This package is the single source of truth for that format, shared
// by the ABCI app (state.go) and the CLI (cli/main.go).
//
// Layout (all multi-byte lengths are uvarints; the hash is a raw byte array,
// so there is no endianness in play):
//
//	CommitTx:  Tag(1) | uvarint(len(key)) | key | 32 raw hash bytes
//	RevealTx:  Tag(1) | uvarint(len(key)) | key | uvarint(len(value)) | value
package tx

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// TxType is the 1-byte leading tag of a serialized transaction.
type TxType uint8

const (
	TxUnknown TxType = iota
	TxCommit
	TxReveal
	TxTransfer // reserved: the standalone transfer tx was removed; funds move only via pay_commit/pay_reveal
	TxPayCommit
	TxPayReveal
)

// hashLen is the length of a sha256 digest, the fixed-size payload of a commit.
const hashLen = 32

// Message is any typed transaction that can be marshaled to its binary form.
type Message interface {
	Type() TxType
	Marshal() []byte
}

// CommitTx publishes a commitment: Hash is the raw sha256 digest of the value.
type CommitTx struct {
	Key  []byte
	Hash [32]byte
}

func (m *CommitTx) Type() TxType { return TxCommit }

func (m *CommitTx) Marshal() []byte {
	buf := make([]byte, 1+binary.MaxVarintLen64+len(m.Key)+hashLen)
	n := 1
	buf[0] = byte(TxCommit)
	n += binary.PutUvarint(buf[n:], uint64(len(m.Key)))
	n += copy(buf[n:], m.Key)
	n += copy(buf[n:], m.Hash[:])
	return buf[:n]
}

// RevealTx opens a commitment: Value must hash to the stored CommitTx.Hash.
type RevealTx struct {
	Key   []byte
	Value []byte
}

func (m *RevealTx) Type() TxType { return TxReveal }

func (m *RevealTx) Marshal() []byte {
	buf := make([]byte, 1+2*binary.MaxVarintLen64+len(m.Key)+len(m.Value))
	n := 1
	buf[0] = byte(TxReveal)
	n += binary.PutUvarint(buf[n:], uint64(len(m.Key)))
	n += copy(buf[n:], m.Key)
	n += binary.PutUvarint(buf[n:], uint64(len(m.Value)))
	n += copy(buf[n:], m.Value)
	return buf[:n]
}

// ---- signatureless payment scheme (accounts) ----

// Transfer is one credit leg of a payment body: pay `Amount` to the payee
// account at address `To` (64 lowercase hex chars).
type Transfer struct {
	To     []byte
	Amount uint64
}

// Payment is the revealed payment body: who paid (their address), the sequence
// number, the credit legs, and the fee. Its canonical encoding (see
// EncodePayment) is what the commitment C binds, so commit and reveal must
// agree on it.
type Payment struct {
	From      []byte
	Seq       uint64
	Transfers []Transfer
	Fee       uint64
}

// PayCommitTx records a commitment to a future payment without revealing the
// spend secret. `C` binds the payment body + rotation target to this account,
// identified by its address (64 lowercase hex chars).
type PayCommitTx struct {
	Acct    []byte
	C       [32]byte
	TExpire uint64
	Fee     uint64
}

func (m *PayCommitTx) Type() TxType { return TxPayCommit }

func (m *PayCommitTx) Marshal() []byte {
	buf := make([]byte, 1+binary.MaxVarintLen64+len(m.Acct)+hashLen+2*binary.MaxVarintLen64)
	n := 1
	buf[0] = byte(TxPayCommit)
	n += binary.PutUvarint(buf[n:], uint64(len(m.Acct)))
	n += copy(buf[n:], m.Acct)
	n += copy(buf[n:], m.C[:])
	n += binary.PutUvarint(buf[n:], m.TExpire)
	n += binary.PutUvarint(buf[n:], m.Fee)
	return buf[:n]
}

// PayRevealTx authorises a payment by revealing the spend secret `RCurrent`
// (whose hash must equal the account's current P) alongside the full body.
type PayRevealTx struct {
	Body     Payment
	PNext    [32]byte
	N        [32]byte
	TExpire  uint64
	RCurrent [32]byte
}

func (m *PayRevealTx) Type() TxType { return TxPayReveal }

func (m *PayRevealTx) Marshal() []byte {
	body := EncodePayment(m.Body)
	buf := make([]byte, 1+len(body)+3*hashLen+binary.MaxVarintLen64)
	n := 1
	buf[0] = byte(TxPayReveal)
	n += copy(buf[n:], body)
	n += copy(buf[n:], m.PNext[:])
	n += copy(buf[n:], m.N[:])
	n += binary.PutUvarint(buf[n:], m.TExpire)
	n += copy(buf[n:], m.RCurrent[:])
	return buf[:n]
}

// Parse decodes a raw transaction into a typed Message.
func Parse(raw []byte) (Message, error) {
	if len(raw) < 1 {
		return nil, errors.New("tx: empty transaction")
	}
	switch TxType(raw[0]) {
	case TxCommit:
		return parseCommit(raw[1:])
	case TxReveal:
		return parseReveal(raw[1:])
	case TxPayCommit:
		return parsePayCommit(raw[1:])
	case TxPayReveal:
		return parsePayReveal(raw[1:])
	default:
		return nil, fmt.Errorf("tx: unknown type tag %d", raw[0])
	}
}

func parseCommit(b []byte) (*CommitTx, error) {
	key, rest, err := readBytes(b)
	if err != nil {
		return nil, err
	}
	if len(rest) != hashLen {
		return nil, fmt.Errorf("tx: commit hash is %d bytes, want %d", len(rest), hashLen)
	}
	var h [32]byte
	copy(h[:], rest)
	return &CommitTx{Key: key, Hash: h}, nil
}

func parseReveal(b []byte) (*RevealTx, error) {
	key, rest, err := readBytes(b)
	if err != nil {
		return nil, err
	}
	value, _, err := readBytes(rest)
	if err != nil {
		return nil, err
	}
	return &RevealTx{Key: key, Value: value}, nil
}

func parsePayCommit(b []byte) (*PayCommitTx, error) {
	acct, rest, err := readBytes(b)
	if err != nil {
		return nil, err
	}
	if len(rest) < hashLen {
		return nil, errors.New("tx: pay commit hash truncated")
	}
	var c [32]byte
	copy(c[:], rest[:hashLen])
	rest = rest[hashLen:]
	texp, rest, err := readUvar(rest)
	if err != nil {
		return nil, err
	}
	fee, _, err := readUvar(rest)
	if err != nil {
		return nil, err
	}
	return &PayCommitTx{Acct: acct, C: c, TExpire: texp, Fee: fee}, nil
}

func parsePayReveal(b []byte) (*PayRevealTx, error) {
	body, rest, err := readPayment(b)
	if err != nil {
		return nil, err
	}
	var pnext, n, rcurrent [32]byte
	for _, out := range []*[32]byte{&pnext, &n} {
		if len(rest) < hashLen {
			return nil, errors.New("tx: pay reveal hash truncated")
		}
		copy(out[:], rest[:hashLen])
		rest = rest[hashLen:]
	}
	texp, rest, err := readUvar(rest)
	if err != nil {
		return nil, err
	}
	if len(rest) < hashLen {
		return nil, errors.New("tx: pay reveal RCurrent truncated")
	}
	copy(rcurrent[:], rest[:hashLen])
	return &PayRevealTx{Body: body, PNext: pnext, N: n, TExpire: texp, RCurrent: rcurrent}, nil
}

// readPayment parses a canonical Payment and returns it plus the remaining bytes.
func readPayment(b []byte) (Payment, []byte, error) {
	var p Payment
	from, rest, err := readBytes(b)
	if err != nil {
		return p, nil, err
	}
	p.From = from
	p.Seq, rest, err = readUvar(rest)
	if err != nil {
		return p, nil, err
	}
	count, rest, err := readUvar(rest)
	if err != nil {
		return p, nil, err
	}
	p.Transfers = make([]Transfer, 0, count)
	for i := uint64(0); i < count; i++ {
		to, r, err := readBytes(rest)
		if err != nil {
			return p, nil, err
		}
		amt, r, err := readUvar(r)
		if err != nil {
			return p, nil, err
		}
		p.Transfers = append(p.Transfers, Transfer{To: to, Amount: amt})
		rest = r
	}
	p.Fee, rest, err = readUvar(rest)
	if err != nil {
		return p, nil, err
	}
	return p, rest, nil
}

// readUvar reads a single uvarint field, returning the value and the remainder.
func readUvar(b []byte) (uint64, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errors.New("tx: malformed uvarint field")
	}
	return v, b[n:], nil
}

// ---- commitment construction (single source of truth) ----

// DefaultChainID is the chain identifier folded into every commitment. Real
// nodes can override it; commit, reveal and the CLI must all use the same value
// or the recomputed commitment will not match the stored one.
const DefaultChainID = "sealstore"

// DomainTag separates this scheme's commitments from any other hash usage.
const DomainTag = "SIGLESS_COMMIT_V1"

// EncodePayment is the canonical binary encoding of a Payment used both for the
// wire form of a PayRevealTx and for the commitment hash, so commit and reveal
// agree byte-for-byte.
func EncodePayment(p Payment) []byte {
	s := varintSize(uint64(len(p.From))) + len(p.From)
	s += varintSize(p.Seq)
	s += varintSize(uint64(len(p.Transfers)))
	for _, tr := range p.Transfers {
		s += varintSize(uint64(len(tr.To))) + len(tr.To) + varintSize(tr.Amount)
	}
	s += varintSize(p.Fee)
	buf := make([]byte, s)
	n := 0
	n += binary.PutUvarint(buf[n:], uint64(len(p.From)))
	n += copy(buf[n:], p.From)
	n += binary.PutUvarint(buf[n:], p.Seq)
	n += binary.PutUvarint(buf[n:], uint64(len(p.Transfers)))
	for _, tr := range p.Transfers {
		n += binary.PutUvarint(buf[n:], uint64(len(tr.To)))
		n += copy(buf[n:], tr.To)
		n += binary.PutUvarint(buf[n:], tr.Amount)
	}
	n += binary.PutUvarint(buf[n:], p.Fee)
	return buf[:n]
}

// PayHash is Hash(PaymentBody) from the spec — hashing the canonical body.
func PayHash(p Payment) [32]byte {
	return sha256.Sum256(EncodePayment(p))
}

// CommitHash computes the commitment C per the spec:
//
//	C = Hash("SIGLESS_COMMIT_V1" || chain_id || A || pay_hash || P_next || N || t_expire)
//
// Variable-length inputs (chain_id, A) are uvarint-length-prefixed so the
// framing is unambiguous.
func CommitHash(chainID, acct []byte, p Payment, pNext, nonce [32]byte, tExpire uint64) [32]byte {
	pay := PayHash(p)
	s := len(DomainTag) + varintSize(uint64(len(chainID))) + len(chainID) +
		varintSize(uint64(len(acct))) + len(acct) + hashLen + hashLen + hashLen + varintSize(tExpire)
	buf := make([]byte, s)
	n := 0
	n += copy(buf[n:], DomainTag)
	n += binary.PutUvarint(buf[n:], uint64(len(chainID)))
	n += copy(buf[n:], chainID)
	n += binary.PutUvarint(buf[n:], uint64(len(acct)))
	n += copy(buf[n:], acct)
	n += copy(buf[n:], pay[:])
	n += copy(buf[n:], pNext[:])
	n += copy(buf[n:], nonce[:])
	n += binary.PutUvarint(buf[n:], tExpire)
	return sha256.Sum256(buf[:n])
}

// varintSize returns an upper bound for the encoded size of v.
func varintSize(v uint64) int { return binary.MaxVarintLen64 }

// ParseAddress validates a public account address: exactly 64 lowercase hex
// chars (a sha256 digest). The address IS the on-chain account key, and its
// bytes seed the account's auth hash P when the account is first created —
// so the chain never invents a hash: funds credited to anything else would
// be locked forever (no preimage) or spendable by anyone (public string).
func ParseAddress(addr []byte) ([32]byte, error) {
	var out [32]byte
	if len(addr) != 2*hashLen {
		return out, fmt.Errorf("tx: address %q is not %d hex chars", addr, 2*hashLen)
	}
	for _, c := range addr {
		// hex.DecodeString alone would accept uppercase, which would let two
		// spellings of one address create two accounts.
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return out, fmt.Errorf("tx: address %q is not lowercase hex", addr)
		}
	}
	h, err := hex.DecodeString(string(addr))
	if err != nil || len(h) != hashLen {
		return out, fmt.Errorf("tx: address %q is not valid hex", addr)
	}
	copy(out[:], h)
	return out, nil
}

// readBytes reads a uvarint length prefix and returns that many bytes plus the
// remainder. n<=0 covers both truncation (n==0) and varint overflow (n<0); the
// bounds check rejects lengths that exceed the remaining data.
func readBytes(b []byte) (val, rest []byte, err error) {
	l, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, nil, errors.New("tx: malformed length prefix")
	}
	if l > uint64(len(b)-n) {
		return nil, nil, errors.New("tx: length prefix exceeds remaining data")
	}
	end := n + int(l)
	return b[n:end], b[end:], nil
}
