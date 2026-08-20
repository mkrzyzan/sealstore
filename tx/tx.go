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
	"encoding/binary"
	"errors"
	"fmt"
)

// TxType is the 1-byte leading tag of a serialized transaction.
type TxType uint8

const (
	TxUnknown TxType = iota
	TxCommit
	TxReveal
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
