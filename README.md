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

# 2. create wallets and copy their addresses (balances start at genesis)
./sealstore-cli wallet my-secret
# → address: 9f86d08fb044…

# 3. init the node (do init once), then pre-credit accounts in the genesis
cometbft init --home /tmp/cometbft-home
# edit /tmp/cometbft-home/config/genesis.json and add an app_state:
#   "app_state": {
#     "accounts": [
#       {"address": "<64 hex from step 2>", "balance": 1000000}
#     ]
#   }
# Each account is created keyed by its address with P seeded from the address
# itself — spendable by exactly the wallet that printed it.

# 4. start the app + node
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

## 💸 Signatureless payments (accounts)

A second scheme (see [`docs/signatureless-commit-reveal.md`](docs/signatureless-commit-reveal.md))
authorises payments by **hash preimage only** — no signatures. Accounts hold
`{balance, seq, P}`; spending rotates `P` and burns the revealed secret.

**The address is the first hash.** A public address is exactly 64 hex chars —
`Hash(R_0)`, the first hash of the wallet's secret chain. The address *is* the
account id. Accounts come into existence in two ways: pre-credited in the
genesis `app_state`, or lazily when they receive their first payment — either
way the account is keyed by the address with auth hash `P` seeded from the
address bytes (so `P` equals the address until the first spend rotates it).
Anything that isn't a 64-hex address is rejected — the chain never invents a
hash, since funds sent to a hash whose preimage nobody knows would be locked
forever.

**Funds move one way only:** `pay_commit` + `pay_reveal`. The commitment `C`
binds the payment body, the next auth hash `P_next`, a nonce and the expiry
*before* the secret is revealed, so a miner cannot substitute a different
payment. The wallet derives `P_next = Hash(R_{k+1})` and a random nonce at
commit time and stores them as pending; the reveal burns `R_k` and advances
the wallet.

**Wallets** store the preimages locally (`~/.sealstore-cli/wallets/`, one file
per address); spend secrets are derived as `R_k = sha256("R" ‖ seed ‖ k)` and
never leave your machine. Commands identify a wallet by its full address:

```bash
./sealstore-cli wallet -                  # create a wallet (random seed)
# → address: 9f86d08fb044…                # put this in genesis to fund it
./sealstore-cli wallets                   # list wallets (address, step)
```

**Transfer** is the one-shot convenience: it commits and immediately reveals
(they land in consecutive blocks, so the commitment is already on-chain
before the spend secret becomes visible):

```bash
./sealstore-cli transfer <from-address> <to-address> 10 1 1000000
# → amount 10, fee 1, t_expire 1000000
./sealstore-cli account <to-address>      # balance=10 seq=0 P=<its address>
./sealstore-cli account <from-address>    # balance, seq=1, P=<rotated>
```

Or drive the two phases by hand:

```bash
# phase 1: commit (fee deducted; one active commit per account)
./sealstore-cli pay_commit <from-address> <to-address> 10 1 1000000

# phase 2: reveal the spend secret and execute the payment
./sealstore-cli pay_reveal <from-address>
```

A commit that expires (no reveal before `t_expire`) is dead: its fee stays
burned and the next `pay_commit` overrides it — the CLI clears the local
pending state automatically once the chain no longer honours it.

Replaying the reveal, reusing the old secret, or substituting the payee all
fail — the commitment is consumed and `P` has rotated.

Replaying the reveal, reusing the old secret, or substituting the payee all
fail — the commitment is consumed and `P` has rotated.

## ✅ Tests

```bash
go test ./...
```
