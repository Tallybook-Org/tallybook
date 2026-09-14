# Tallybook

Tallybook is settlement bookkeeping for services that charge per HTTP
request — APIs paid by AI agents and other machines over Stellar, using
the [x402](https://www.x402.org/) protocol, MPP charge mode, or MPP
payment channels.

The contracts Tallybook settles against are built and deployed in a
separate repository. This repository is everything off-chain: a metering
collector, a settlement daemon, and a chain indexer, all backed by
Postgres.

## Why this exists

In MPP session mode, a payer deposits into a one-way payment channel and
signs cumulative off-chain commitments as they consume an API. That
revenue is a signature in the recipient's own database until it's
submitted on chain — and once the channel's refund waiting period
elapses, the payer can reclaim **the entire remaining balance**,
including anything the recipient earned but never settled for. The
contract reserves nothing for the recipient.

That means a lost commitment is lost revenue, and a missed settlement
deadline is lost revenue. The settler (`internal/settle`) is the
component that decides whether the operator actually gets paid, and its
design favors durability over throughput, and settling early over
settling optimally, throughout.

## Architecture

Three services, sharing one Postgres database:

- **Collector** — an HTTP metering layer. Wraps a paid API, records every
  request, prices it against the operator's published schedule, and holds
  custody of payment-channel commitments (`internal/httpapi`,
  `internal/meter`, `internal/custody`).
- **Settler** — a daemon that watches open payment channels and sweeps
  earned revenue on chain before the payer can reclaim it
  (`internal/settle`).
- **Indexer** — ingests Soroban events into Postgres so metered usage can
  be reconciled against what actually settled (`internal/indexer`).

Each owns its own slice of the schema and talks to Stellar through
`internal/stellar`, a thin Soroban JSON-RPC client and set of typed
contract bindings — there is no official Go SDK for Soroban RPC, so this
client is hand-rolled and verified against the live RPC surface rather
than assumed.

```
internal/
├── config/     # env loading, validation, secret redaction
├── stellar/    # Soroban RPC client, XDR helpers, contract bindings
├── merkle/     # usage-root tree builder, matching the contract byte for byte
├── meter/      # price schedules, request pricing, period lifecycle, anchoring
├── custody/    # commitment verification and persist-before-acknowledge storage
├── settle/     # exposure calculation, sweep policy, submission, retry, metrics
├── indexer/    # event ingestion, channel state tracking, reconciliation
├── store/      # migration runner and numbered .sql migrations
└── httpapi/    # the collector's metering middleware
```

## The on-chain contracts

Tallybook consumes three Soroban contracts; it does not modify or vendor
any of them.

| Contract | Role |
|---|---|
| `price_book` | Operators publish versioned price schedules here. |
| `statement_registry` | Billing periods are anchored here as merkle-rooted statements, with on-chain dispute/resolution. |
| [`one-way-channel`](https://github.com/stellar-experimental/one-way-channel) | **External and unaudited.** MPP session payment channels: cumulative signed commitments, settled or closed on chain, with a funder-reclaim path after a waiting period. |

Tallybook is not a payment facilitator, router, or client-side `pay()`
SDK, and it takes no protocol fee — see `CLAUDE.md` for the full
specification this codebase was built against, including every
on-chain interface in detail.

## Status

Every service listed above is implemented as a tested library
(`internal/...`), including real-Postgres integration tests and, where
practical, replayed real testnet RPC exchanges rather than synthetic
ones. `cmd/collector`, `cmd/settler`, and `cmd/indexer` — the thin
`main.go` wiring that turns these libraries into three running
binaries — have not been built yet; see `internal/settle`'s `Daemon`,
`internal/indexer`'s `Ingestor`, and `internal/httpapi`'s `Middleware`
for what each entrypoint needs to construct and run.

## Getting started

Prerequisites: Go (see `go.mod` for the minimum version), Docker, and a
Soroban RPC endpoint (testnet works out of the box).

```sh
git clone https://github.com/Tallybook-Org/tallybook.git
cd tallybook
make up              # starts Postgres via docker-compose, on localhost:5433
cp .env.example .env # fill in TB_PRICE_BOOK_ID, TB_STATEMENT_REGISTRY_ID,
                      # TB_OPERATOR_ADDRESS, and an operator signing key
make check           # gofmt, go vet, staticcheck, and the full test suite
```

`make check` runs the same steps CI does, in the same order — see
`.github/workflows/ci.yml` and `Makefile`. The test suite includes
integration tests against a real Postgres instance (not mocks), so
`make up` needs to have succeeded first.

Every environment variable is documented in `.env.example` and validated
at startup by `internal/config` — a missing or malformed one fails fast
with a clear message naming it, rather than starting with a zero value.

## Documentation

- [Operator runbook](docs/operator-runbook.md) — operational notes for
  deploying and running the on-chain and off-chain pieces, including
  known hazards worth knowing before you hit them yourself.
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- `CLAUDE.md` — the full specification this codebase implements.

## License

[Apache License 2.0](LICENSE).
