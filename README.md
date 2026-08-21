# 🔐 SealStore — a commit‑reveal key/value chain (CometBFT)

A sovereign CometBFT blockchain that stores key/value data in a **commit‑reveal**
scheme: nothing is revealed until its hash has been committed first.

## 🤔 What problem it solves

Shor's algorithm breaks ECDSA: every signed payment exposes the public key, and
every exposed key is a future forged spend. SealStore removes the signature
**completely** — a spend is authorised by a hash preimage (`Hash(R_current) =
P`), so only sha256 stands between an attacker and the funds, and quantum
computers merely halve hash strength (Grover), they don't break it. Revealed
secrets burn on use (`P` rotates): nothing reusable is ever exposed.

The same seal/open primitive also keeps values out of the mempool — for
sealed‑bid auctions, private voting, or unbiasable randomness.

## ⚙️ How it works

Payments are **signatureless**: an account authorises a spend by revealing a
hash preimage, not by signing. Accounts hold `{balance, seq, P}`, where `P` is
the current auth hash (`Hash(R_current)`); each spend rotates `P`, so the
revealed secret is burned the moment it becomes public.

The leading byte is the type tag, byte strings are uvarint-length-prefixed, and
hashes are raw 32-byte arrays.

- **pay_commit** — reserve a spend without revealing the secret: deduct the
  fee and record `C`; at most one active commit per account (an expired one
  is overridden)
- **pay_reveal** — authorise and execute: check `Hash(R_current) = P`, apply
  the transfers, rotate `P := P_next`, consume the commitment

Two phases, because once `R_current` is visible a miner could substitute a
different payment — the commitment `C` binds the payment body and rotation
target before the secret is out
([full spec](docs/signatureless-commit-reveal.md)).

The same commit–reveal primitive also seals plain key/value data
(`0x01 CommitTx` / `0x02 RevealTx`: publish `sha256(value)`, reveal later
against the stored seal).

### 🆚 vs ECDSA-signature payments

| | Signatureless (this chain) | ECDSA payment (typical) |
|---|---|---|
| **Byte footprint** | ~336 B in 2 txs (102 + 234); auth is four 32-B hashes | ~110 B in 1 tx — Bitcoin P2WPKH (71 B sig + 33 B pubkey) or Ethereum (65 B r‖s‖v) |
| **Computation** | 3 sha256 (~1 µs) — no curve math; the client only derives and hashes | 1 ECDSA verify (double scalar multiplication, ~50–100 µs); the client signs |
| **Blockchain ops** | 2 txs; active-commit state + height clock for expiry; fee at commit; one pending spend per account; settles in ≥ 2 blocks | 1 tx; two balance writes; settles in 1 confirmation |

- **You pay:** ~3× the bytes and a second block inclusion; pending spends
  serialize per account
- **You gain:** ~50–100× cheaper verification (every node verifies every tx —
  hashes, not curve math), only stdlib `sha256`, and no Shor exposure
- **Burned on use:** a revealed secret can never authorise anything again —
  `P` has rotated and the commitment is consumed
- ~192 of the 336 bytes are addresses stored as hex ASCII; raw 32-byte
  addresses would bring the total to ~240 B

## 📦 Requirements

- Go 1.25+
- `cometbft` (pure Go): `go install github.com/cometbft/cometbft/cmd/cometbft@v0.40.0`

## 🚀 Build & run (pure Go, no Docker)

```bash
# 1. build the ABCI app + CLI
go build -o sealstore ./cmd/sealstore && go build -o sealstore-cli ./cmd/sealstore-cli

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
