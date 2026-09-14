# Contributing

Tallybook is infrastructure that handles money. That shapes how changes
get made here more than it does in most projects — read this before
opening a PR.

## Ground rules

- **No placeholders, no stubbed handlers, no `panic("TODO")`.** A
  function is finished when it's committed, with tests.
- **Money is integer, always.** `*big.Int` in Go, `NUMERIC(39,0)` in
  Postgres. Never `float64`, never a decimal string in arithmetic.
- **No panics outside `main` startup validation.** A library function
  returns an error.
- **Every I/O function takes `ctx context.Context` first** and honours
  cancellation. Every external call has a timeout.
- **`log/slog` only.** No `fmt.Println`, no `log.Printf`.
- **A secret is never logged, formatted, or committed.** If you touch
  anything that handles `TB_OPERATOR_SECRET` or a signing key, check that
  redaction still holds (`internal/config`'s `Secret` type, and its own
  redaction test) before you open a PR.
- If a change to `internal/stellar` is required, **verify the shape
  against the live Soroban RPC surface or the real contract source
  first** — this package has a documented history of catching real gaps
  between assumption and the live network (see its own doc comments and
  `testdata/stellar/README.md`); don't reintroduce one from memory.
- The three contracts under `internal/stellar`'s bindings
  (`price_book`, `statement_registry`, `one-way-channel`) are deployed
  and immutable from this repository's side. If an interface looks
  wrong, say so in the PR rather than working around it silently —
  `one-way-channel` in particular is external and **unaudited**; treat
  it accordingly in anything you write about it.

## Local setup

```sh
git clone https://github.com/Tallybook-Org/tallybook.git
cd tallybook
make up      # starts Postgres via docker-compose (localhost:5433)
cp .env.example .env
make check   # gofmt, go vet, staticcheck, and the full test suite
```

`make check` is exactly what CI runs (`.github/workflows/ci.yml`), in the
same order — if it passes locally, CI should too. The test suite
includes real integration tests against Postgres (§8 of `CLAUDE.md`:
"Integration tests against real Postgres in Docker, not mocks"), so
`make up` has to have succeeded first; only the Soroban RPC boundary is
mocked, and only with recorded real responses under `testdata/`, never
hand-invented ones.

## Code style

- Table-driven tests, subtests via `t.Run`. Cover the error paths, not
  just the happy one.
- Errors wrapped with `%w` and enough context to know what failed and
  with what input: `fmt.Errorf("verify commitment for channel %s: %w",
  addr, err)`. Sentinel errors as package-level `var ErrX =
  errors.New(...)`, matched with `errors.Is`; a typed error (a struct
  carrying extra fields) matched with `errors.As`.
- Dependencies injected via constructor functions — no global mutable
  state.
- A package that needs to call into a sibling package generally defines
  a small interface for exactly what it needs, rather than depending on
  that package's concrete type — see `internal/custody.ChannelReader` or
  `internal/settle.ChannelSettler` for the pattern. It keeps packages
  testable without a live database or RPC endpoint, and it's the
  convention throughout this codebase; a new dependency edge that skips
  it stands out.
- Every exported identifier gets a doc comment. Comments explain *why*,
  not what the code already says — this repository leans heavily on
  doc comments to record a design decision, a verified assumption, or a
  known gap, right next to the code it applies to, rather than losing
  that context to a commit message no one rereads.
- `gofmt`, `go vet`, and `staticcheck` clean before every commit
  (`make check`).

## Dependencies

Justify every third-party module in the commit that adds it. Prefer the
standard library — each dependency here is attack surface and
maintenance cost on top of code that moves money.

## Git workflow

- Stage named files. Never `git add .` or `git add -A`.
- One commit per logical unit — one type, one function, one test file.
  Feature and its test are commonly two separate commits
  (`feat(scope): ...` then `test(scope): ...`) rather than one that does
  both.
- Push immediately after every commit. Never batch several commits
  before pushing.
- Conventional commits: `type(scope): description`, lowercase,
  imperative, no trailing period. Scopes in use: `store`, `stellar`,
  `merkle`, `meter`, `custody`, `settle`, `indexer`, `httpapi`,
  `config`, `ci`, `workspace`.
- Never force-push, never rewrite history that's already been pushed.
- Never commit a secret, a keypair, or a `.env` file.

## Opening a PR

Say what you changed and why, and call out anywhere the change touches
money handling, secret handling, or an on-chain interface specifically —
those get the closest review. If you deliberately deviated from
something `CLAUDE.md` specifies, say so and say why; "the requirement as
written would lose money" is a legitimate reason to stop and flag it
rather than build it.

## Reporting a vulnerability

See [SECURITY.md](SECURITY.md) — please don't open a public issue for a
security problem.
