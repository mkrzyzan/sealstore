# 🔐 SealStore — a commit‑reveal key/value chain (CometBFT)

A sovereign CometBFT blockchain that stores key/value data in a **commit‑reveal**
scheme: nothing is revealed until its hash has been committed first.

## 🤔 What problem it solves

Public ledgers leak: values sit in the mempool before they land on-chain, so the
last actor can always front‑run or outbid the first. SealStore flips that — you
**seal** a value by publishing `sha256(value)`, and **open** it later by revealing
the value, which is verified against the seal. Great for sealed‑bid auctions,
private voting, or unbiasable randomness.

## ⚙️ How it works

Two transaction types, sent as **pure binary** structures marshaled with Go's
`encoding/binary` package (defined in [`tx/`](tx/)):

```
CommitTx:  0x01 | uvarint(len(key)) | key | 32 raw sha256(value) bytes
RevealTx:  0x02 | uvarint(len(key)) | key | uvarint(len(value)) | value
```

The leading byte is the type tag, multi-byte lengths are uvarints, and the commit
hash is a raw 32-byte array (so the format is byte-order independent).

- **commit** — store the commitment (kept afterward for verification)
- **reveal** — accepted only if `sha256(value)` matches the seal

Anything else — or a reveal with no/mismatched commit — is rejected (ABCI code 1).

> ⚠️ **Breaking change:** the wire format is now binary. Transactions serialized
> in the old `commit=<key>=<hash>` / `reveal=<key>=<value>` string format are
> rejected.

## 📦 Requirements

- Go 1.25+
- `cometbft` (pure Go): `go install github.com/cometbft/cometbft/cmd/cometbft@v0.40.0`

## 🚀 Build & run (pure Go, no Docker)

```bash
# 1. build the ABCI app + CLI
go build -o sealstore . && go build -o sealstore-cli ./cli

# 2. init & start the node (do init once)
cometbft init --home /tmp/cometbft-home
./sealstore -kv-home $HOME/.kvstore
cometbft node --home /tmp/cometbft-home --proxy_app=unix:///tmp/example.sock
```

The node RPC is then at `localhost:26657`.

## 💻 Use via the CLI

**One user (quick path):**

```bash
./sealstore-cli health
./sealstore-cli set city paris          # commit + reveal in one
./sealstore-cli get city                # → paris
./sealstore-cli getcommit city          # → the stored seal (sha256 hex)
```

**Two users — see the secrecy in action (e.g. a sealed bid):**

Alice seals a bid (only the hash is published — the value stays hidden):

```bash
./sealstore-cli commit bid1 100         # Alice: seal sha256("100")
# → "bid1": commitment = sha256("100") = <hex>
```

Bob, watching the chain, can see the seal but *not* the value:

```bash
./sealstore-cli getcommit bid1          # Bob: sees only the seal → <hex>
./sealstore-cli get bid1                # Bob: "key does not exist" (value hidden)
```

Alice opens the seal by revealing the value (verified against it):

```bash
./sealstore-cli reveal bid1 100         # Alice: opens it
./sealstore-cli get bid1                # Bob, now: → 100
```

Bob can independently verify: `sha256("100")` must equal the seal he saw earlier
(`getcommit bid1` is unchanged — the seal is kept for verification).

## ✅ Tests

```bash
go test ./...
```
