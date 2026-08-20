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
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	default:
		return fmt.Errorf("unknown command %q (try \"help\")", fields[0])
	}
	return nil
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

	fmt.Printf("sealstore cli (rpc: %s). Commands: set, commit, reveal, get, getcommit, health, history, help, exit.\n", c.rpcAddr)
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
	fmt.Printf("sealstore cli (rpc: %s). Commands: set, commit, reveal, get, getcommit, health, history, help, exit.\n", c.rpcAddr)
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
	fmt.Println("  health                check the node RPC is responsive")
	fmt.Println("  history               show previous commands")
	fmt.Println("  help                  this help")
	fmt.Println("  exit                  leave the REPL")
}
