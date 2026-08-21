# Security

Known properties, limitations, and open issues in the signatureless commit–reveal payment scheme.

## Why the substitution attack does not land

A malicious miner who sees `R_current` at reveal time cannot substitute their own `RevealTx`:

1. **Exact match against the pre-recorded commit.** On reveal, `C` is recomputed from the reveal's fields and must match the commitment stored on-chain at commit time, byte-for-byte. Since `pay_hash = Hash(PaymentBody)` is inside `C`, the body, `P_next`, `N`, and `t_expire` are frozen when the commit lands; SHA-256 preimage resistance binds them.
2. **One active commit per account.** A new commit is rejected while a non-expired one exists, so a replacement cannot be written in the only window that matters — while the original is pending and `R_current` is still valid.

Substitution would need *both* a free slot and a leaked secret, which cannot co-occur in the honest flow: `R_current` is only revealed inside a `RevealTx`, which requires a stored commit and immediately rotates `P`.

## Open issue 1 — Commitment does not bind the account's current secret state

`C` binds the static address `A` but not the account's current `P` (nor `seq`). Theft-prevention rests on operational rules (single slot, commit-before-reveal); if those change, the same `C` could be replayed against a different state.

**Fix:** fold `P_cur` (the account's `P` at commit) into the commitment:

```
C = Hash("SIGLESS_COMMIT_V1" || chain_id || A || P_cur || pay_hash || P_next || N || t_expire)
```

This makes the binding provably state-specific.

## Issue 2 — CommitTx is unauthenticated (slot-hostage DoS)

Commit validation covers only address validity, balance ≥ fee, and slot availability — nothing ties the committer to the account. Anyone can file a garbage commit (far-future `t_expire`) against a victim account, rejecting legitimate commits until expiry and burning any fee already paid.

Fee-priced by design; documented, not fixed. Hardening candidates: refund the fee on expiry and bound `t_expire`; or require a zero-knowledge proof of the preimage of `P_cur`.

## Issue 3 — past `t_expire` accepted at commit

Commit processing stores a commit whose `t_expire ≤ current height`: fee burned, slot occupied, reveal impossible. A backdated commit holds a hostage slot.

**Fix:** reject `t_expire <= current height` at commit.

## Accepted limitations

- **Master-seed compromise is out of scope** — `S` must be protected off-chain.
- **Commit fee is non-refundable** on forfeit (by design; doubles as the DoS gate).