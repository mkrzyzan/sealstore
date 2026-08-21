package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"

	"sealstore/internal/tx"
)

// newTestApp opens an in-memory Badger instance so tests touch no filesystem.
func newTestApp(t *testing.T) *MyApp {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))
	if err != nil {
		t.Fatalf("opening in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return SealstoreAbciApp(db)
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

// ---- signatureless payment scheme helpers ----

// acctKey builds the on-chain key of the account at address addr.
func acctKey(addr string) []byte { return []byte("a/" + addr) }

// addrOf returns the public address of a spend secret r: hex(Hash(r)). The
// account at that address is seeded with P = Hash(r) at first credit.
func addrOf(r []byte) string { return hex.EncodeToString(hash(r)) }

// genesisFor builds a genesis app_state crediting the given accounts, given
// as address/balance pairs: genesisFor(bob, 100, eve, 0).
func genesisFor(pairs ...any) string {
	var b strings.Builder
	b.WriteString(`{"accounts":[`)
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"address":%q,"balance":%d}`, pairs[i], pairs[i+1])
	}
	b.WriteString("]}")
	return b.String()
}

// initChain runs InitChain with the given genesis app_state JSON.
func initChain(t *testing.T, app *MyApp, appState string) {
	t.Helper()
	if _, err := app.InitChain(context.Background(), &abcitypes.RequestInitChain{AppStateBytes: []byte(appState)}); err != nil {
		t.Fatalf("InitChain: %v", err)
	}
}

// payCommitTx commits a payment for the account at address `acct` with
// commitment C.
func payCommitTx(acct string, c [32]byte, texp, fee uint64) []byte {
	return (&tx.PayCommitTx{Acct: []byte(acct), C: c, TExpire: texp, Fee: fee}).Marshal()
}

// payRevealTx builds a PayRevealTx over the given body/hashes.
func payRevealTx(body tx.Payment, pnext, n, rcurrent []byte, texp uint64) []byte {
	var pn, nn, rc [32]byte
	copy(pn[:], pnext)
	copy(nn[:], n)
	copy(rc[:], rcurrent)
	return (&tx.PayRevealTx{Body: body, PNext: pn, N: nn, TExpire: texp, RCurrent: rc}).Marshal()
}

// applyCode runs one block and returns the first tx's exec code (0 = accepted).
func applyCode(t *testing.T, app *MyApp, raw []byte) uint32 {
	t.Helper()
	return apply(t, app, raw).TxResults[0].Code
}

// b32 pads/copies b into a fixed 32-byte field.
func b32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b)
	return out
}

// commitFor builds the commitment C for a payment from the account at address
// `acct` with the given rotation target, nonce and expiry.
func commitFor(acct string, body tx.Payment, pnext, nonce []byte, texp uint64) [32]byte {
	return tx.CommitHash([]byte(tx.DefaultChainID), []byte(acct), body, b32(pnext), b32(nonce), texp)
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
		[]byte("nonsense"),                     // invalid type tag
		[]byte{byte(tx.TxCommit)},              // tag only, no key/hash
		[]byte{byte(tx.TxCommit), 0x01, 'k'},   // key but truncated hash
		revealTx("key2", nil),                  // empty value reveal
		[]byte{byte(tx.TxTransfer), 0x01, 'k'}, // removed transfer tag must stay unknown
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

func accountState(t *testing.T, app *MyApp, addr string) AccountState {
	t.Helper()
	q := query(t, app, acctKey(addr))
	if q.Log != "exists" {
		t.Fatalf("account %q missing: %q", addr, q.Log)
	}
	var a AccountState
	if err := a.fromString(string(q.Value)); err != nil {
		t.Fatalf("account %q decode: %v", addr, err)
	}
	return a
}

func TestPayRevealOpensPayeeAccount(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-first-secret"))
	bob := addrOf(r0)
	initChain(t, app, genesisFor(bob, 100))

	// Carol has never received funds; her account does not exist yet.
	carolR := hash([]byte("carol-secret"))
	carol := addrOf(carolR)
	if q := query(t, app, acctKey(carol)); q.Log != "key does not exist" {
		t.Fatalf("carol account pre-exists; Query log = %q", q.Log)
	}

	// Bob pays carol; the credit lazily creates her account keyed by her
	// address, with P seeded from the address itself.
	p1 := hash([]byte("bob-next"))
	nonce := []byte("n")
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 10}}, Fee: 0}
	c := commitFor(bob, body, p1, nonce, 100)
	applyCode(t, app, payCommitTx(bob, c, 100, 0))
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, r0, 100)); code != 0 {
		t.Fatalf("reveal code = %d, want 0", code)
	}

	ca := accountState(t, app, carol)
	if ca.Balance != 10 {
		t.Errorf("carol balance = %d, want 10", ca.Balance)
	}
	if ca.Seq != 0 {
		t.Errorf("carol seq = %d, want 0", ca.Seq)
	}
	if !bytes.Equal(ca.P[:], hash(carolR)) {
		t.Errorf("carol P = %x, want %x (seeded from her address)", ca.P, hash(carolR))
	}
}

func TestRevealBadPayeeRejected(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	bob := addrOf(r0)
	initChain(t, app, genesisFor(bob, 100))

	// A payment leg to a non-address is rejected at FinalizeBlock: the chain
	// never invents a hash (funds could lock forever).
	p1 := hash([]byte("bob-next"))
	nonce := []byte("n")
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte("bob"), Amount: 1}}, Fee: 0}
	c := commitFor(bob, body, p1, nonce, 100)
	applyCode(t, app, payCommitTx(bob, c, 100, 0))
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, r0, 100)); code == 0 {
		t.Fatal("reveal to bad payee accepted, want rejection")
	}
	if q := query(t, app, []byte("a/bob")); q.Log != "key does not exist" {
		t.Fatalf("account created despite rejection; Query log = %q", q.Log)
	}
}

func TestInitChainPreCredits(t *testing.T) {
	app := newTestApp(t)
	rA := hash([]byte("alice-secret"))
	rB := hash([]byte("bobs-seed"))
	alice, bob := addrOf(rA), addrOf(rB)

	initChain(t, app, genesisFor(alice, 1000000, bob, 5))

	a := accountState(t, app, alice)
	if a.Balance != 1000000 || a.Seq != 0 {
		t.Errorf("alice = %d/%d, want 1000000/0", a.Balance, a.Seq)
	}
	if !bytes.Equal(a.P[:], hash(rA)) {
		t.Errorf("alice P = %x, want %x (seeded from her address)", a.P, hash(rA))
	}
	b := accountState(t, app, bob)
	if b.Balance != 5 {
		t.Errorf("bob balance = %d, want 5", b.Balance)
	}

	// A pre-credited account is immediately spendable via commit+reveal.
	p1 := hash([]byte("alice-next"))
	nonce := []byte("n")
	body := tx.Payment{From: []byte(alice), Seq: 1, Transfers: []tx.Transfer{{To: []byte(bob), Amount: 7}}, Fee: 1}
	c := commitFor(alice, body, p1, nonce, 1000)
	applyCode(t, app, payCommitTx(alice, c, 1000, 1))
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, rA, 1000)); code != 0 {
		t.Fatalf("spend from genesis account rejected: code = %d", code)
	}
	b2 := accountState(t, app, bob)
	if b2.Balance != 5+7 {
		t.Errorf("bob balance after payment = %d, want %d", b2.Balance, 5+7)
	}
}

func TestInitChainValidates(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		appState string
	}{
		{"bad address", `{"accounts":[{"address":"bob","balance":1}]}`},
		{"uppercase address", fmt.Sprintf(`{"accounts":[{"address":%q,"balance":1}]}`, strings.ToUpper(addrOf(hash([]byte("x")))))},
		{"malformed json", `{"accounts":`},
	} {
		app := newTestApp(t)
		_, err := app.InitChain(ctx, &abcitypes.RequestInitChain{AppStateBytes: []byte(tc.appState)})
		if err == nil {
			t.Errorf("%s: InitChain succeeded, want error", tc.name)
		}
	}

	// Empty or absent app_state is a no-op, not an error.
	app := newTestApp(t)
	if _, err := app.InitChain(ctx, &abcitypes.RequestInitChain{AppStateBytes: nil}); err != nil {
		t.Fatalf("InitChain(nil): %v", err)
	}
	if _, err := app.InitChain(ctx, &abcitypes.RequestInitChain{AppStateBytes: []byte(`{}`)}); err != nil {
		t.Fatalf("InitChain(empty): %v", err)
	}
}

func TestRevealHappyPathRotatesAndApplies(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	r1 := hash([]byte("bob-next"))
	p1 := hash(r1)
	bob := addrOf(r0)
	carol := addrOf(hash([]byte("carol-seed")))

	initChain(t, app, genesisFor(bob, 100))

	// Bob commits a payment of 10 to carol with a fee.
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 10}}, Fee: 5}
	nonce := []byte("nonce-01")
	c := commitFor(bob, body, p1, nonce, 100)
	if code := applyCode(t, app, payCommitTx(bob, c, 100, 5)); code != 0 {
		t.Fatalf("commit code = %d, want 0", code)
	}

	body.Fee = 5
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, r0, 100)); code != 0 {
		t.Fatalf("reveal code = %d, want 0", code)
	}

	b := accountState(t, app, bob)
	if b.Balance != 100-5-10 {
		t.Errorf("bob balance = %d, want %d", b.Balance, 100-5-10)
	}
	if b.Seq != 1 {
		t.Errorf("bob seq = %d, want 1", b.Seq)
	}
	if !bytes.Equal(b.P[:], p1) {
		t.Errorf("bob P = %x, want %x (rotated)", b.P, p1)
	}
	ca := accountState(t, app, carol)
	if ca.Balance != 10 {
		t.Errorf("carol balance = %d, want 10", ca.Balance)
	}

	// Active commit is consumed: a second reveal of the same body is a replay
	// and must be rejected (no active commit remains).
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, r0, 100)); code == 0 {
		t.Fatal("replayed reveal accepted, want rejection")
	}

	// The old R0 no longer authorises either (P rotated to p1).
	spent := tx.Payment{From: []byte(bob), Seq: 2, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 1}}, Fee: 0}
	c2 := commitFor(bob, spent, p1, []byte("n2"), 100)
	applyCode(t, app, payCommitTx(bob, c2, 100, 0))
	if code := applyCode(t, app, payRevealTx(spent, p1, []byte("n2"), r0, 100)); code == 0 {
		t.Fatal("reveal with stale R_current accepted, want nonzero")
	}
}

func TestReplacementTheftRejected(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	mine := hash([]byte("attacker-secret"))
	bob := addrOf(r0)
	eve := addrOf(mine)

	initChain(t, app, genesisFor(bob, 100, eve, 0))

	// Bob commits his legitimate payment (to carol) first.
	carol := addrOf(hash([]byte("c-seed")))
	nonce := []byte("committed-n")
	p1 := hash([]byte("bob-next"))
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 10}}, Fee: 5}
	c := commitFor(bob, body, p1, nonce, 100)
	if code := applyCode(t, app, payCommitTx(bob, c, 100, 5)); code != 0 {
		t.Fatalf("commit code = %d, want 0", code)
	}

	// A miner substitutes a different payee (eve) in the revealed body. Its
	// recomputed commitment cannot match the stored C, so it is rejected even
	// though the authorising secret r0 (hash == bob.P) is correct.
	evil := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(eve), Amount: 10}}, Fee: 5}
	if code := applyCode(t, app, payRevealTx(evil, hash([]byte("evil-next")), []byte("evil-n"), r0, 100)); code == 0 {
		t.Fatal("replacement attack accepted, want rejection")
	}

	// The attacker's account must not get credited.
	m := accountState(t, app, eve)
	if m.Balance != 0 {
		t.Errorf("eve balance = %d, want 0", m.Balance)
	}
}

func TestWrongSpendSecretRejected(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	bob := addrOf(r0)
	carol := addrOf(hash([]byte("c-seed")))

	initChain(t, app, genesisFor(bob, 100))

	nonce := []byte("n")
	p1 := hash([]byte("bob-next"))
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 1}}, Fee: 0}
	c := commitFor(bob, body, p1, nonce, 100)
	applyCode(t, app, payCommitTx(bob, c, 100, 0))

	// Correct nonce and rotation, but the wrong spend secret: hash(R_wrong) != P.
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, []byte("wrong-secret"), 100)); code == 0 {
		t.Fatal("reveal with wrong R_current accepted, want nonzero")
	}
}

func TestCommitExclusivity(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	bob := addrOf(r0)
	carol := addrOf(hash([]byte("c-seed")))

	initChain(t, app, genesisFor(bob, 100))

	p1 := hash([]byte("bob-next"))
	nonce := []byte("n")
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 1}}, Fee: 0}
	c := commitFor(bob, body, p1, nonce, 1000)

	// First commit is active (far enough that it has not expired).
	if code := applyCode(t, app, payCommitTx(bob, c, 1000, 0)); code != 0 {
		t.Fatalf("commit code = %d, want 0", code)
	}
	// A second commit on the same account is blocked while the first is active.
	c2 := commitFor(bob, body, p1, []byte("n2"), 1000)
	if code := applyCode(t, app, payCommitTx(bob, c2, 1000, 0)); code == 0 {
		t.Fatal("second commit while one is pending accepted, want rejection")
	}
}

func TestCommitLazyExpiryOverwrite(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	bob := addrOf(r0)
	carol := addrOf(hash([]byte("c-seed")))

	initChain(t, app, genesisFor(bob, 100))

	p1 := hash([]byte("bob-next"))
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 1}}, Fee: 0}
	c0 := commitFor(bob, body, p1, []byte("n0"), 0)
	c1 := commitFor(bob, body, p1, []byte("n1"), 100)

	// First commit expires immediately (texp=0). By the time a second commit
	// arrives the clock is past 0, so it is allowed to overwrite lazily.
	if code := applyCode(t, app, payCommitTx(bob, c0, 0, 0)); code != 0 {
		t.Fatalf("commit code = %d, want 0", code)
	}
	if code := applyCode(t, app, payCommitTx(bob, c1, 1, 0)); code != 0 {
		t.Fatalf("secondary commit (lazy overwrite) code = %d, want 0", code)
	}
}

func TestRevealExpiredRejected(t *testing.T) {
	app := newTestApp(t)
	r0 := hash([]byte("bob-current"))
	bob := addrOf(r0)
	carol := addrOf(hash([]byte("c-seed")))

	initChain(t, app, genesisFor(bob, 100))

	p1 := hash([]byte("bob-next"))
	nonce := []byte("n")
	body := tx.Payment{From: []byte(bob), Seq: 1, Transfers: []tx.Transfer{{To: []byte(carol), Amount: 1}}, Fee: 0}
	// texp=0: the commit block leaves the clock at 1, so by reveal time the
	// commitment is already past its expiry.
	c := commitFor(bob, body, p1, nonce, 0)
	if code := applyCode(t, app, payCommitTx(bob, c, 0, 0)); code != 0 {
		t.Fatalf("commit code = %d, want 0", code)
	}
	if code := applyCode(t, app, payRevealTx(body, p1, nonce, r0, 0)); code == 0 {
		t.Fatal("reveal after t_expire accepted, want rejection")
	}
}
