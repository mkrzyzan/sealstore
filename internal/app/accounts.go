package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dgraph-io/badger/v3"

	"sealstore/internal/tx"
)

// hash returns the sha256 digest of b — the generic chain primitive. The
// account scheme uses it for commitment payloads and the spend-secret check.
func hash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// ---- on-chain account state ----
//
// Account state and active-commit records are stored as small text blobs
// ("<balance>|<seq>|<hex P>", "<hex C>|<fee>|<t_expire>") so both the ABCI app
// and the standalone CLI (which keeps its own decoder, to stay independent of
// the app package and its Badger dependency) can decode them with plain string
// splitting.

// AccountState is the mutable on-chain state of one account.
type AccountState struct {
	Balance uint64
	Seq     uint64 // monotonically increasing; anti-replay and ordering
	P       [32]byte
}

func (a *AccountState) fromString(s string) error {
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return fmt.Errorf("account: malformed state %q", s)
	}
	var err error
	a.Balance, err = strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return fmt.Errorf("account: bad balance %q", parts[0])
	}
	a.Seq, err = strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("account: bad seq %q", parts[1])
	}
	if len(parts[2]) != 64 {
		return fmt.Errorf("account: bad P %q", parts[2])
	}
	p, err := hex.DecodeString(parts[2])
	if err != nil || len(p) != 32 {
		return fmt.Errorf("account: bad P %q", parts[2])
	}
	copy(a.P[:], p)
	return nil
}

func (a *AccountState) toString() string {
	return fmt.Sprintf("%d|%d|%s", a.Balance, a.Seq, hex.EncodeToString(a.P[:]))
}

// ActiveCommit is the recorded, spend-bound commitment for one account.
type ActiveCommit struct {
	C       [32]byte
	Fee     uint64
	TExpire uint64
}

func (c *ActiveCommit) fromString(s string) error {
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return fmt.Errorf("active: malformed state %q", s)
	}
	if len(parts[0]) != 64 {
		return fmt.Errorf("active: bad C %q", parts[0])
	}
	cv, err := hex.DecodeString(parts[0])
	if err != nil || len(cv) != 32 {
		return fmt.Errorf("active: bad C %q", parts[0])
	}
	copy(c.C[:], cv)
	c.Fee, err = strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("active: bad fee %q", parts[1])
	}
	c.TExpire, err = strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return fmt.Errorf("active: bad t_expire %q", parts[2])
	}
	return nil
}

func (c *ActiveCommit) toString() string {
	return fmt.Sprintf("%s|%d|%d", hex.EncodeToString(c.C[:]), c.Fee, c.TExpire)
}

// ---- key namespaces ----

const (
	accountPrefix      = "a/"
	activeCommitPrefix = "ac/"
	heightKey          = "hgt"
)

func accountKey(acct []byte) []byte { return append([]byte(accountPrefix), acct...) }

func activeKey(acct []byte) []byte { return append([]byte(activeCommitPrefix), acct...) }

// ChainID returns the chain identifier folded into every commitment.
func ChainID() []byte { return []byte(tx.DefaultChainID) }

// ---- reading / writing ----

// getStr reads a value, distinguishing "absent" from "error".
func getStr(txn *badger.Txn, key []byte) (val []byte, ok bool, err error) {
	item, err := txn.Get(key)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	var got []byte
	if err := item.Value(func(v []byte) error {
		got = append([]byte(nil), v...)
		return nil
	}); err != nil {
		return nil, false, err
	}
	return got, true, nil
}

func loadAccount(txn *badger.Txn, acct []byte) (*AccountState, error) {
	raw, ok, err := getStr(txn, accountKey(acct))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var a AccountState
	if err := a.fromString(string(raw)); err != nil {
		return nil, err
	}
	return &a, nil
}

func loadActive(txn *badger.Txn, acct []byte) (*ActiveCommit, error) {
	raw, ok, err := getStr(txn, activeKey(acct))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var c ActiveCommit
	if err := c.fromString(string(raw)); err != nil {
		return nil, err
	}
	return &c, nil
}

// height reads the block-height clock. A missing entry is 0 (fresh chain);
// only monotonicity matters, not the actual value.
func height(txn *badger.Txn) (uint64, error) {
	raw, ok, err := getStr(txn, []byte(heightKey))
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	h, _ := strconv.ParseUint(string(raw), 10, 64)
	return h, nil
}

// bumpHeight writes h+1 so reveal-time expiry checks see a rising clock.
func bumpHeight(txn *badger.Txn, h uint64) error {
	return txn.Set([]byte(heightKey), []byte(strconv.FormatUint(h+1, 10)))
}

// ---- handlers (called from FinalizeBlock on the shared block txn) ----

// credit adds amount to the account at address addr, creating the account on
// its first credit: keyed by the address itself, with the auth hash P seeded
// from the address bytes (the account's first hash). This is the only way
// accounts come into existence besides the genesis pre-credit.
func credit(txn *badger.Txn, addr []byte, amount uint64) error {
	a, err := loadAccount(txn, addr)
	if err != nil {
		return err
	}
	if a == nil {
		p, err := tx.ParseAddress(addr)
		if err != nil {
			return err
		}
		a = &AccountState{Balance: amount, Seq: 0, P: p}
	} else {
		a.Balance += amount
	}
	return txn.Set(accountKey(addr), []byte(a.toString()))
}

// genesisAccounts is the app_state schema of the cometbft genesis file:
//
//	{"accounts": [{"address": "<64 hex>", "balance": 1000000}, ...]}
type genesisAccounts struct {
	Accounts []struct {
		Address string `json:"address"`
		Balance uint64 `json:"balance"`
	} `json:"accounts"`
}

// applyGenesis pre-credits the genesis accounts. It runs once from InitChain
// on a fresh chain; each account is keyed by its address with P seeded from
// the address itself, so whoever's wallet derives the address can spend the
// balance. An empty or absent app_state is a no-op.
func (m *MyApp) applyGenesis(appState []byte) error {
	if len(appState) == 0 {
		return nil
	}
	var g genesisAccounts
	if err := json.Unmarshal(appState, &g); err != nil {
		return fmt.Errorf("genesis: malformed app_state: %w", err)
	}
	if len(g.Accounts) == 0 {
		return nil
	}
	txn := m.db.NewTransaction(true)
	defer txn.Discard()
	for i, acc := range g.Accounts {
		if _, err := tx.ParseAddress([]byte(acc.Address)); err != nil {
			return fmt.Errorf("genesis: account %d: %w", i, err)
		}
		if err := credit(txn, []byte(acc.Address), acc.Balance); err != nil {
			return fmt.Errorf("genesis: account %d: %w", i, err)
		}
	}
	return txn.Commit()
}

// processPayCommit reserves a spend: deducts the fee and records the active
// commitment. At most one active (non-expired) commit may exist per account.
func (m *MyApp) processPayCommit(txn *badger.Txn, c *tx.PayCommitTx) error {
	if _, err := tx.ParseAddress(c.Acct); err != nil {
		return err
	}
	acct, err := loadAccount(txn, c.Acct)
	if err != nil {
		return err
	}
	if acct == nil {
		return errors.New("commit: account does not exist")
	}
	if acct.Balance < c.Fee {
		return errors.New("commit: insufficient balance for fee")
	}

	existing, err := loadActive(txn, c.Acct)
	if err != nil {
		return err
	}
	h, err := height(txn)
	if err != nil {
		return err
	}
	// A commit overwrites only an expired one (lazy expiry); an active one
	// blocks until it is revealed or expires.
	if existing != nil && h <= existing.TExpire {
		return errors.New("commit: active commit pending")
	}

	acct.Balance -= c.Fee
	if err := txn.Set(accountKey(c.Acct), []byte(acct.toString())); err != nil {
		return err
	}
	ac := &ActiveCommit{C: c.C, Fee: c.Fee, TExpire: c.TExpire}
	return txn.Set(activeKey(c.Acct), []byte(ac.toString()))
}

// processPayReveal authorises the payment. It recomputes the commitment from the
// revealed body and requires an exact match, verifies the spend secret, then
// applies the transfers and consumes the commitment.
func (m *MyApp) processPayReveal(txn *badger.Txn, r *tx.PayRevealTx) error {
	acct := r.Body.From
	if _, err := tx.ParseAddress(acct); err != nil {
		return err
	}
	for _, tr := range r.Body.Transfers {
		if _, err := tx.ParseAddress(tr.To); err != nil {
			return err
		}
	}
	state, err := loadAccount(txn, acct)
	if err != nil {
		return err
	}
	if state == nil {
		return errors.New("reveal: account does not exist")
	}

	// Recompute the commitment and require an exact match against what was
	// committed — a substituted body cannot satisfy the pre-recorded C.
	cExpected := tx.CommitHash(ChainID(), acct, r.Body, r.PNext, r.N, r.TExpire)
	stored, err := loadActive(txn, acct)
	if err != nil {
		return err
	}
	if stored == nil {
		return errors.New("reveal: no active commit")
	}
	if !bytes.Equal(stored.C[:], cExpected[:]) {
		return errors.New("reveal: commitment mismatch (replacement rejected)")
	}

	h, err := height(txn)
	if err != nil {
		return err
	}
	if h > stored.TExpire {
		return errors.New("reveal: commitment expired")
	}

	// Authorisation: knowledge of the current spend secret.
	if !bytes.Equal(hash(r.RCurrent[:]), state.P[:]) {
		return errors.New("reveal: spend secret does not authorize (bad R_current)")
	}

	// Replay / ordering.
	if !bytes.Equal(r.Body.From, acct) || r.Body.Seq != state.Seq+1 {
		return errors.New("reveal: sequence out of order or replay")
	}
	if r.Body.Fee != stored.Fee {
		return errors.New("reveal: fee does not match the committed fee")
	}

	// Balance check (the fee was already charged at commit).
	var total uint64
	for _, tr := range r.Body.Transfers {
		total += tr.Amount
	}
	if state.Balance < total {
		return errors.New("reveal: insufficient balance")
	}

	// Debit the payer (the fee was already charged at commit).
	state.Balance -= total

	// Credit each payee; a payee receiving for the first time has its account
	// created (keyed by address, P seeded from the address itself).
	for _, tr := range r.Body.Transfers {
		if err := credit(txn, tr.To, tr.Amount); err != nil {
			return err
		}
	}

	// Key rotation + replay protection.
	state.P = r.PNext
	state.Seq += 1
	if err := txn.Set(accountKey(acct), []byte(state.toString())); err != nil {
		return err
	}
	// Consume the commitment — it cannot be replayed.
	return txn.Delete(activeKey(acct))
}
