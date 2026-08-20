package main

import (
	"bytes"
	"context"
	"testing"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"

	"sealstore/tx"
)

// newTestApp opens an in-memory Badger instance so tests touch no filesystem.
func newTestApp(t *testing.T) *MyApp {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))
	if err != nil {
		t.Fatalf("opening in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewMyApp(db)
}

// apply runs a block of txs through FinalizeBlock, then Commit to flush to the
// DB, and returns the FinalizeBlock response (mirrors the real ABCI flow).
func apply(t *testing.T, app *MyApp, txs ...[]byte) *abcitypes.ResponseFinalizeBlock {
	t.Helper()
	resp, err := app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{Txs: txs})
	if err != nil {
		t.Fatalf("FinalizeBlock: %v", err)
	}
	if _, err := app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return resp
}

// query looks up a key through the app's Query handler.
func query(t *testing.T, app *MyApp, key []byte) *abcitypes.ResponseQuery {
	t.Helper()
	resp, err := app.Query(context.Background(), &abcitypes.RequestQuery{Data: key})
	if err != nil {
		t.Fatalf("Query(%q): %v", key, err)
	}
	return resp
}

// commitTx marshals a commit message into its binary wire form. hash must
// already be 32 bytes (a sha256 digest).
func commitTx(key string, hash []byte) []byte {
	var h [32]byte
	copy(h[:], hash)
	return (&tx.CommitTx{Key: []byte(key), Hash: h}).Marshal()
}

// revealTx marshals a reveal message into its binary wire form.
func revealTx(key string, value []byte) []byte {
	return (&tx.RevealTx{Key: []byte(key), Value: value}).Marshal()
}

func TestIsValid(t *testing.T) {
	app := newTestApp(t)

	valid := [][]byte{
		commitTx("key1", make([]byte, 32)),
		revealTx("key2", []byte("value")),
	}
	for _, raw := range valid {
		if code := app.isValid(raw); code != 0 {
			t.Errorf("isValid(%x) = %d, want 0", raw, code)
		}
	}

	invalid := [][]byte{
		nil,
		[]byte("nonsense"),                   // invalid type tag
		[]byte{byte(tx.TxCommit)},            // tag only, no key/hash
		[]byte{byte(tx.TxCommit), 0x01, 'k'}, // key but truncated hash
		revealTx("key2", nil),                // empty value reveal
	}
	for _, raw := range invalid {
		if code := app.isValid(raw); code == 0 {
			t.Errorf("isValid(%x) = 0, want nonzero", raw)
		}
	}
}

func TestCommitTxStoresCommitment(t *testing.T) {
	app := newTestApp(t)

	secret := []byte("top secret")
	cp := hash(secret)
	raw := commitTx("key1", cp)

	resp := apply(t, app, raw)
	if resp.TxResults[0].Code != 0 {
		t.Fatalf("commit tx code = %d, want 0", resp.TxResults[0].Code)
	}

	q := query(t, app, []byte("commit/key1"))
	if q.Log != "exists" {
		t.Fatalf("commit not stored; Query log = %q", q.Log)
	}
	if !bytes.Equal(q.Value, cp) {
		t.Fatalf("stored commitment %q, want %q", q.Value, cp)
	}
}

func TestRevealVerifiesAndStores(t *testing.T) {
	app := newTestApp(t)

	secret := []byte("the real secret")
	cp := hash(secret)

	apply(t, app, commitTx("key2", cp))

	resp := apply(t, app, revealTx("key2", secret))
	if resp.TxResults[0].Code != 0 {
		t.Fatalf("reveal tx code = %d, want 0", resp.TxResults[0].Code)
	}

	q := query(t, app, []byte("key2"))
	if q.Log != "exists" {
		t.Fatalf("final value not stored; Query log = %q", q.Log)
	}
	if !bytes.Equal(q.Value, secret) {
		t.Fatalf("stored value %q, want %q", q.Value, secret)
	}

	// The commitment is kept after a successful reveal so it stays verifiable.
	qc := query(t, app, []byte("commit/key2"))
	if qc.Log != "exists" {
		t.Fatalf("commitment should remain after reveal; Query log = %q", qc.Log)
	}
	if !bytes.Equal(qc.Value, cp) {
		t.Fatalf("stored commitment %q, want %q", qc.Value, cp)
	}
}

func TestRevealWithoutCommitRejected(t *testing.T) {
	app := newTestApp(t)

	resp := apply(t, app, revealTx("keyX", []byte("value")))
	if resp.TxResults[0].Code == 0 {
		t.Fatal("reveal without prior commit: code = 0, want nonzero")
	}

	if q := query(t, app, []byte("keyX")); q.Log != "key does not exist" {
		t.Fatalf("value stored despite rejection; Query log = %q", q.Log)
	}
}

func TestRevealMismatchRejected(t *testing.T) {
	app := newTestApp(t)

	// Commit to hash("expected") but reveal a different value.
	apply(t, app, commitTx("keyY", hash([]byte("expected"))))

	resp := apply(t, app, revealTx("keyY", []byte("actual-value")))
	if resp.TxResults[0].Code == 0 {
		t.Fatal("reveal with mismatching hash: code = 0, want nonzero")
	}

	if q := query(t, app, []byte("keyY")); q.Log != "key does not exist" {
		t.Fatalf("value stored despite mismatch; Query log = %q", q.Log)
	}
}

func TestMalformedRejected(t *testing.T) {
	app := newTestApp(t)

	for _, raw := range [][]byte{
		[]byte("nonsense"),                   // invalid type tag
		[]byte{byte(tx.TxCommit)},            // tag only, no payload
		[]byte{byte(tx.TxCommit), 0x05, 'k'}, // key length prefix exceeds remaining
	} {
		if resp := apply(t, app, raw); resp.TxResults[0].Code == 0 {
			t.Errorf("malformed tx %x: code = 0, want nonzero", raw)
		}
	}
}

func TestCheckTxValidates(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	valid := [][]byte{
		commitTx("key", make([]byte, 32)),
		revealTx("key", []byte("value")),
	}
	for _, raw := range valid {
		resp, err := app.CheckTx(ctx, &abcitypes.RequestCheckTx{Tx: raw})
		if err != nil {
			t.Fatalf("CheckTx(%x): %v", raw, err)
		}
		if resp.Code != 0 {
			t.Errorf("CheckTx(%x) code = %d, want 0", raw, resp.Code)
		}
	}

	invalid := [][]byte{[]byte("nonsense")}
	for _, raw := range invalid {
		resp, err := app.CheckTx(ctx, &abcitypes.RequestCheckTx{Tx: raw})
		if err != nil {
			t.Fatalf("CheckTx(%x): %v", raw, err)
		}
		if resp.Code == 0 {
			t.Errorf("CheckTx(%x) code = 0, want nonzero", raw)
		}
	}
}
