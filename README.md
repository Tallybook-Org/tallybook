# Tallybook

Tallybook is settlement bookkeeping for services that charge per HTTP
request — APIs paid by AI agents and other machines over Stellar, using
the [x402](https://www.x402.org/) protocol, MPP charge mode, or MPP
payment channels.

## Relationship to tallybook-contracts

The Soroban contracts Tallybook settles against — `price_book`,
`statement_registry` — are built, deployed, and owned by a separate
repository, `tallybook-contracts`. This repository consumes their
deployed, immutable interfaces (contract IDs supplied via
`TB_PRICE_BOOK_ID` / `TB_STATEMENT_REGISTRY_ID`) and restates none of
their logic: no contract code is vendored or forked here, and if an
interface here looks wrong, the fix belongs in that repository, not this
one. Tallybook also consumes a third, external contract it does not own
at all — see below.

This repository is everything off-chain: a metering collector, a
settlement daemon, and a chain indexer, all backed by Postgres.

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

- **Collector** (`cmd/collector`) — an HTTP metering layer. Wraps a paid
  API, records every request, prices it against the operator's published
  schedule, and holds custody of payment-channel commitments
  (`internal/httpapi`, `internal/meter`, `internal/custody`).
- **Settler** (`cmd/settler`) — a daemon that watches open payment
  channels and sweeps earned revenue on chain before the payer can
  reclaim it (`internal/settle`).
- **Indexer** (`cmd/indexer`) — ingests Soroban events into Postgres so
  metered usage can be reconciled against what actually settled
  (`internal/indexer`).

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
cmd/
├── collector/  # HTTP metering layer entrypoint
├── settler/    # settlement daemon entrypoint
└── indexer/    # chain indexer entrypoint
```

## The on-chain contracts

Tallybook consumes three Soroban contracts; it does not modify, fork, or
vendor any of them.

| Contract | Owner | Role |
|---|---|---|
| `price_book` | tallybook-contracts | Operators publish versioned price schedules here. |
| `statement_registry` | tallybook-contracts | Billing periods are anchored here as merkle-rooted statements, with on-chain dispute/resolution. |
| [`one-way-channel`](https://github.com/stellar-experimental/one-way-channel) | stellar-experimental (third party) | **External and unaudited.** MPP session payment channels: cumulative signed commitments, settled or closed on chain, with a funder-reclaim path after a waiting period. |

Tallybook is not a payment facilitator, router, or client-side `pay()`
SDK, and it takes no protocol fee — see `CLAUDE.md` for the full
specification this codebase was built against, including every on-chain
interface in detail.

## Getting started

Prerequisites: Go (see `go.mod` for the minimum version), Docker, and a
Soroban RPC endpoint (testnet works out of the box).

```sh
git clone https://github.com/Tallybook-Org/tallybook.git
cd tallybook
cp .env.example .env  # fill in TB_PRICE_BOOK_ID, TB_STATEMENT_REGISTRY_ID,
                       # TB_OPERATOR_ADDRESS, and an operator signing key
make up                # Postgres only, on localhost:5433 — what the test suite needs
make check              # gofmt, go vet, staticcheck, and the full test suite
```

`make check` runs the same steps CI does, in the same order — see
`.github/workflows/ci.yml`. The test suite includes integration tests
against a real Postgres instance (not mocks, §8), so `make up` needs to
have succeeded first.

### Running all three services

`docker-compose.yml` defines all four containers — Postgres plus
collector, settler, and indexer — so the whole stack comes up with one
command once `.env` is filled in:

```sh
make up-all    # docker compose up -d --build: postgres + all three services
make down-all  # docker compose down
```

Each service binary fails fast at startup, naming whichever required
`TB_*` variable is still missing (§7 of `CLAUDE.md`; `internal/config`),
so an incomplete `.env` shows up immediately in `docker compose logs`
rather than as a silent misconfiguration. `make up` (Postgres alone,
without `--build`) is what local development and the test suite use day
to day; `make up-all` is for running the real binaries.

Every environment variable is documented in `.env.example` and validated
at startup — a missing or malformed one fails fast with a clear message
naming it.

| Variable | Example | Notes |
|---|---|---|
| `TB_DATABASE_URL` | `postgres://...` | Required |
| `TB_STELLAR_RPC_URL` | `https://soroban-testnet.stellar.org` | Required |
| `TB_NETWORK_PASSPHRASE` | `Test SDF Network ; September 2015` | Required |
| `TB_PRICE_BOOK_ID` | `CB2IEP...` | From tallybook-contracts' README |
| `TB_STATEMENT_REGISTRY_ID` | `CB75TT...` | From tallybook-contracts' README |
| `TB_OPERATOR_ADDRESS` | `G...` | Required |
| `TB_OPERATOR_SECRET_SOURCE` | `env` \| `file` | How the signing key is obtained |
| `TB_OPERATOR_SECRET` | `S...` | Only when source is `env`. Never logged, never committed |
| `TB_OPERATOR_SECRET_PATH` | `/run/secrets/operator.key` | Only when source is `file` |
| `TB_SAFETY_MARGIN_LEDGERS` | `1440` | Settler deadline margin |
| `TB_MAX_EXPOSURE` | `10000000` | Stroops; triggers a sweep |
| `TB_MAX_EXPOSURE_AGE` | `24h` | Duration |
| `TB_PERIOD_DURATION` | `720h` | How long a billing period may stay open before it's closed on calendar grounds alone, independent of any price change |
| `TB_SETTLER_TICK` | `30s` | Settler poll interval |
| `TB_INDEXER_START_LEDGER` | `4590000` | Where ingestion begins |
| `TB_COLLECTOR_ADDR` | `:8080` | Collector listen address |
| `TB_LOG_LEVEL` | `info` | |

Secrets never appear in logs, error messages, metrics labels, or commits
— enforced by a redacting `Secret` type in `internal/config` and a test
asserting it.

## Known limitations

- **Channel discovery is filtered by topic, network-wide.**
  `one-way-channel` is deployed once per channel (its own factory
  contract), and nothing in `CLAUDE.md`'s env vars names that factory's
  address — so `internal/indexer` can't scope its `getEvents` filter to
  "channels this factory created" the way it scopes `price_book` and
  `statement_registry` events to their own known contract IDs. Channel
  lifecycle events are filtered by topic only, across the whole network,
  which is the only way to discover a channel never seen before. This
  is narrowed one step: a discovered channel is only ever tracked if its
  recipient matches `TB_OPERATOR_ADDRESS` (`internal/indexer/channels.go`),
  so a channel paying out to some other operator is discarded — but the
  underlying topic-only exposure remains, and an unrelated contract
  emitting the same bare-symbol topic shape would still be evaluated (and
  then discarded, once its recipient doesn't match). See
  `internal/indexer/ingest.go`'s package doc comment for the full
  reasoning.
- **The settler doesn't estimate a wall-clock refund deadline.**
  `refund_deadline_ledger` is a ledger number; converting it to a
  wall-clock countdown for retry pacing would need verified,
  live ledger-close-time data this codebase doesn't have — `CLAUDE.md`
  itself warns against trusting that arithmetic unverified (§6). Instead,
  `internal/settle.Daemon` re-evaluates deadline pressure fresh every
  tick and tightens its own polling interval under pressure — see
  `internal/settle/daemon.go`.
- **`cmd/collector` mounts no protected route.** Tallybook meters "a
  paid API" generically; there is no config naming a specific upstream to
  reverse-proxy. The collector binary starts up, migrates the schema, and
  exposes what an operator's own route registration needs
  (`httpapi.Middleware` and its dependencies) — wiring an actual route
  table is deployment-specific.

## Documentation

- [Operator runbook](docs/operator-runbook.md) — operational notes for
  deploying and running the on-chain and off-chain pieces, including
  known hazards worth knowing before you hit them yourself.
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- `CLAUDE.md` — the full specification this codebase implements.

## License

[Apache License 2.0](LICENSE).
