// Command cli is a tiny command-line client for the sealstore commit/reveal ABCI
// app. It talks to the running CometBFT node via the official RPC client.
//
// Usage:
//
//	cli                       # interactive REPL (up/down arrow recall)
//	cli health                # one-shot command
//	cli set <key> <value>     # commit then reveal
//	cli commit <key> <value>  # publish sha256(value) as the commitment
//	cli reveal <key> <value>  # reveal value (verified against the commit)
//	cli get <key>             # read the revealed value
//	cli getcommit <key>       # read the stored commitment (sha256 hex)
//	cli history
//
// Signatureless payment scheme:
//
//	cli wallet [seed|-]                                 # create a wallet
//	cli wallets                                         # list wallets
//	cli transfer <from-address> <to-address> <amount> <fee> <texp>
//	                                                    # pay_commit + pay_reveal in one shot
//	cli account <address>                               # inspect an account
//	cli pay_commit <from-address> <to-address> <amount> <fee> <texp>
//	cli pay_reveal <from-address>
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtclient "github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/types"

	"sealstore/tx"
)

func main() {
	if err := Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// DefaultRPCAddr is the CometBFT RPC endpoint the client talks to.
const DefaultRPCAddr = "http://localhost:26657"

var errExit = errors.New("exit")

// Cli is the command-line client.
type Cli struct {
	rpcAddr string
	history string // path to the history file
	rpc     cmtclient.Client
}

// New builds a client. rpcAddr may be "", in which case it falls back to the
// DefaultRPCAddr or the MYSTORE_RPC_ADDR env var.
func New(rpcAddr string) (*Cli, error) {
	if rpcAddr == "" {
		rpcAddr = os.Getenv("MYSTORE_RPC_ADDR")
	}
	if rpcAddr == "" {
		rpcAddr = DefaultRPCAddr
	}

	rpc, err := rpchttp.New(rpcAddr, "/websocket")
	if err != nil {
		return nil, fmt.Errorf("creating rpc client: %w", err)
	}

	return &Cli{
		rpcAddr: rpcAddr,
		history: historyPath(),
		rpc:     rpc,
	}, nil
}

// Run is the entry point. With no arguments it starts an interactive REPL;
// with arguments it runs those commands once (e.g. "set k v", "get k",
// "health", "history").
func Run(args []string) error {
	c, err := New("")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return c.repl()
	}
	if err := c.handle(strings.Join(args, " ")); err != nil && err != errExit {
		return err
	}
	return nil
}

// ---- commands ----

func (c *Cli) handle(line string) error {
	c.record(line)

	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}

	switch fields[0] {
	case "exit", "quit":
		return errExit
	case "help":
		printHelp()
	case "history":
		return c.printHistory()
	case "health":
		if err := c.Health(); err != nil {
			return err
		}
		fmt.Println("ok")
	case "set":
		if len(fields) != 3 {
			return fmt.Errorf("usage: set <key> <value>")
		}
		return c.Set(fields[1], fields[2])
	case "commit":
		if len(fields) != 3 {
			return fmt.Errorf("usage: commit <key> <value>")
		}
		return c.Commit(fields[1], fields[2])
	case "reveal":
		if len(fields) != 3 {
			return fmt.Errorf("usage: reveal <key> <value>")
		}
		return c.Reveal(fields[1], fields[2])
	case "get":
		if len(fields) != 2 {
			return fmt.Errorf("usage: get <key>")
		}
		v, err := c.Get(fields[1])
		if err != nil {
			return err
		}
		fmt.Println(v)
	case "getcommit":
		if len(fields) != 2 {
			return fmt.Errorf("usage: getcommit <key>")
		}
		v, err := c.GetCommit(fields[1])
		if err != nil {
			return err
		}
		fmt.Println(v)
	case "wallet":
		if len(fields) > 2 {
			return fmt.Errorf("usage: wallet [seed|-]")
		}
		return c.Wallet(optArg(fields, 1))
	case "wallets":
		return c.Wallets()
	case "transfer":
		if len(fields) != 6 {
			return fmt.Errorf("usage: transfer <from-address> <to-address> <amount> <fee> <texp>")
		}
		return c.Transfer(fields[1], fields[2], fields[3], fields[4], fields[5])
	case "account":
		if len(fields) != 2 {
			return fmt.Errorf("usage: account <address>")
		}
		return c.Account(fields[1])
	case "pay_commit":
		if len(fields) != 6 {
			return fmt.Errorf("usage: pay_commit <from-address> <to-address> <amount> <fee> <texp>")
		}
		return c.PayCommit(fields[1], fields[2], fields[3], fields[4], fields[5])
	case "pay_reveal":
		if len(fields) != 2 {
			return fmt.Errorf("usage: pay_reveal <from-address>")
		}
		return c.PayReveal(fields[1])
	default:
		return fmt.Errorf("unknown command %q (try \"help\")", fields[0])
	}
	return nil
}

// optArg returns fields[i] when present, else "".
func optArg(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}

// Set is a convenience that commits sha256(value) and then reveals value.
func (c *Cli) Set(key, value string) error {
	if err := c.Commit(key, value); err != nil {
		return err
	}
	if err := c.Reveal(key, value); err != nil {
		return err
	}
	stored, err := c.Get(key)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	fmt.Printf("stored %q = %q\n", key, stored)
	return nil
}

// Commit publishes the commitment for a value.
func (c *Cli) Commit(key, value string) error {
	sum := sha256.Sum256([]byte(value))
	msg := &tx.CommitTx{Key: []byte(key), Hash: sum}
	if err := c.broadcast(types.Tx(msg.Marshal())); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Printf("%q: commitment = sha256(%q) = %s\n", key, value, hex.EncodeToString(sum[:]))
	return nil
}

// Reveal publishes the value and is verified against the stored commitment.
func (c *Cli) Reveal(key, value string) error {
	msg := &tx.RevealTx{Key: []byte(key), Value: []byte(value)}
	if err := c.broadcast(types.Tx(msg.Marshal())); err != nil {
		return fmt.Errorf("reveal: %w", err)
	}
	fmt.Printf("%q = %q (verified against commitment)\n", key, value)
	return nil
}

// Get returns the revealed value stored under key.
func (c *Cli) Get(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := c.rpc.ABCIQuery(ctx, "", cmtbytes.HexBytes([]byte(key)))
	if err != nil {
		return "", err
	}
	if res.Response.Log != "exists" {
		return "", fmt.Errorf("%s", res.Response.Log)
	}
	return string(res.Response.Value), nil
}

// GetCommit returns the stored commitment for key as a sha256 hex string.
func (c *Cli) GetCommit(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := c.rpc.ABCIQuery(ctx, "", cmtbytes.HexBytes([]byte("commit/"+key)))
	if err != nil {
		return "", err
	}
	if res.Response.Log != "exists" {
		return "", fmt.Errorf("%s", res.Response.Log)
	}
	return hex.EncodeToString(res.Response.Value), nil
}

// ---- signatureless payment scheme ----

// accountInfo is the decoded on-chain account state ("balance|seq|P-hex").
type accountInfo struct {
	Balance uint64
	Seq     uint64
	P       string
}

// queryRaw fetches a raw stored value, reporting whether it exists.
func (c *Cli) queryRaw(key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := c.rpc.ABCIQuery(ctx, "", cmtbytes.HexBytes([]byte(key)))
	if err != nil {
		return nil, false, err
	}
	if res.Response.Log != "exists" {
		return nil, false, nil
	}
	return res.Response.Value, true, nil
}

// getAccount loads an account's state, returning an error for a missing account.
func (c *Cli) getAccount(addr string) (*accountInfo, error) {
	val, ok, err := c.queryRaw("a/" + addr)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("account %q does not exist", addr)
	}
	parts := strings.Split(string(val), "|")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed account state %q", string(val))
	}
	var a accountInfo
	if a.Balance, err = strconv.ParseUint(parts[0], 10, 64); err != nil {
		return nil, fmt.Errorf("bad balance: %w", err)
	}
	if a.Seq, err = strconv.ParseUint(parts[1], 10, 64); err != nil {
		return nil, fmt.Errorf("bad seq: %w", err)
	}
	a.P = parts[2]
	return &a, nil
}

// parseUint parses a decimal uint64 argument.
func parseUint(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return v, nil
}

// parseHex32 decodes a 64-char hex string into a 32-byte field.
func parseHex32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("bad 32-byte hex %q", s)
	}
	copy(out[:], b)
	return out, nil
}

// ---- wallets ----

// wallet is a local, client-side wallet. The seed never leaves this file;
// spend-secret preimages are derived from it as R_k = sha256("R"+seed+itoa(k)).
// The public address is hex(sha256(R_0)) — the first hash of the chain. It is
// its own on-chain account key and seeds the account's auth hash P at first
// credit; the wallet holds the preimage.
type wallet struct {
	Seed    string         `json:"seed"`
	Step    uint64         `json:"step"` // k: R_step is the current spend secret
	Pending *pendingCommit `json:"pending,omitempty"`
}

// pendingCommit records the material of a pay_commit awaiting its reveal, so
// pay_reveal can rebuild the exact committed body.
type pendingCommit struct {
	To       string `json:"to"` // payee address as given
	Amount   uint64 `json:"amount"`
	Fee      uint64 `json:"fee"`
	TExpire  uint64 `json:"t_expire"`
	Seq      uint64 `json:"seq"`
	PNextHex string `json:"pnext_hex"`
	NonceHex string `json:"nonce_hex"`
}

// rAt derives the k-th spend-secret preimage.
func (w *wallet) rAt(k uint64) [32]byte {
	b := append(append([]byte("R"), []byte(w.Seed)...), []byte(strconv.FormatUint(k, 10))...)
	return sha256.Sum256(b)
}

// address returns the public receiving address: hex(P_0) where
// P_0 = sha256(R_0) — the first hash of the wallet's chain.
func (w *wallet) address() string {
	r0 := w.rAt(0)
	p0 := sha256.Sum256(r0[:])
	return hex.EncodeToString(p0[:])
}

func walletDir() string {
	if p := os.Getenv("MYSTORE_CLI_WALLETS"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".sealstore-cli", "wallets")
	}
	return ".sealstore-cli-wallets"
}

func walletPath(addr string) string { return filepath.Join(walletDir(), addr+".json") }

func loadWallet(addr string) (*wallet, error) {
	b, err := os.ReadFile(walletPath(addr))
	if err != nil {
		return nil, fmt.Errorf("wallet %q not found (create it with: wallet [seed|-])", addr)
	}
	var w wallet
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("wallet %q is corrupt: %w", addr, err)
	}
	return &w, nil
}

func saveWallet(w *wallet) error {
	if err := os.MkdirAll(walletDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(walletPath(w.address()), b, 0o600)
}

// Wallet creates a wallet from an optional seed (random when omitted or "-")
// and prints its public address. The wallet file is named by the address, so
// re-creating the same seed is refused as a duplicate.
func (c *Cli) Wallet(seed string) error {
	if seed == "-" {
		seed = ""
	}
	if seed == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return fmt.Errorf("generating seed: %w", err)
		}
		seed = hex.EncodeToString(secret)
	}
	w := &wallet{Seed: seed}
	if _, err := os.Stat(walletPath(w.address())); err == nil {
		return fmt.Errorf("wallet for address %s already exists", w.address())
	}
	if err := saveWallet(w); err != nil {
		return err
	}
	fmt.Printf("wallet created\naddress: %s\n", w.address())
	return nil
}

// Wallets lists the local wallets.
func (c *Cli) Wallets() error {
	entries, err := os.ReadDir(walletDir())
	if err != nil {
		fmt.Println("(no wallets yet)")
		return nil
	}
	any := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		w, err := loadWallet(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		status := ""
		if w.Pending != nil {
			status = " (pending commit)"
		}
		fmt.Printf("%s  step=%d%s\n", w.address(), w.Step, status)
		any = true
	}
	if !any {
		fmt.Println("(no wallets yet)")
	}
	return nil
}

// Transfer moves funds the only way the chain allows: a pay_commit followed
// immediately by its pay_reveal (they land in consecutive blocks, so the
// commitment is already on-chain before the spend secret becomes visible).
func (c *Cli) Transfer(fromAddr, toAddr, amountStr, feeStr, texpStr string) error {
	if err := c.PayCommit(fromAddr, toAddr, amountStr, feeStr, texpStr); err != nil {
		return fmt.Errorf("transfer: %w", err)
	}
	return c.PayReveal(fromAddr)
}

// Account prints an account's balance, seq, P and any active commit.
func (c *Cli) Account(addr string) error {
	a, err := c.getAccount(addr)
	if err != nil {
		return err
	}
	fmt.Printf("account %s: balance=%d seq=%d P=%s\n", addr, a.Balance, a.Seq, a.P)

	if val, ok, err := c.queryRaw("ac/" + addr); err != nil {
		return err
	} else if ok {
		fmt.Printf("active commit: %s\n", string(val))
	} else {
		fmt.Println("active commit: none")
	}
	return nil
}

// getHeight returns the chain's block-height clock (0 on a fresh chain).
func (c *Cli) getHeight() (uint64, error) {
	val, ok, err := c.queryRaw("hgt")
	if err != nil || !ok {
		return 0, err
	}
	h, err := strconv.ParseUint(string(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad height %q: %w", string(val), err)
	}
	return h, nil
}

// clearPendingCommit drops the wallet's pending commit when the chain no
// longer honours it: either no active commit exists (already revealed, or the
// commit never landed) or it has expired (the chain lazily overwrites expired
// commits, and pay_reveal can no longer execute it). Returns whether the
// pending state was dropped.
func (c *Cli) clearPendingCommit(w *wallet) (bool, error) {
	val, ok, err := c.queryRaw("ac/" + w.address())
	if err != nil {
		return false, err
	}
	if !ok {
		// No active commit on-chain: the pending entry is stale.
		w.Pending = nil
		return true, saveWallet(w)
	}
	parts := strings.Split(string(val), "|")
	if len(parts) != 3 {
		return false, fmt.Errorf("malformed active commit %q", string(val))
	}
	texp, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return false, fmt.Errorf("bad t_expire in active commit: %w", err)
	}
	h, err := c.getHeight()
	if err != nil {
		return false, err
	}
	if h > texp {
		w.Pending = nil
		return true, saveWallet(w)
	}
	return false, nil
}

// PayCommit publishes the commitment for a payment from the wallet at address
// `fromAddr`: P_next = Hash(R_{k+1}) and a fresh random nonce are derived
// locally, and the pending commit is stored in the wallet so pay_reveal can
// complete it.
func (c *Cli) PayCommit(fromAddr, toAddr, amountStr, feeStr, texpStr string) error {
	w, err := loadWallet(fromAddr)
	if err != nil {
		return err
	}
	if w.Pending != nil {
		// A pending commit only blocks while the chain still honours it; an
		// expired or consumed one is overridden by this new commit.
		dropped, err := c.clearPendingCommit(w)
		if err != nil {
			return err
		}
		if !dropped {
			return fmt.Errorf("wallet %s already has a pending commit (run: pay_reveal %s)", fromAddr, fromAddr)
		}
	}
	if _, err := tx.ParseAddress([]byte(toAddr)); err != nil {
		return fmt.Errorf("payee must be a 64 hex char address: %w", err)
	}
	amount, err := parseUint(amountStr)
	if err != nil {
		return err
	}
	fee, err := parseUint(feeStr)
	if err != nil {
		return err
	}
	texp, err := parseUint(texpStr)
	if err != nil {
		return err
	}
	a, err := c.getAccount(w.address())
	if err != nil {
		return err
	}
	body := tx.Payment{
		From:      []byte(w.address()),
		Seq:       a.Seq + 1,
		Transfers: []tx.Transfer{{To: []byte(toAddr), Amount: amount}},
		Fee:       fee,
	}
	rnext := w.rAt(w.Step + 1)
	pnext := sha256.Sum256(rnext[:])
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}
	cv := tx.CommitHash([]byte(tx.DefaultChainID), []byte(w.address()), body, pnext, nonce, texp)
	msg := &tx.PayCommitTx{Acct: []byte(w.address()), C: cv, TExpire: texp, Fee: fee}
	if err := c.broadcast(types.Tx(msg.Marshal())); err != nil {
		return fmt.Errorf("pay_commit: %w", err)
	}
	w.Pending = &pendingCommit{
		To:       toAddr,
		Amount:   amount,
		Fee:      fee,
		TExpire:  texp,
		Seq:      body.Seq,
		PNextHex: hex.EncodeToString(pnext[:]),
		NonceHex: hex.EncodeToString(nonce[:]),
	}
	if err := saveWallet(w); err != nil {
		return fmt.Errorf("saving pending commit: %w", err)
	}
	fmt.Printf("committed payment for %s: C=%s (seq=%d, fee=%d, t_expire=%d)\n",
		w.address(), hex.EncodeToString(cv[:]), body.Seq, fee, texp)
	return nil
}

// PayReveal reveals the wallet's current spend secret R_k and executes the
// pending committed payment. On success the wallet advances to step k+1 (the
// revealed secret is burned by the on-chain P rotation).
func (c *Cli) PayReveal(fromAddr string) error {
	w, err := loadWallet(fromAddr)
	if err != nil {
		return err
	}
	p := w.Pending
	if p == nil {
		return fmt.Errorf("wallet %s has no pending commit", fromAddr)
	}
	if _, err := tx.ParseAddress([]byte(p.To)); err != nil {
		return fmt.Errorf("stored payee address invalid: %w", err)
	}
	pnext, err := parseHex32(p.PNextHex)
	if err != nil {
		return err
	}
	nonce, err := parseHex32(p.NonceHex)
	if err != nil {
		return err
	}
	body := tx.Payment{
		From:      []byte(w.address()),
		Seq:       p.Seq,
		Transfers: []tx.Transfer{{To: []byte(p.To), Amount: p.Amount}},
		Fee:       p.Fee,
	}
	msg := &tx.PayRevealTx{Body: body, PNext: pnext, N: nonce, TExpire: p.TExpire, RCurrent: w.rAt(w.Step)}
	if berr := c.broadcast(types.Tx(msg.Marshal())); berr != nil {
		// The reveal may have landed before the error surfaced (e.g. a client
		// timeout after commit): if the account's seq has advanced to the
		// pending seq, treat it as executed.
		if a, aerr := c.getAccount(w.address()); aerr != nil || a.Seq < p.Seq {
			return fmt.Errorf("pay_reveal: %w", berr)
		}
	}
	w.Step++
	w.Pending = nil
	if err := saveWallet(w); err != nil {
		return fmt.Errorf("advancing wallet: %w", err)
	}
	fmt.Printf("payment executed: %d to %s; wallet %s advanced to step %d\n", p.Amount, p.To, fromAddr, w.Step)
	return nil
}

// Health checks that the node RPC is reachable.
func (c *Cli) Health() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := c.rpc.Health(ctx)
	return err
}

// broadcast submits a tx to broadcast_tx_commit and reports whether the node
// accepted it (both CheckTx and the finalize/exec result must succeed).
func (c *Cli) broadcast(tx types.Tx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := c.rpc.BroadcastTxCommit(ctx, tx)
	if err != nil {
		return err
	}
	if res.CheckTx.Code != 0 {
		return fmt.Errorf("check_tx rejected (code %d): %s", res.CheckTx.Code, res.CheckTx.Log)
	}
	if res.TxResult.Code != 0 {
		return fmt.Errorf("tx rejected (code %d): %s", res.TxResult.Code, res.TxResult.Log)
	}
	return nil
}

// ---- REPL ----

// repl starts the interactive loop. When stdin/stdout are real terminals it
// uses readline (up/down arrow history recall); otherwise it falls back to a
// plain line-by-line reader (e.g. when input is piped).
func (c *Cli) repl() error {
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return c.readlineRepl()
	}
	return c.plainRepl()
}

// readlineRepl offers a line editor with persistent history (up-arrow recall).
func (c *Cli) readlineRepl() error {
	rl, err := readline.NewEx(&readline.Config{Prompt: "> "})
	if err != nil {
		return err
	}
	defer rl.Close()

	// Seed the editor's in-memory history from the history file.
	for _, l := range c.loadHistory() {
		rl.SaveHistory(l)
	}

	fmt.Printf("sealstore cli (rpc: %s). Commands: set, commit, reveal, get, getcommit, wallet, wallets, transfer, account, pay_commit, pay_reveal, health, history, help, exit.\n", c.rpcAddr)
	for {
		line, err := rl.Readline()
		if err == io.EOF {
			fmt.Println()
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rl.SaveHistory(line) // up-arrow recall within this session
		if err := c.handle(line); err != nil {
			if err == errExit {
				return nil
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

// plainRepl reads commands line-by-line without editing (used when input is
// piped, e.g. in tests or scripts).
func (c *Cli) plainRepl() error {
	fmt.Printf("sealstore cli (rpc: %s). Commands: set, commit, reveal, get, getcommit, wallet, wallets, transfer, account, pay_commit, pay_reveal, health, history, help, exit.\n", c.rpcAddr)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			fmt.Println()
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := c.handle(line); err != nil {
			if err == errExit {
				return nil
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

// isTerminal reports whether f is an interactive device (char device).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ---- history ----

func historyPath() string {
	if p := os.Getenv("MYSTORE_CLI_HISTORY"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".sealstore-cli", "history")
	}
	return ".sealstore-cli-history"
}

// record appends a command line to the history file.
func (c *Cli) record(line string) {
	f, err := os.OpenFile(c.history, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// loadHistory returns the previously recorded command lines.
func (c *Cli) loadHistory() []string {
	b, err := os.ReadFile(c.history)
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// printHistory shows previously recorded commands, numbered.
func (c *Cli) printHistory() error {
	lines := c.loadHistory()
	if len(lines) == 0 {
		fmt.Println("(no history yet)")
		return nil
	}
	for i, line := range lines {
		fmt.Printf("%4d  %s\n", i+1, line)
	}
	return nil
}

func printHelp() {
	fmt.Println("commands:")
	fmt.Println("  set <key> <value>     commit then reveal")
	fmt.Println("  commit <key> <value>  publish sha256(value) as the commitment")
	fmt.Println("  reveal <key> <value>  reveal value (verified against the commit)")
	fmt.Println("  get <key>             read the revealed value")
	fmt.Println("  getcommit <key>       read the stored commitment (sha256 hex)")
	fmt.Println("  wallet [seed|-]        create a wallet (random seed when omitted),")
	fmt.Println("                        print its address (64 hex chars)")
	fmt.Println("  wallets               list local wallets")
	fmt.Println("  transfer <from-address> <to-address> <amount> <fee> <texp>")
	fmt.Println("                        commit + reveal in one shot — the only")
	fmt.Println("                        way funds move")
	fmt.Println("  account <address>     show balance, seq, P and any active commit")
	fmt.Println("  pay_commit <from-address> <to-address> <amount> <fee> <texp>")
	fmt.Println("                        phase 1: commit the payment (secrets derived")
	fmt.Println("                        from the wallet, pending state saved locally)")
	fmt.Println("  pay_reveal <from-address>")
	fmt.Println("                        phase 2: reveal the spend secret and execute")
	fmt.Println("  health                check the node RPC is responsive")
	fmt.Println("  history               show previous commands")
	fmt.Println("  help                  this help")
	fmt.Println("  exit                  leave the REPL")
}
