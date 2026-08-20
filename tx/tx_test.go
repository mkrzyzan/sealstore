package tx

import (
	"bytes"
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
