package tx

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRoundTripCommit(t *testing.T) {
	var h [32]byte
	for i := range h {
		h[i] = byte(i)
	}
	in := &CommitTx{Key: []byte("key1"), Hash: h}
	out, err := Parse(in.Marshal())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := out.(*CommitTx)
	if !ok {
		t.Fatalf("Parse returned %T, want *CommitTx", out)
	}
	if got.Type() != TxCommit {
		t.Errorf("Type() = %d, want %d", got.Type(), TxCommit)
	}
	if !bytes.Equal(got.Key, in.Key) {
		t.Errorf("Key = %q, want %q", got.Key, in.Key)
	}
	if !bytes.Equal(got.Hash[:], in.Hash[:]) {
		t.Errorf("Hash = %x, want %x", got.Hash, in.Hash)
	}
}

func TestRoundTripReveal(t *testing.T) {
	in := &RevealTx{Key: []byte("key2"), Value: []byte("value")}
	out, err := Parse(in.Marshal())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := out.(*RevealTx)
	if !ok {
		t.Fatalf("Parse returned %T, want *RevealTx", out)
	}
	if got.Type() != TxReveal {
		t.Errorf("Type() = %d, want %d", got.Type(), TxReveal)
	}
	if !bytes.Equal(got.Key, in.Key) {
		t.Errorf("Key = %q, want %q", got.Key, in.Key)
	}
	if !bytes.Equal(got.Value, in.Value) {
		t.Errorf("Value = %q, want %q", got.Value, in.Value)
	}
}

func TestRoundTripEmptyKey(t *testing.T) {
	// An empty key is allowed (parity with the old string format's commit==hash).
	in := &CommitTx{Key: nil, Hash: [32]byte{1}}
	out, err := Parse(in.Marshal())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, ok := out.(*CommitTx); !ok || len(got.Key) != 0 {
		t.Fatalf("Parse returned %#v, want an empty-key CommitTx", out)
	}
}

func TestParseEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%v) succeeded, want error", raw)
		}
	}
}

func TestParseUnknownTag(t *testing.T) {
	if _, err := Parse([]byte{0x09, 0x01, 'k'}); err == nil {
		t.Error("Parse with unknown tag succeeded, want error")
	}
}

func TestParseTruncatedVarint(t *testing.T) {
	// Key length prefix sets the continuation bit but is never completed.
	if _, err := Parse([]byte{byte(TxCommit), 0x80}); err == nil {
		t.Error("Parse with truncated length prefix succeeded, want error")
	}
}

func TestParseLengthExceedsData(t *testing.T) {
	// Declared key length 5, but only 3 bytes follow the prefix.
	if _, err := Parse([]byte{byte(TxCommit), 0x05, 'k', 'e', 'y'}); err == nil {
		t.Error("Parse with over-long length prefix succeeded, want error")
	}
}

func TestParseCommitWrongHashLen(t *testing.T) {
	// Valid key, but 35 trailing bytes instead of the required 32.
	raw := append([]byte{byte(TxCommit), 0x01, 'k'}, make([]byte, 35)...)
	if _, err := Parse(raw); err == nil {
		t.Error("Parse with 35-byte commit hash succeeded, want error")
	}
}

func TestParseCommitShortHash(t *testing.T) {
	// Valid key, but only 31 trailing bytes for the hash.
	raw := append([]byte{byte(TxCommit), 0x01, 'k'}, make([]byte, 31)...)
	if _, err := Parse(raw); err == nil {
		t.Error("Parse with 31-byte commit hash succeeded, want error")
	}
}

func TestParseOverflowVarint(t *testing.T) {
	// A 10-byte uvarint whose last byte is 0x02 overflows 64 bits; binary.Uvarint
	// reports it with a negative byte count, which readBytes must reject.
	prefix := bytes.Repeat([]byte{0xff}, 9)
	prefix = append(prefix, 0x02)
	raw := append([]byte{byte(TxCommit)}, prefix...)
	if _, err := Parse(raw); err == nil {
		t.Error("Parse with overflowing length prefix succeeded, want error")
	}
}

// ---- signatureless payment scheme ----

// hexAddr returns a syntactically valid address: 64 lowercase hex chars built
// from a repeated byte pattern.
func hexAddr(b byte) string {
	return hex.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func TestParseAddress(t *testing.T) {
	valid := hexAddr(0x1f)
	raw := bytes.Repeat([]byte{0x1f}, 32)

	h, err := ParseAddress([]byte(valid))
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	if !bytes.Equal(h[:], raw) {
		t.Errorf("decoded = %x, want %x", h[:], raw)
	}

	bad := []string{
		"",                                     // empty
		"bob",                                  // not an address
		"1f1f",                                 // too short
		valid + "a",                            // too long
		string(bytes.Repeat([]byte("zz"), 32)), // 64 chars, not hex
		strings.ToUpper(valid),                 // uppercase is not canonical
	}
	for _, s := range bad {
		if _, err := ParseAddress([]byte(s)); err == nil {
			t.Errorf("ParseAddress(%q) succeeded, want error", s)
		}
	}
}

func TestRoundTripPayCommit(t *testing.T) {
	in := &PayCommitTx{Acct: []byte(hexAddr(0xab)), C: [32]byte{9}, TExpire: 100, Fee: 5}
	out, err := Parse(in.Marshal())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := out.(*PayCommitTx)
	if !ok {
		t.Fatalf("Parse returned %T, want *PayCommitTx", out)
	}
	if !bytes.Equal(got.Acct, in.Acct) || !bytes.Equal(got.C[:], in.C[:]) {
		t.Errorf("Acct/C = %q/%x, want %q/%x", got.Acct, got.C, in.Acct, in.C)
	}
	if got.TExpire != in.TExpire || got.Fee != in.Fee {
		t.Errorf("TExpire/Fee = %d/%d, want %d/%d", got.TExpire, got.Fee, in.TExpire, in.Fee)
	}
}

func TestRoundTripPayReveal(t *testing.T) {
	in := &PayRevealTx{
		Body:     Payment{From: []byte(hexAddr(0xab)), Seq: 3, Transfers: []Transfer{{To: []byte(hexAddr(0xcd)), Amount: 10}, {To: []byte(hexAddr(0xef)), Amount: 4}}, Fee: 5},
		PNext:    [32]byte{1},
		N:        [32]byte{2},
		TExpire:  99,
		RCurrent: [32]byte{3},
	}
	out, err := Parse(in.Marshal())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := out.(*PayRevealTx)
	if !ok {
		t.Fatalf("Parse returned %T, want *PayRevealTx", out)
	}
	if !bytes.Equal(got.Body.From, in.Body.From) || got.Body.Seq != in.Body.Seq || got.Body.Fee != in.Body.Fee {
		t.Errorf("Body = %+v, want %+v", got.Body, in.Body)
	}
	if len(got.Body.Transfers) != len(in.Body.Transfers) {
		t.Fatalf("transfer count = %d, want %d", len(got.Body.Transfers), len(in.Body.Transfers))
	}
	for i := range in.Body.Transfers {
		if !bytes.Equal(got.Body.Transfers[i].To, in.Body.Transfers[i].To) || got.Body.Transfers[i].Amount != in.Body.Transfers[i].Amount {
			t.Errorf("Transfer[%d] = %+v, want %+v", i, got.Body.Transfers[i], in.Body.Transfers[i])
		}
	}
	if !bytes.Equal(got.PNext[:], in.PNext[:]) || !bytes.Equal(got.N[:], in.N[:]) || !bytes.Equal(got.RCurrent[:], in.RCurrent[:]) {
		t.Errorf("hashes PNext/N/RCurrent = %x/%x/%x, want %x/%x/%x", got.PNext, got.N, got.RCurrent, in.PNext, in.N, in.RCurrent)
	}
	if got.TExpire != in.TExpire {
		t.Errorf("TExpire = %d, want %d", got.TExpire, in.TExpire)
	}
}

func TestCommitHashMatchesPayHash(t *testing.T) {
	// The commitment must be deterministic and bind the body: changing any input
	// (including the body) changes C.
	p := Payment{From: []byte(hexAddr(0xab)), Seq: 1, Transfers: []Transfer{{To: []byte(hexAddr(0xcd)), Amount: 7}}, Fee: 2}
	pnext := [32]byte{1}
	nonce := [32]byte{2}
	c1 := CommitHash([]byte("sealstore"), []byte(hexAddr(0xab)), p, pnext, nonce, 100)
	if c1 != CommitHash([]byte("sealstore"), []byte(hexAddr(0xab)), p, pnext, nonce, 100) {
		t.Error("CommitHash not deterministic")
	}
	p2 := p
	p2.Fee = 99
	if c1 == CommitHash([]byte("sealstore"), []byte(hexAddr(0xab)), p2, pnext, nonce, 100) {
		t.Error("CommitHash unchanged despite body change")
	}
	if c1 == CommitHash([]byte("other"), []byte(hexAddr(0xab)), p, pnext, nonce, 100) {
		t.Error("CommitHash unchanged despite chain id change")
	}
}
