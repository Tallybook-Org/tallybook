# Security Policy

Tallybook holds custody of payment-channel commitments and moves money
on chain on an operator's behalf. Treat anything that looks like it
could lead to lost funds, a forged commitment, an unauthorized
settlement, or a leaked signing key as a security issue, even if you're
not sure — err toward reporting it privately.

## Audit status

**This repository has not been audited**, and it depends on contracts
this repository does not own or control:

`price_book` and `statement_registry` are built and deployed by
[tallybook-contracts](https://github.com/Tallybook-Org/tallybook-contracts) —
see that repository's own `SECURITY.md` for their audit status.

**Tallybook's MPP payment channel mode depends on
[`stellar-experimental/one-way-channel`](https://github.com/stellar-experimental/one-way-channel),
and that contract is unaudited.** This repository does not fork, vendor,
or reimplement any part of it — `internal/stellar/channel.go` only calls
it — but anything built on Tallybook's MPP session mode inherits
`one-way-channel`'s unaudited status along with whatever this repository's
own audit status ends up being.

Do not deploy this system, or any system built on top of it, to hold or
move real funds without an independent security audit first.

## Reporting a vulnerability

**Please do not open a public GitHub issue for a security problem.**

Report privately via
[GitHub Security Advisories](https://github.com/Tallybook-Org/tallybook/security/advisories/new)
for this repository. Include:

- What you found and where (file/function, or the on-chain interaction
  it affects).
- The impact as you understand it — what an attacker could actually do.
- Steps to reproduce, or a proof of concept, if you have one.
- Whether it's specific to this repository or also affects the
  contracts it calls (`price_book`, `statement_registry`,
  `one-way-channel`) — if it's a contract-level issue, say so
  explicitly, since this repository doesn't control or patch those.

We'll acknowledge a report as soon as we can and follow up with an
assessment once we've had a chance to look. Please give us a reasonable
window to fix a confirmed issue before any public disclosure.

## Scope

In scope:

- `internal/custody` — commitment verification and storage.
- `internal/settle` — the settler: sweep decisions, submission,
  retries.
- `internal/stellar` — the Soroban RPC client, XDR encoding, and
  contract bindings.
- `internal/config` — secret handling and redaction.
- Anything else in this repository that touches money, signing keys, or
  an on-chain call.

Out of scope, but worth reporting anyway so it reaches the right people:

- The on-chain contracts themselves (`price_book`, `statement_registry`
  are a separate repository; `one-way-channel` is
  [external and unaudited](https://github.com/stellar-experimental/one-way-channel) —
  Tallybook consumes it as-is and does not control its code).
- The Stellar network, Soroban RPC infrastructure, or any third-party
  facilitator (e.g. the x402 facilitator) Tallybook happens to
  interoperate with.

## What "in scope" has meant in practice here

A non-exhaustive list of the kind of thing that matters, drawn from
this codebase's own design decisions:

- A commitment's signature must verify against the channel's real,
  independently-sourced `commitment_key` — never a value read off the
  commitment or the request itself (see
  `internal/custody.VerifyCommitment`'s own doc comment for why).
- The monotonic invariant on `commitments.cumulative_amount` is enforced
  by a database trigger, not application code, specifically so a bug in
  Go can't silently regress it.
- Every settle attempt is idempotent, keyed by `(channel,
  cumulative_amount)`, and durably recorded *before* it's submitted —
  not after — so a crash can't produce a silent double-settle or a
  silent loss.
- `TB_OPERATOR_SECRET` (and `TB_OPERATOR_SECRET_PATH`) never appear in
  logs, error messages, metrics labels, or panics — enforced by a
  redacting `Secret` type and a test asserting it.

A change that weakens any of the above, even incidentally, is a security
concern regardless of whether it was the point of the change.

## Supported versions

This project does not yet have tagged releases; security fixes land on
`main`. Once versioned releases exist, this section will name which
ones receive fixes.
