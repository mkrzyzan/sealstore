package main

import (
	"bytes"
	"context"
	"testing"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"
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

func TestParseTx(t *testing.T) {
	tests := []struct {
		name        string
		tx          []byte
		wantTyp     TxType
		wantKey     []byte
		wantPayload []byte
	}{
		{"valid commit", []byte("commit=key1=abc123"), TxCommit, []byte("key1"), []byte("abc123")},
		{"valid reveal", []byte("reveal=key2=value"), TxReveal, []byte("key2"), []byte("value")},
		{"empty tx", nil, TxUnknown, nil, nil},
		{"wrong prefix", []byte("other=key=value"), TxUnknown, nil, nil},
		{"no payload", []byte("commit=key1"), TxUnknown, nil, nil},
		{"bare prefix", []byte("commit"), TxUnknown, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, key, payload := parseTx(tt.tx)
			if typ != tt.wantTyp {
				t.Errorf("parseTx(%q) type = %v, want %v", tt.tx, typ, tt.wantTyp)
			}
			if !bytes.Equal(key, tt.wantKey) {
				t.Errorf("parseTx(%q) key = %q, want %q", tt.tx, key, tt.wantKey)
			}
			if !bytes.Equal(payload, tt.wantPayload) {
				t.Errorf("parseTx(%q) payload = %q, want %q", tt.tx, payload, tt.wantPayload)
			}
		})
	}
}

func TestIsValid(t *testing.T) {
	app := newTestApp(t)

	valid := [][]byte{
		[]byte("commit=key1=abc123"),
		[]byte("reveal=key2=value"),
	}
	for _, tx := range valid {
		if code := app.isValid(tx); code != 0 {
			t.Errorf("isValid(%q) = %d, want 0", tx, code)
		}
	}

	invalid := [][]byte{
		nil,
		[]byte("nonsense"),
		[]byte("commit=key1"), // no payload
		[]byte("other=key=value"),
	}
	for _, tx := range invalid {
		if code := app.isValid(tx); code == 0 {
			t.Errorf("isValid(%q) = 0, want nonzero", tx)
		}
	}
}

func TestCommitTxStoresCommitment(t *testing.T) {
	app := newTestApp(t)

	secret := []byte("top secret")
	cp := hash(secret)
	tx := append([]byte("commit=key1="), cp...)

	resp := apply(t, app, tx)
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

	apply(t, app, append([]byte("commit=key2="), cp...))

	resp := apply(t, app, append([]byte("reveal=key2="), secret...))
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

	resp := apply(t, app, []byte("reveal=keyX=value"))
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
	apply(t, app, []byte("commit=keyY=deadbeef"))

	resp := apply(t, app, []byte("reveal=keyY=actual-value"))
	if resp.TxResults[0].Code == 0 {
		t.Fatal("reveal with mismatching hash: code = 0, want nonzero")
	}

	if q := query(t, app, []byte("keyY")); q.Log != "key does not exist" {
		t.Fatalf("value stored despite mismatch; Query log = %q", q.Log)
	}
}

func TestMalformedRejected(t *testing.T) {
	app := newTestApp(t)

	for _, tx := range [][]byte{
		[]byte("nonsense"),
		[]byte("commit=key"), // no payload,
	} {
		if resp := apply(t, app, tx); resp.TxResults[0].Code == 0 {
			t.Errorf("malformed tx %q: code = 0, want nonzero", tx)
		}
	}
}

func TestCheckTxValidates(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	valid := [][]byte{
		[]byte("commit=key=abc123"),
		[]byte("reveal=key=value"),
	}
	for _, tx := range valid {
		resp, err := app.CheckTx(ctx, &abcitypes.RequestCheckTx{Tx: tx})
		if err != nil {
			t.Fatalf("CheckTx(%q): %v", tx, err)
		}
		if resp.Code != 0 {
			t.Errorf("CheckTx(%q) code = %d, want 0", tx, resp.Code)
		}
	}

	invalid := [][]byte{[]byte("nonsense")}
	for _, tx := range invalid {
		resp, err := app.CheckTx(ctx, &abcitypes.RequestCheckTx{Tx: tx})
		if err != nil {
			t.Fatalf("CheckTx(%q): %v", tx, err)
		}
		if resp.Code == 0 {
			t.Errorf("CheckTx(%q) code = 0, want nonzero", tx)
		}
	}
}
