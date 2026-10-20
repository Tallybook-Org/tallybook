<!-- GENERATED FILE - DO NOT EDIT BY HAND.
     Source of truth: docs/WAVE2_BACKLOG.json
     Regenerate with: python3 scripts/gen-wave-backlog.py  (or `make backlog`)
     Any hand edit here is lost on the next regeneration. -->

# Wave 2 backlog

**160 issues · 27475 points.**

| Tier | Label | Count | Points each | Subtotal |
|---|---|---:|---:|---:|
| high | `complexity: high` | 1 | 340 | 340 |
| medium | `complexity: medium` | 10 | 255 | 2550 |
| low | `complexity: low` | 149 | 165 | 24585 |
| **total** | | **160** | | **27475** |

Complexity uses this repository's existing `complexity: low|medium|high` labels (see `gh label list`); `trivial` in the wave brief maps to `low`. Grades describe real scope, per `MAINTAINER.md` section 2 — they are not adjusted to reach a point total.

`area` is taken from the commit scopes `CONTRIBUTING.md` already defines, since the repository has no `area:` labels. It appears in each issue body rather than as a label.

---

## 1. fix(workspace): set read, write and idle timeouts on all three http servers

`complexity: high` · 340 points · area `workspace`

**What's wrong.** cmd/collector/main.go:110, cmd/indexer/main.go:105 and cmd/settler/main.go:114 each build a server with only Addr and Handler. A connection that opens and never completes its request headers is held indefinitely, so a trivial Slowloris exhausts the listener. On the settler that takes down the /metrics endpoint §6 calls the operator's alerting surface for the nearest refund deadline — the single most important number this system produces — while the deadline keeps approaching. CLAUDE.md §8 already requires every external call to carry a timeout; the inbound direction was missed.

**Proposed fix.** All three binaries construct `&http.Server{Addr:..., Handler:...}` with no ReadHeaderTimeout, ReadTimeout, WriteTimeout or IdleTimeout. Add explicit timeouts to each, sourced from config rather than hardcoded, and document the chosen values.

**Acceptance criteria**

1. All three servers set ReadHeaderTimeout, ReadTimeout, WriteTimeout and IdleTimeout to non-zero values
2. A test opens a connection, writes a partial header, and asserts the server closes it rather than holding it open
3. Timeout values come from internal/config with validated defaults, not literals at the call site
4. The settler's /metrics endpoint still answers within its timeout under a concurrent-reader test
5. go vet and staticcheck stay clean

**Guidance**

- The three call sites are one-liners; the change is small but spans all three binaries, so keep the values consistent.
- internal/config/config.go is where the new fields belong — follow the existing validated-field pattern and fail fast per §7.
- ReadHeaderTimeout is the one that actually stops Slowloris; ReadTimeout alone does not.
- Do not set WriteTimeout shorter than the slowest legitimate upstream response the collector proxies, or you will truncate real responses.

---

## 2. refactor(settle): replace raw lifecycle status literals with typed constants

`complexity: medium` · 255 points · area `settle`

**What's wrong.** internal/settle/policy.go:95 decides the channel-closing sweep trigger on `state.Status == "closing"`. The same untyped literals recur in internal/settle/submit.go:160, internal/settle/retry.go:156, internal/meter/anchor.go:375 and internal/meter/period.go:107. A misspelling compiles and silently disables a sweep trigger; §6 lists channel-closing as one of four triggers that decide whether the operator gets paid before the funder can reclaim the balance. The repo already does this correctly for protocol names (internal/httpapi/metering.go:26) — the lifecycle statuses just never got the same treatment.

**Proposed fix.** Channel, settlement, and period lifecycle statuses are compared and assigned as bare string literals across five files. Introduce typed string constants for each lifecycle and use them everywhere, so a typo is a compile error.

**Acceptance criteria**

1. A typed constant exists for each of the channel, settlement, period and anchor lifecycles
2. No bare status literal remains in non-test code under internal/ (a grep in CI or a test asserts this)
3. Passing an unrecognised status to Decide returns a wait decision and does not settle
4. Existing settle and meter tests pass unchanged apart from constant references

**Guidance**

- Mirror the existing const block at internal/httpapi/metering.go:26, which already does this for protocol names.
- The database CHECK constraints in internal/store/migrations are the source of truth for the legal values — read them first and do not invent a status the schema will reject.
- Keep the constants' underlying type `string` so existing pgx scans into them keep working without conversion.

---

## 3. ci(workspace): add govulncheck and a gosec job to the pipeline

`complexity: medium` · 255 points · area `ci`

**What's wrong.** .github/workflows/ci.yml has no vulnerability or security-lint step. This is not hypothetical: the missing HTTP server timeouts filed alongside this issue are exactly gosec's G112 (Slowloris), and nothing in the current pipeline flags them. For a repository whose own CONTRIBUTING opens with "Tallybook is infrastructure that handles money" and which pins two direct dependencies handling XDR and Postgres, an unnoticed advisory in either is a live risk.

**Proposed fix.** CI runs gofmt, go vet, staticcheck, go build and go test. Add govulncheck for known module vulnerabilities and gosec for the security-lint classes staticcheck does not cover.

**Acceptance criteria**

1. govulncheck ./... runs in CI and fails the build on a known advisory in a reachable path
2. gosec runs in CI; any rule deliberately excluded is listed with a one-line reason in the workflow
3. gosec flags the G112 server-timeout finding on the pre-fix tree, proving the job actually catches it
4. The new jobs do not require Postgres and so run even when the service container is unavailable

**Guidance**

- Add both as separate steps in the existing build-vet-staticcheck-test job rather than new jobs, so they share the Go setup and module cache.
- staticcheck is already pinned to an exact version (2025.1.1) in the workflow — pin govulncheck and gosec the same way; an unpinned security tool turns CI red on someone else's release.
- govulncheck needs the module graph, so it must run after `go mod download`.

---

## 4. fix(settle): make Decide refuse a channel that is not open or closing

`complexity: medium` · 255 points · area `settle`

**What's wrong.** §6 scopes the sweep to "every channel with status IN ('open','closing')", but internal/settle/policy.go only inspects status to test for "closing" at line 95. A channel already refunded or closed, reaching Decide with a stale non-zero exposure row, satisfies the deadline-pressure branch above it and returns ShouldSettle — a settle attempt against a channel with nothing left to take, which is a pure transaction-fee loss. The daemon's own query currently filters correctly, so this is a latent gap rather than a live bug; Decide is documented as a pure total function of its inputs, which is exactly why it should not depend on a caller invariant it can check itself.

**Proposed fix.** Decide trusts its caller to have filtered channels to status open or closing. Make it reject any other status itself and return a wait decision.

**Acceptance criteria**

1. Decide returns a wait decision for status closed and for status refunded, even under deadline pressure
2. A table-driven test covers all four statuses crossed against deadline pressure present and absent
3. The existing open and closing behaviour is unchanged, proven by the current tests passing untouched
4. The daemon's channel query is left as the first line of defence, not replaced

**Guidance**

- internal/settle/policy.go:83 is the guard clause that already short-circuits on zero exposure — add the status check beside it, before the trigger switch.
- Pair this with the typed-status-constant issue; doing that one first makes this a two-line change.
- Do not return an error: Decide is deliberately a pure function with no error path, and callers treat a wait decision as the safe default.

---

## 5. fix(stellar): verify the generated auth nonce is in the range soroban accepts

`complexity: medium` · 255 points · area `stellar`

**What's wrong.** internal/stellar/invoke.go:278 returns `int64(binary.BigEndian.Uint64(b[:]))`. With the top bit set — half the time — the nonce is negative. Whether the host accepts a negative nonce in a SorobanAddressCredentials entry is not something this repo has verified, and CONTRIBUTING is explicit that a change or claim in internal/stellar must be checked "against the live Soroban RPC surface or the real contract source first". The same file already documents one hard-won nonce-footprint trap (CLAUDE.md §4), so the cost of being wrong here is a class of intermittent, hard-to-reproduce auth failure on roughly half of fresh invocations.

**Proposed fix.** The authorization-entry nonce is generated by reinterpreting eight random bytes as a signed int64, which is negative for roughly half of all draws. Confirm against the Soroban host what range it actually accepts and constrain generation to it.

**Acceptance criteria**

1. The accepted nonce range is established from the Soroban host source or live network behaviour, and recorded in a doc comment with the reference
2. Nonce generation is constrained to that range, with a test asserting many generated nonces all fall inside it
3. If negative nonces turn out to be accepted, a written finding in the doc comment recording that, with evidence, closes this issue — no code change required
4. Nonce generation stays uniformly distributed over whatever range is chosen; it must not collapse to a narrow band

**Guidance**

- Read internal/stellar/invoke.go's existing doc comments first — they record the CAP-71 and footprint findings and set the evidentiary bar for this package.
- The honest outcome here may be "this is already fine"; a written, cited finding is an acceptable completion per CONTRIBUTING's verify-before-you-claim rule.
- Do not simply mask the sign bit without checking: halving the nonce space to avoid an unverified problem is its own, quieter change.

---

## 6. fix(workspace): compare ErrServerClosed with errors.Is, not !=

`complexity: low` · 165 points · area `workspace` · `good first issue`

**What's wrong.** cmd/collector/main.go:114, cmd/indexer/main.go:119 and cmd/settler/main.go:128 all use `!=`. CLAUDE.md §8 and CONTRIBUTING both require sentinel errors to be matched with errors.Is. Today net/http returns the sentinel unwrapped so the comparison happens to work; if that ever changes, or a middleware wraps it, each binary logs a normal shutdown as a fatal startup error instead — noise in exactly the logs an operator reads during an incident.

**Proposed fix.** All three binaries test the shutdown sentinel with `err != http.ErrServerClosed`. Switch to errors.Is so a wrapped sentinel is still recognised.

**Acceptance criteria**

1. All three sites use errors.Is(err, http.ErrServerClosed)
2. A test wraps http.ErrServerClosed with fmt.Errorf("%w") and asserts the shutdown path still treats it as a clean shutdown
3. A real listen failure, such as a port already in use, is still reported as a fatal error
4. gofmt, go vet and staticcheck stay clean

**Guidance**

- Three one-line changes; cmd/collector/main.go:114 is the clearest place to start.
- The errors package is already imported in each file, so no import changes are needed.
- Keep the existing log message and exit behaviour — this is a comparison fix, not a rework of shutdown.

**Entry point:** `cmd/collector/main.go`

---

## 7. refactor(stellar): remove the panic from mustScAddress

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** internal/stellar/invoke.go:288 is the only panic in non-test code under internal/. CLAUDE.md §8 states the rule without exception — "No panics outside main startup validation. A library function returns an error" — and CONTRIBUTING repeats it. The existing comment argues the input is always a package-internal constant, which is true today, but contract IDs reach this package from TB_PRICE_BOOK_ID and TB_STATEMENT_REGISTRY_ID (§7), so the assumption is one config path away from being false, and the failure mode is a crashed daemon rather than a startup error naming the bad variable.

**Proposed fix.** mustScAddress panics when a contract ID fails to parse. Return an error instead and thread it through the callers, so internal/ contains no panic outside main.

**Acceptance criteria**

1. mustScAddress is gone or returns an error, and no panic remains in non-test code under internal/
2. A test passes a malformed contract ID and asserts an error is returned, naming the sentinel or type it matches
3. A grep-based test or CI step asserts internal/ stays panic-free, so this cannot regress
4. Callers surface the error with the wrapping style CONTRIBUTING requires, naming the offending input

**Guidance**

- Start at internal/stellar/invoke.go:281 and follow the callers upward; the chain is deep, which is the reason the panic was there.
- Config-sourced contract IDs should fail at startup in internal/config instead, per §7's fail-fast-with-the-variable-named rule — that may be the better half of the fix.
- Do not convert the panic into a silently ignored zero value; an unparseable address must stop the call.

**Entry point:** `internal/stellar/invoke.go`

---

## 8. test(workspace): convert the remaining non-table tests to subtests

`complexity: low` · 165 points · area `workspace` · `good first issue`

**What's wrong.** CONTRIBUTING requires "Table-driven tests, subtests via t.Run" and CLAUDE.md §8 repeats it, but the measured ratio across the repo is 27 t.Run sites to 281 test functions. Some of those tests are legitimately not table-shaped — an integration test against Postgres often is not — so this is an audit with a conversion, not a blanket rewrite. The payoff is that a failing case names itself instead of failing an umbrella test.

**Proposed fix.** The suite has 281 test functions but only 27 t.Run call sites. Audit which tests are genuinely table-shaped and convert those, leaving a short note where a flat test is the right shape.

**Acceptance criteria**

1. Every test that asserts the same behaviour over more than two input sets uses t.Run with a named case per set
2. Any test deliberately left flat carries a one-line comment saying why
3. Total test count does not fall, and go test ./... still passes with -count=1
4. Subtest names are stable strings, not derived from loop indices, so a failure names the case

**Guidance**

- Start with internal/merkle and internal/meter, the two most input-driven packages.
- internal/settle/policy_test.go already follows the target shape — copy its structure rather than inventing one.
- Do not convert the Postgres integration tests for the sake of the count; their setup cost per subtest is real.

**Entry point:** `internal/merkle/leaf_test.go`

---

## 9. chore(workspace): add CODEOWNERS routing money-path packages to review

`complexity: low` · 165 points · area `ci` · `good first issue`

**What's wrong.** There is no CODEOWNERS file anywhere in the repo. CONTRIBUTING says changes touching money handling, secret handling or an on-chain interface "get the closest review", but nothing enforces that routing, so such a PR can be approved by whoever is free. §5 calls commitments "THE MOST IMPORTANT TABLE IN THIS SCHEMA" and a bad migration against it is unrecoverable.

**Proposed fix.** Add a CODEOWNERS file routing review of the packages that move money — internal/custody, internal/settle, internal/store/migrations and internal/stellar — to named owners.

**Acceptance criteria**

1. A .github/CODEOWNERS exists covering internal/custody, internal/settle, internal/store/migrations and internal/stellar
2. GitHub's CODEOWNERS syntax check passes and the repo settings show the rules as active
3. A PR touching only docs/ does not trigger the money-path owners
4. The file carries a comment explaining why these four paths are singled out

**Guidance**

- CONTRIBUTING's "Opening a PR" section already names the three categories to protect — mirror those paths rather than inventing a split.
- Ownership must be a team or a user that actually exists, or GitHub silently ignores the rule.
- Keep the catch-all last; CODEOWNERS applies the last matching pattern, not the most specific.

**Entry point:** `CONTRIBUTING.md`

---

## 10. docs(workspace): add a pull request template carrying the money-path checklist

`complexity: low` · 165 points · area `workspace` · `good first issue`

**What's wrong.** No PR template exists. CONTRIBUTING's "Opening a PR" section lists exactly what a PR must say — including that "the requirement as written would lose money" is a legitimate reason to flag rather than build — but a contributor only learns this by reading CONTRIBUTING first. A template puts the prompt where the work happens.

**Proposed fix.** Add a PR template prompting for the disclosures CONTRIBUTING already asks for in prose: what changed, whether money, secret or on-chain handling is touched, and any deliberate CLAUDE.md deviation.

**Acceptance criteria**

1. A .github/PULL_REQUEST_TEMPLATE.md exists and renders on a new PR
2. It prompts for money, secret and on-chain-interface impact as three separate explicit questions
3. It asks whether make check passed locally, matching CONTRIBUTING's stated gate
4. It stays under roughly 30 lines, so it is read rather than deleted

**Guidance**

- Lift the wording from CONTRIBUTING's "Opening a PR" section verbatim so the two cannot drift.
- Use comment markers for instructions so they do not end up in the PR body.
- Do not add a checklist nobody can fail; each item should be answerable with real content.

**Entry point:** `CONTRIBUTING.md`

---

## 11. chore(workspace): add an editorconfig covering go, sql and yaml

`complexity: low` · 165 points · area `workspace` · `good first issue`

**What's wrong.** No .editorconfig exists. gofmt keeps Go consistent, but the SQL migrations under internal/store/migrations and the YAML in .github/workflows have no formatter at all, and a migration is a file nobody may edit once applied (§5) — so a whitespace-only diff on one is noise on exactly the files that most need a clean history.

**Proposed fix.** Add an .editorconfig fixing indentation, final newline and trailing whitespace for the file types in the repo: Go, SQL migrations, YAML workflows and Markdown.

**Acceptance criteria**

1. An .editorconfig covers *.go, *.sql, *.yml and *.md with explicit indent style and size
2. Settings for *.go match what gofmt already produces, so the two never disagree
3. Running the repo's existing make check produces no new diff after the file is added
4. insert_final_newline and trim_trailing_whitespace are set for the SQL and YAML globs

**Guidance**

- Check the existing migrations under internal/store/migrations for their current indentation before choosing a value — match reality, do not reformat applied migrations.
- gofmt uses tabs for Go; setting spaces there would fight the formatter CI enforces.
- This is configuration only — do not reformat existing files in the same PR.

**Entry point:** `internal/store/migrations/0001_periods.sql`

---

## 12. ci(workspace): evaluate golangci-lint against the existing staticcheck job

`complexity: low` · 165 points · area `ci` · `good first issue`

**What's wrong.** There is no .golangci.yml and CI runs staticcheck alone (.github/workflows/ci.yml). staticcheck is strong but does not cover several classes this codebase cares about — errorlint for the %w and errors.Is rules CLAUDE.md §8 mandates, and bodyclose on the Soroban RPC client's HTTP responses. Whether that is worth a second linter is a real question, not a foregone conclusion.

**Proposed fix.** Assess whether golangci-lint adds findings staticcheck does not on this codebase. If it does, add a pinned config and job; if it does not, record that finding and close.

**Acceptance criteria**

1. golangci-lint is run once over the tree and its findings are triaged into real and noise
2. If adopted: a pinned version and a config enabling only the linters that found something, with staticcheck not duplicated
3. If not adopted: a written finding in the PR recording what it found and why it was not worth the job, which closes this issue
4. Either way CI runtime is reported before and after, so the cost is explicit

**Guidance**

- errorlint and bodyclose are the two most likely to earn their place here; start by enabling just those.
- staticcheck is already pinned at 2025.1.1 in the workflow — an unpinned golangci-lint would make CI fail on someone else's release.
- An honest "not worth it" outcome is a valid completion; do not add a job that only reports noise.

**Entry point:** `.github/workflows/ci.yml`

---

## 13. ci(workspace): enable dependabot for go modules and github actions

`complexity: low` · 165 points · area `ci` · `good first issue`

**What's wrong.** No .github/dependabot.yml exists. go.mod pins two direct dependencies — github.com/jackc/pgx/v5 and github.com/stellar/go-stellar-sdk — that respectively hold the database credentials and build the transactions that move money, and the workflow pins actions/checkout and actions/setup-go by major version. Nothing currently tells anyone when an update to either lands. CONTRIBUTING's "justify every third-party module" rule makes the small dependency surface a deliberate choice, which makes watching it cheap.

**Proposed fix.** Add a dependabot config watching the Go module requirements and the pinned GitHub Actions, grouped so routine action bumps do not each open a PR.

**Acceptance criteria**

1. A dependabot config watches gomod and github-actions ecosystems
2. Action updates are grouped into a single PR; the two direct Go modules are not grouped
3. The schedule is weekly or less frequent, so the backlog is not flooded
4. A comment records that each Go module bump still needs the CONTRIBUTING justification in its commit

**Guidance**

- Keep the two direct Go dependencies ungrouped: each needs its own justification commit per CONTRIBUTING's dependency rule.
- Indirect dependencies can be grouped; there are eight of them in go.mod today.
- Do not enable version updates for the Postgres service image pinned in the workflow — that one has to match the migrations.

**Entry point:** `.github/workflows/ci.yml`

---

## 14. refactor(store): consolidate the three copies of numericToBigInt

`complexity: medium` · 255 points · area `store`

**What's wrong.** internal/settle/numeric.go:23, internal/meter/numeric.go:22 and internal/indexer/reconcile.go:138 each define their own numericToBigInt, converting a pgtype.Numeric into the *big.Int that every amount in this system is carried as. This is the single conversion standing between NUMERIC(39,0) in Postgres and the money arithmetic CLAUDE.md §8 insists is integer-only. Three copies means a correctness fix — negative exponents, NaN, a null Int — lands in one and silently misses two, and the three are not guaranteed identical today. A rounding bug here is a financial bug by the repo's own framing.

**Proposed fix.** numericToBigInt is implemented three separate times — in internal/settle, internal/meter and internal/indexer. Move one canonical implementation into a shared package and delete the copies.

**Acceptance criteria**

1. One exported conversion exists in a shared package and all three call sites use it
2. The three former copies are deleted, verified by a grep-based assertion in the test or CI
3. A table-driven test covers a negative exponent, a positive exponent, a zero value, a NaN and an invalid Numeric, asserting the error for each rejected case
4. The three packages' existing tests pass unchanged, proving behaviour was preserved
5. No call site silently drops the error the conversion returns

**Guidance**

- Diff the three implementations before moving any of them — if they already differ, the difference is itself the finding and belongs in the PR description.
- internal/store is the natural home: all three consumers already depend on the database layer, so it adds no new dependency edge.
- CONTRIBUTING's sibling-package convention prefers a small interface over a concrete dependency; a free function is the exception that fits here.
- Watch the Exp handling: internal/indexer/reconcile.go:148 scales by 10**Exp, so check whether the other two do the same before unifying.

---

## 15. test(stellar): add a fuzz target for the eight contract event decoders

`complexity: medium` · 255 points · area `stellar`

**What's wrong.** internal/stellar/events.go exposes eight decoders taking a base64 string straight off the chain — DecodePriceBookPublishEvent at line 98 through DecodeChannelRefundEvent at line 381. The repo has zero fuzz targets. This input is attacker-adjacent: anyone can emit an event with matching topics on a public network, and issue #7 is a live case of a decoder meeting a real payload it did not expect and wedging cmd/indexer's cursor permanently. A fuzzer is the cheapest way to find the next one before the network does.

**Proposed fix.** Add a fuzz target driving the eight Decode*Event functions with arbitrary base64 input, asserting each either returns a typed error or a fully-populated struct, and never panics.

**Acceptance criteria**

1. A Fuzz function covers all eight Decode*Event entry points
2. No input causes a panic, an out-of-range slice index, or an unbounded allocation
3. Every rejection path returns an error that names the field and the expected type, not a bare failure
4. A seed corpus is committed from the real recorded payloads already in testdata/stellar/
5. The target runs in CI with a bounded -fuzztime so it cannot hang the pipeline

**Guidance**

- testdata/stellar/ already holds recorded real events — seed from those rather than inventing corpus entries, per CLAUDE.md §8's recorded-responses-only rule.
- Issue #7's three-field Withdraw payload is the known-bad case; assert the fuzzer reaches that shape.
- Keep the fuzz target in package stellar so it can see the unexported helpers the decoders share.

---

## 16. test(stellar): add a fuzz target for the ScVal decode helpers

`complexity: medium` · 255 points · area `stellar`

**What's wrong.** internal/stellar/xdr.go:177-295 defines twelve decoders — DecodeBool through DecodeMap — each of which must reject a mismatched ScVal type. There are no fuzz targets in the repo. DecodeI128 at line 218 is the one that matters most: it reassembles a 128-bit amount from hi and lo words, and every charged amount and settlement figure in the system passes through it. A sign or shift error there is a money error, and it is exactly the class a fuzzer finds.

**Proposed fix.** Add a fuzz target over the twelve Decode* ScVal helpers, asserting type confusion is always reported as ErrWrongScValType rather than producing a wrong value.

**Acceptance criteria**

1. A Fuzz function reaches all twelve Decode* helpers with both matching and mismatched ScVal types
2. Every type mismatch returns *ErrWrongScValType and never a zero value with a nil error
3. DecodeI128 round-trips against ScvI128 for arbitrary big.Int inputs across the full signed 128-bit range, including both extremes
4. No input panics or allocates unboundedly

**Guidance**

- internal/stellar/xdr_test.go already covers round trips — extend that file's helpers rather than duplicating the setup.
- The I128 two's-complement handling at xdr.go:222-226 is the highest-value thing to fuzz; build the corpus around its boundaries.
- Use the xdr package's own types to construct inputs; fuzzing raw bytes through the XDR unmarshaller tests the SDK, not this code.

---

## 17. test(meter): add a fuzz target for ParseSchedule

`complexity: medium` · 255 points · area `meter`

**What's wrong.** internal/meter/schedule.go:81 parses an operator's price schedule from raw bytes, guarded by six sentinels at lines 24-29 including ErrUnitPriceOutOfRange, which exists specifically to keep a price inside the i128 range statement_registry uses. The repo has no fuzz targets. This function decides what every request is charged; a schedule that parses into a valid-looking struct with a bad unit price prices real traffic wrongly, and the pricing path has no second check behind it.

**Proposed fix.** Add a fuzz target for ParseSchedule over arbitrary JSON, asserting every malformed schedule is rejected with one of the six named sentinels and no partially-populated Schedule escapes.

**Acceptance criteria**

1. A Fuzz function drives ParseSchedule with arbitrary bytes and never panics
2. Every returned error matches one of the six package sentinels via errors.Is; no bare or wrapped-unknown error escapes
3. A successful parse always yields a Schedule whose every rule has a non-empty method, a path_template starting with /, and a unit price within the i128 range
4. A rejected parse returns a nil Schedule, never a partially-filled one

**Guidance**

- internal/meter/schedule_test.go already enumerates the rejection cases — seed the corpus from its table so the fuzzer starts from known-interesting inputs.
- The i128 bound at schedule.go:29 is the subtle one: check it against the exact range, not math.MaxInt64.
- Assert the nil-on-error contract explicitly; a non-nil Schedule alongside an error is the bug most likely hiding here.

---

## 18. test(merkle): add a fuzz target asserting tree and proof agreement

`complexity: medium` · 255 points · area `merkle`

**What's wrong.** internal/merkle/tree.go builds the sorted-pair tree whose roots the deployed statement_registry accepts or rejects, and enforces the 32-node cap at line 93. The repo has no fuzz targets. CLAUDE.md §4 warns that sorted-pair implementations diverge on unbalanced trees and that the seven-leaf fixture is where that shows; a fuzzer over arbitrary leaf counts covers the unbalanced shapes no fixture enumerates. A root this package gets wrong is a proof the contract refuses on chain, which is an anchor that cannot be submitted.

**Proposed fix.** Add a fuzz target that builds a tree from an arbitrary leaf set and asserts every generated proof verifies against the root, and that no proof exceeds the contract's 32-node cap.

**Acceptance criteria**

1. A Fuzz function builds trees from 1 to at least 1024 arbitrary leaves without panicking
2. Every leaf's generated proof satisfies VerifyProof against the tree root
3. No generated proof exceeds 32 nodes, and a leaf set large enough to breach it returns the documented error rather than a long proof
4. Duplicate leaves in one set behave as the existing documented duplicate handling specifies, not by silent dedup
5. The existing testdata/merkle fixture roots still match exactly

**Guidance**

- Do not regenerate testdata/merkle — CLAUDE.md §2 requires those stay byte-identical to the Rust reference; the fuzzer complements them.
- tree.go:45's duplicate-leaf map is the subtle interaction; fuzz leaf sets containing repeats deliberately.
- Sorted-pair hashing means a proof node can equal its sibling — contracts repo issue #13 tracks exactly that fixture gap, so include the case here.

---

## 19. test(store): add a fuzz target for ParseMigrations

`complexity: low` · 165 points · area `store` · `good first issue`

**What's wrong.** internal/store/migrate.go:39 parses numbered migration files into an ordered list, and §5 is explicit that an applied migration must never be edited. The repo has no fuzz targets. Migration ordering decides the shape of the commitments table — "THE MOST IMPORTANT TABLE IN THIS SCHEMA" — so a filename the parser mis-sorts or silently skips is a schema applied in the wrong order on a fresh database, which is a failure nobody sees until production.

**Proposed fix.** Add a fuzz target for ParseMigrations over arbitrary in-memory filesystems, asserting malformed or out-of-order migration filenames are rejected rather than silently reordered.

**Acceptance criteria**

1. A Fuzz function drives ParseMigrations with arbitrary fstest.MapFS contents and never panics
2. Duplicate version numbers, gaps, non-numeric prefixes and zero-padded variants each return a named error rather than being skipped
3. A successful parse always returns migrations in strictly ascending version order
4. ErrNoMigrations is returned for an empty filesystem, matched with errors.Is

**Guidance**

- testing/fstest.MapFS is the right input type; internal/store/migrations_fs_test.go already uses it.
- The ordering assertion matters more than the error text: a silently reordered list is worse than a rejected one.
- Zero-padding is the trap — 0010 must sort after 0009, not lexically before 002.

**Entry point:** `internal/store/migrate.go`

---

## 20. test(httpapi): add a benchmark for the metering middleware hot path

`complexity: medium` · 255 points · area `httpapi`

**What's wrong.** CLAUDE.md §5 calls the requests table "The hot path", and internal/httpapi/metering.go is the code on it — every metered request passes through it, generating a request ID, hashing the endpoint and pricing against the catalog. The repo has zero benchmarks, so the per-request cost of metering a paid API is simply unknown, and so is whether a change to it regressed anything. An operator deciding whether to put this in front of a high-volume API has no number to reason about.

**Proposed fix.** Add a benchmark over the metering middleware measuring per-request overhead and allocations, with the Recorder and LedgerSource stubbed so it measures this package only.

**Acceptance criteria**

1. A Benchmark function measures the middleware end to end with stubbed Recorder and LedgerSource
2. Allocations per request are reported via b.ReportAllocs and recorded in the PR description as the baseline
3. The benchmark does not touch Postgres or the network, so it runs in CI without the service container
4. A second benchmark isolates endpoint hashing and request-ID generation, so a regression can be attributed

**Guidance**

- internal/httpapi/metering_test.go already has stub implementations of both interfaces — reuse them rather than writing new ones.
- crypto/rand for the request ID is likely the dominant cost; measure it separately before concluding anything about the rest.
- Report the baseline numbers in the PR body; a benchmark with no recorded baseline cannot detect a future regression.

---

## 21. test(merkle): add benchmarks for tree building and proof generation

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** internal/merkle/tree.go builds a tree over every request in a billing period before a statement can be anchored. The repo has zero benchmarks. A period covering a month of a busy API is a large leaf set, and nothing records how long that takes or how much it allocates — so nobody knows whether period closing is a millisecond or a minute, which is the difference between anchoring on time and holding a transaction open against the price-change boundary §4 forbids a statement from spanning.

**Proposed fix.** Add benchmarks for BuildTree and proof generation across representative leaf counts, so the cost of closing a billing period is a known number.

**Acceptance criteria**

1. Benchmarks cover BuildTree at several leaf counts spanning at least three orders of magnitude
2. A separate benchmark measures generating proofs for every leaf in a built tree
3. b.ReportAllocs is enabled and baseline numbers are recorded in the PR description
4. The benchmarks use deterministic synthetic leaves, not the fixtures, so timings are stable

**Guidance**

- internal/merkle/tree_test.go has leaf-generation helpers already — reuse them.
- Use b.ResetTimer after generating leaves so setup is excluded from the measurement.
- Do not benchmark against testdata/merkle fixtures: they are tiny and fixed, which tells you nothing about scale.

**Entry point:** `internal/merkle/tree_test.go`

---

## 22. test(settle): add a benchmark for exposure calculation

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** internal/settle/exposure.go:72 runs two correlated subqueries over commitments for every channel, on every settler tick. The repo has zero benchmarks. §6 requires this to run for every open channel each tick, so the per-channel cost sets the ceiling on how many channels one settler can watch — and the settler is the component that decides whether the operator gets paid. That ceiling is currently unmeasured.

**Proposed fix.** Add a benchmark for Calculator.Calculate against a seeded Postgres instance, measuring the per-channel cost of the exposure query at realistic commitment counts.

**Acceptance criteria**

1. A benchmark measures Calculate against a channel seeded with commitment counts spanning at least three orders of magnitude
2. Baseline timings are recorded in the PR description for each size
3. The benchmark skips cleanly when Postgres is unavailable, matching the existing integration-test guard
4. Whether the index at migration 0003 is actually used is confirmed with EXPLAIN and stated in the PR

**Guidance**

- internal/settle/helpers_test.go:28 holds the existing Postgres-availability guard — reuse it so the benchmark behaves like the integration tests.
- The MIN(received_at) subquery filtered on cumulative_amount > last_settled_amount is the one most likely to scan; check it with EXPLAIN first.
- scripts/test-ci.sh fails CI on a skipped test — confirm a skipped benchmark does not trip it.

**Entry point:** `internal/merkle/tree_test.go`

---

## 23. docs(config): add a runnable example for Config.LogValue

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Config.LogValue` is exported from `internal/config` at internal/config/config.go:111 (`func (c *Config) LogValue() slog.Value`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleConfig_LogValue` for `Config.LogValue` (internal/config/config.go:111, a method on `Config`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleConfig_LogValue` exists in `internal/config/config_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/config/...` passes with the asserted output matching exactly
4. `go doc internal/config Config.LogValue` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/config/config.go:111 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleConfig_LogValue` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleLogValue` would silently document the package instead of `Config.LogValue`.

**Entry point:** `internal/config/config.go`

---

## 24. docs(config): add a runnable example for Config.String

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Config.String` is exported from `internal/config` at internal/config/config.go:93 (`func (c *Config) String() string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleConfig_String` for `Config.String` (internal/config/config.go:93, a method on `Config`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleConfig_String` exists in `internal/config/config_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/config/...` passes with the asserted output matching exactly
4. `go doc internal/config Config.String` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/config/config.go:93 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleConfig_String` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleString` would silently document the package instead of `Config.String`.

**Entry point:** `internal/config/config.go`

---

## 25. docs(config): add a runnable example for Secret.GoString

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Secret.GoString` is exported from `internal/config` at internal/config/secret.go:14 (`func (s Secret) GoString() string { return "[REDACTED]" }`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSecret_GoString` for `Secret.GoString` (internal/config/secret.go:14, a method on `Secret`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSecret_GoString` exists in `internal/config/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/config/...` passes with the asserted output matching exactly
4. `go doc internal/config Secret.GoString` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/config/secret.go:14 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/config/secret.go yet, so create `internal/config/example_test.go` in package `config` for it.
- The name must be exactly `ExampleSecret_GoString` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleGoString` would silently document the package instead of `Secret.GoString`.

**Entry point:** `internal/config/secret.go`

---

## 26. docs(config): add a runnable example for Secret.Reveal

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Secret.Reveal` is exported from `internal/config` at internal/config/secret.go:19 (`func (s Secret) Reveal() string { return string(s) }`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSecret_Reveal` for `Secret.Reveal` (internal/config/secret.go:19, a method on `Secret`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSecret_Reveal` exists in `internal/config/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/config/...` passes with the asserted output matching exactly
4. `go doc internal/config Secret.Reveal` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/config/secret.go:19 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/config/secret.go yet, so create `internal/config/example_test.go` in package `config` for it.
- The name must be exactly `ExampleSecret_Reveal` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleReveal` would silently document the package instead of `Secret.Reveal`.

**Entry point:** `internal/config/secret.go`

---

## 27. docs(config): add a runnable example for Secret.String

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Secret.String` is exported from `internal/config` at internal/config/secret.go:11 (`func (s Secret) String() string { return "[REDACTED]" }`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSecret_String` for `Secret.String` (internal/config/secret.go:11, a method on `Secret`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSecret_String` exists in `internal/config/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/config/...` passes with the asserted output matching exactly
4. `go doc internal/config Secret.String` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/config/secret.go:11 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/config/secret.go yet, so create `internal/config/example_test.go` in package `config` for it.
- The name must be exactly `ExampleSecret_String` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleString` would silently document the package instead of `Secret.String`.

**Entry point:** `internal/config/secret.go`

---

## 28. docs(httpapi): add a runnable example for Middleware

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `Middleware` is exported from `internal/httpapi` at internal/httpapi/metering.go:182 (`func Middleware(cfg Config) (func(http.Handler) http.Handler, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMiddleware` for `Middleware` (internal/httpapi/metering.go:182), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMiddleware` exists in `internal/httpapi/metering_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/httpapi/...` passes with the asserted output matching exactly
4. `go doc internal/httpapi Middleware` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/httpapi/metering.go:182 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 29. docs(merkle): add a runnable example for BuildTree

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `BuildTree` is exported from `internal/merkle` at internal/merkle/tree.go:40 (`func BuildTree(leaves [][32]byte) (*Tree, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleBuildTree` for `BuildTree` (internal/merkle/tree.go:40), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleBuildTree` exists in `internal/merkle/tree_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/merkle/...` passes with the asserted output matching exactly
4. `go doc internal/merkle BuildTree` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/merkle/tree.go:40 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/tree_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/tree.go`

---

## 30. docs(merkle): add a runnable example for EndpointHash

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `EndpointHash` is exported from `internal/merkle` at internal/merkle/leaf.go:41 (`func EndpointHash(method, pathTemplate string) [32]byte`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleEndpointHash` for `EndpointHash` (internal/merkle/leaf.go:41), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleEndpointHash` exists in `internal/merkle/leaf_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/merkle/...` passes with the asserted output matching exactly
4. `go doc internal/merkle EndpointHash` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/merkle/leaf.go:41 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/leaf_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/leaf.go`

---

## 31. docs(merkle): add a runnable example for Fold

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `Fold` is exported from `internal/merkle` at internal/merkle/tree.go:104 (`func Fold(leaf [32]byte, proof Proof) [32]byte`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleFold` for `Fold` (internal/merkle/tree.go:104), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleFold` exists in `internal/merkle/tree_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/merkle/...` passes with the asserted output matching exactly
4. `go doc internal/merkle Fold` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/merkle/tree.go:104 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/tree_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/tree.go`

---

## 32. docs(merkle): add a runnable example for Leaf

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `Leaf` is exported from `internal/merkle` at internal/merkle/leaf.go:48 (`func Leaf(r Record) ([32]byte, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleLeaf` for `Leaf` (internal/merkle/leaf.go:48), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleLeaf` exists in `internal/merkle/leaf_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/merkle/...` passes with the asserted output matching exactly
4. `go doc internal/merkle Leaf` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/merkle/leaf.go:48 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/leaf_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/leaf.go`

---

## 33. docs(merkle): add a runnable example for VerifyProof

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `VerifyProof` is exported from `internal/merkle` at internal/merkle/tree.go:115 (`func VerifyProof(root, leaf [32]byte, proof Proof) bool`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleVerifyProof` for `VerifyProof` (internal/merkle/tree.go:115), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleVerifyProof` exists in `internal/merkle/tree_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/merkle/...` passes with the asserted output matching exactly
4. `go doc internal/merkle VerifyProof` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/merkle/tree.go:115 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/tree_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/tree.go`

---

## 34. docs(meter): add a runnable example for Anchorer.AnchorPeriod

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Anchorer.AnchorPeriod` is exported from `internal/meter` at internal/meter/anchor.go:366 (`func (a *Anchorer) AnchorPeriod(`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleAnchorer_AnchorPeriod` for `Anchorer.AnchorPeriod` (internal/meter/anchor.go:366, a method on `Anchorer`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleAnchorer_AnchorPeriod` exists in `internal/meter/anchor_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Anchorer.AnchorPeriod` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/anchor.go:366 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleAnchorer_AnchorPeriod` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleAnchorPeriod` would silently document the package instead of `Anchorer.AnchorPeriod`.

**Entry point:** `internal/meter/anchor.go`

---

## 35. docs(meter): add a runnable example for Catalog.EffectiveLedgerAfter

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Catalog.EffectiveLedgerAfter` is exported from `internal/meter` at internal/meter/pricing.go:114 (`func (c *Catalog) EffectiveLedgerAfter(version uint32) (uint32, bool)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleCatalog_EffectiveLedgerAfter` for `Catalog.EffectiveLedgerAfter` (internal/meter/pricing.go:114, a method on `Catalog`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleCatalog_EffectiveLedgerAfter` exists in `internal/meter/pricing_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Catalog.EffectiveLedgerAfter` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/pricing.go:114 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleCatalog_EffectiveLedgerAfter` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleEffectiveLedgerAfter` would silently document the package instead of `Catalog.EffectiveLedgerAfter`.

**Entry point:** `internal/meter/pricing.go`

---

## 36. docs(meter): add a runnable example for Catalog.VersionAt

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Catalog.VersionAt` is exported from `internal/meter` at internal/meter/pricing.go:86 (`func (c *Catalog) VersionAt(ledger uint32) (*CatalogVersion, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleCatalog_VersionAt` for `Catalog.VersionAt` (internal/meter/pricing.go:86, a method on `Catalog`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleCatalog_VersionAt` exists in `internal/meter/pricing_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Catalog.VersionAt` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/pricing.go:86 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleCatalog_VersionAt` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleVersionAt` would silently document the package instead of `Catalog.VersionAt`.

**Entry point:** `internal/meter/pricing.go`

---

## 37. docs(meter): add a runnable example for Pricer.Price

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Pricer.Price` is exported from `internal/meter` at internal/meter/pricing.go:148 (`func (p *Pricer) Price(ledger uint32, method, pathTemplate string, units uint64) (*PricedRequest, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExamplePricer_Price` for `Pricer.Price` (internal/meter/pricing.go:148, a method on `Pricer`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExamplePricer_Price` exists in `internal/meter/pricing_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Pricer.Price` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/pricing.go:148 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExamplePricer_Price` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExamplePrice` would silently document the package instead of `Pricer.Price`.

**Entry point:** `internal/meter/pricing.go`

---

## 38. docs(meter): add a runnable example for Schedule.Lookup

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Schedule.Lookup` is exported from `internal/meter` at internal/meter/schedule.go:128 (`func (s *Schedule) Lookup(method, pathTemplate string) (*big.Int, bool)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSchedule_Lookup` for `Schedule.Lookup` (internal/meter/schedule.go:128, a method on `Schedule`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSchedule_Lookup` exists in `internal/meter/schedule_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Schedule.Lookup` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/schedule.go:128 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/schedule_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleSchedule_Lookup` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleLookup` would silently document the package instead of `Schedule.Lookup`.

**Entry point:** `internal/meter/schedule.go`

---

## 39. docs(meter): add a runnable example for Schedule.Rules

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Schedule.Rules` is exported from `internal/meter` at internal/meter/schedule.go:138 (`func (s *Schedule) Rules() []Rule`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSchedule_Rules` for `Schedule.Rules` (internal/meter/schedule.go:138, a method on `Schedule`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSchedule_Rules` exists in `internal/meter/schedule_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Schedule.Rules` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/schedule.go:138 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/schedule_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleSchedule_Rules` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleRules` would silently document the package instead of `Schedule.Rules`.

**Entry point:** `internal/meter/schedule.go`

---

## 40. docs(meter): add a runnable example for ScheduleHash

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `ScheduleHash` is exported from `internal/meter` at internal/meter/schedule.go:151 (`func ScheduleHash(raw []byte) [32]byte`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScheduleHash` for `ScheduleHash` (internal/meter/schedule.go:151), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScheduleHash` exists in `internal/meter/schedule_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter ScheduleHash` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/schedule.go:151 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/schedule_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/schedule.go`

---

## 41. docs(settle): add a runnable example for Daemon.LastSuccessfulChainRead

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `Daemon.LastSuccessfulChainRead` is exported from `internal/settle` at internal/settle/daemon.go:83 (`func (d *Daemon) LastSuccessfulChainRead() (time.Time, bool)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDaemon_LastSuccessfulChainRead` for `Daemon.LastSuccessfulChainRead` (internal/settle/daemon.go:83, a method on `Daemon`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDaemon_LastSuccessfulChainRead` exists in `internal/settle/daemon_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle Daemon.LastSuccessfulChainRead` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/daemon.go:83 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/daemon_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleDaemon_LastSuccessfulChainRead` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleLastSuccessfulChainRead` would silently document the package instead of `Daemon.LastSuccessfulChainRead`.

**Entry point:** `internal/settle/daemon.go`

---

## 42. docs(settle): add a runnable example for Decide

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `Decide` is exported from `internal/settle` at internal/settle/policy.go:80 (`func Decide(p Policy, state ChannelState, currentLedger uint32, now time.Time) Decision`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecide` for `Decide` (internal/settle/policy.go:80), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecide` exists in `internal/settle/policy_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle Decide` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/policy.go:80 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/policy_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/policy.go`

---

## 43. docs(settle): add a runnable example for DefaultRetryOptions

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `DefaultRetryOptions` is exported from `internal/settle` at internal/settle/retry.go:69 (`func DefaultRetryOptions(deadline time.Time) RetryOptions`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDefaultRetryOptions` for `DefaultRetryOptions` (internal/settle/retry.go:69), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDefaultRetryOptions` exists in `internal/settle/retry_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle DefaultRetryOptions` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/retry.go:69 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/retry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/retry.go`

---

## 44. docs(settle): add a runnable example for Exposure.Age

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `Exposure.Age` is exported from `internal/settle` at internal/settle/exposure.go:51 (`func (e *Exposure) Age(now time.Time) time.Duration`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleExposure_Age` for `Exposure.Age` (internal/settle/exposure.go:51, a method on `Exposure`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleExposure_Age` exists in `internal/settle/exposure_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle Exposure.Age` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/exposure.go:51 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/exposure_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleExposure_Age` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleAge` would silently document the package instead of `Exposure.Age`.

**Entry point:** `internal/settle/exposure.go`

---

## 45. docs(settle): add a runnable example for IsPermanent

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `IsPermanent` is exported from `internal/settle` at internal/settle/retry.go:28 (`func IsPermanent(err error) bool`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleIsPermanent` for `IsPermanent` (internal/settle/retry.go:28), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleIsPermanent` exists in `internal/settle/retry_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle IsPermanent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/retry.go:28 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/retry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/retry.go`

---

## 46. docs(settle): add a runnable example for Metrics.ServeHTTP

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `Metrics.ServeHTTP` is exported from `internal/settle` at internal/settle/metrics.go:50 (`func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMetrics_ServeHTTP` for `Metrics.ServeHTTP` (internal/settle/metrics.go:50, a method on `Metrics`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMetrics_ServeHTTP` exists in `internal/settle/metrics_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/settle/...` passes with the asserted output matching exactly
4. `go doc internal/settle Metrics.ServeHTTP` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/settle/metrics.go:50 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/metrics_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleMetrics_ServeHTTP` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleServeHTTP` would silently document the package instead of `Metrics.ServeHTTP`.

**Entry point:** `internal/settle/metrics.go`

---

## 47. docs(stellar): add a runnable example for AuthorizeInvocation

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `AuthorizeInvocation` is exported from `internal/stellar` at internal/stellar/invoke.go:190 (`func AuthorizeInvocation(`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleAuthorizeInvocation` for `AuthorizeInvocation` (internal/stellar/invoke.go:190), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleAuthorizeInvocation` exists in `internal/stellar/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar AuthorizeInvocation` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/invoke.go:190 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/stellar/invoke.go yet, so create `internal/stellar/example_test.go` in package `stellar` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/invoke.go`

---

## 48. docs(stellar): add a runnable example for BuildCommitment

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `BuildCommitment` is exported from `internal/stellar` at internal/stellar/channel.go:141 (`func BuildCommitment(contractID, networkPassphrase string, amount *big.Int) ([]byte, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleBuildCommitment` for `BuildCommitment` (internal/stellar/channel.go:141), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleBuildCommitment` exists in `internal/stellar/channel_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar BuildCommitment` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/channel.go:141 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/channel_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/channel.go`

---

## 49. docs(stellar): add a runnable example for DecodeAddress

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeAddress` is exported from `internal/stellar` at internal/stellar/xdr.go:261 (`func DecodeAddress(v xdr.ScVal) (string, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeAddress` for `DecodeAddress` (internal/stellar/xdr.go:261), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeAddress` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeAddress` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:261 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 50. docs(stellar): add a runnable example for DecodeBool

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeBool` is exported from `internal/stellar` at internal/stellar/xdr.go:177 (`func DecodeBool(v xdr.ScVal) (bool, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeBool` for `DecodeBool` (internal/stellar/xdr.go:177), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeBool` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeBool` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:177 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 51. docs(stellar): add a runnable example for DecodeBytes

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeBytes` is exported from `internal/stellar` at internal/stellar/xdr.go:236 (`func DecodeBytes(v xdr.ScVal) ([]byte, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeBytes` for `DecodeBytes` (internal/stellar/xdr.go:236), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeBytes` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeBytes` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:236 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 52. docs(stellar): add a runnable example for DecodeChannelCloseEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeChannelCloseEvent` is exported from `internal/stellar` at internal/stellar/events.go:335 (`func DecodeChannelCloseEvent(valueB64 string) (*ChannelCloseEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeChannelCloseEvent` for `DecodeChannelCloseEvent` (internal/stellar/events.go:335), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeChannelCloseEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeChannelCloseEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:335 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 53. docs(stellar): add a runnable example for DecodeChannelOpenEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeChannelOpenEvent` is exported from `internal/stellar` at internal/stellar/events.go:287 (`func DecodeChannelOpenEvent(valueB64 string) (*ChannelOpenEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeChannelOpenEvent` for `DecodeChannelOpenEvent` (internal/stellar/events.go:287), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeChannelOpenEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeChannelOpenEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:287 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 54. docs(stellar): add a runnable example for DecodeChannelRefundEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeChannelRefundEvent` is exported from `internal/stellar` at internal/stellar/events.go:381 (`func DecodeChannelRefundEvent(valueB64 string) (*ChannelRefundEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeChannelRefundEvent` for `DecodeChannelRefundEvent` (internal/stellar/events.go:381), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeChannelRefundEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeChannelRefundEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:381 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 55. docs(stellar): add a runnable example for DecodeChannelWithdrawEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeChannelWithdrawEvent` is exported from `internal/stellar` at internal/stellar/events.go:356 (`func DecodeChannelWithdrawEvent(valueB64 string) (*ChannelWithdrawEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeChannelWithdrawEvent` for `DecodeChannelWithdrawEvent` (internal/stellar/events.go:356), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeChannelWithdrawEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeChannelWithdrawEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:356 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 56. docs(stellar): add a runnable example for DecodeI128

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeI128` is exported from `internal/stellar` at internal/stellar/xdr.go:218 (`func DecodeI128(v xdr.ScVal) (*big.Int, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeI128` for `DecodeI128` (internal/stellar/xdr.go:218), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeI128` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeI128` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:218 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 57. docs(stellar): add a runnable example for DecodeI32

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeI32` is exported from `internal/stellar` at internal/stellar/xdr.go:193 (`func DecodeI32(v xdr.ScVal) (int32, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeI32` for `DecodeI32` (internal/stellar/xdr.go:193), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeI32` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeI32` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:193 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 58. docs(stellar): add a runnable example for DecodeI64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeI64` is exported from `internal/stellar` at internal/stellar/xdr.go:209 (`func DecodeI64(v xdr.ScVal) (int64, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeI64` for `DecodeI64` (internal/stellar/xdr.go:209), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeI64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeI64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:209 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 59. docs(stellar): add a runnable example for DecodeMap

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeMap` is exported from `internal/stellar` at internal/stellar/xdr.go:295 (`func DecodeMap(v xdr.ScVal) (map[string]xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeMap` for `DecodeMap` (internal/stellar/xdr.go:295), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeMap` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeMap` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:295 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 60. docs(stellar): add a runnable example for DecodePriceBookPublishEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodePriceBookPublishEvent` is exported from `internal/stellar` at internal/stellar/events.go:98 (`func DecodePriceBookPublishEvent(valueB64 string) (*PriceBookPublishEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodePriceBookPublishEvent` for `DecodePriceBookPublishEvent` (internal/stellar/events.go:98), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodePriceBookPublishEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodePriceBookPublishEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:98 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 61. docs(stellar): add a runnable example for DecodeStatementAnchorEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeStatementAnchorEvent` is exported from `internal/stellar` at internal/stellar/events.go:142 (`func DecodeStatementAnchorEvent(valueB64 string) (*StatementAnchorEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeStatementAnchorEvent` for `DecodeStatementAnchorEvent` (internal/stellar/events.go:142), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeStatementAnchorEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeStatementAnchorEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:142 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 62. docs(stellar): add a runnable example for DecodeStatementDisputeEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeStatementDisputeEvent` is exported from `internal/stellar` at internal/stellar/events.go:197 (`func DecodeStatementDisputeEvent(valueB64 string) (*StatementDisputeEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeStatementDisputeEvent` for `DecodeStatementDisputeEvent` (internal/stellar/events.go:197), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeStatementDisputeEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeStatementDisputeEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:197 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 63. docs(stellar): add a runnable example for DecodeStatementResolveEvent

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeStatementResolveEvent` is exported from `internal/stellar` at internal/stellar/events.go:239 (`func DecodeStatementResolveEvent(valueB64 string) (*StatementResolveEvent, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeStatementResolveEvent` for `DecodeStatementResolveEvent` (internal/stellar/events.go:239), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeStatementResolveEvent` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeStatementResolveEvent` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:239 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 64. docs(stellar): add a runnable example for DecodeString

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeString` is exported from `internal/stellar` at internal/stellar/xdr.go:252 (`func DecodeString(v xdr.ScVal) (string, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeString` for `DecodeString` (internal/stellar/xdr.go:252), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeString` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeString` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:252 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 65. docs(stellar): add a runnable example for DecodeSymbol

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeSymbol` is exported from `internal/stellar` at internal/stellar/xdr.go:244 (`func DecodeSymbol(v xdr.ScVal) (string, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeSymbol` for `DecodeSymbol` (internal/stellar/xdr.go:244), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeSymbol` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeSymbol` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:244 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 66. docs(stellar): add a runnable example for DecodeU32

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeU32` is exported from `internal/stellar` at internal/stellar/xdr.go:185 (`func DecodeU32(v xdr.ScVal) (uint32, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeU32` for `DecodeU32` (internal/stellar/xdr.go:185), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeU32` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeU32` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:185 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 67. docs(stellar): add a runnable example for DecodeU64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeU64` is exported from `internal/stellar` at internal/stellar/xdr.go:201 (`func DecodeU64(v xdr.ScVal) (uint64, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeU64` for `DecodeU64` (internal/stellar/xdr.go:201), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeU64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeU64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:201 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 68. docs(stellar): add a runnable example for DecodeVec

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `DecodeVec` is exported from `internal/stellar` at internal/stellar/xdr.go:284 (`func DecodeVec(v xdr.ScVal) ([]xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleDecodeVec` for `DecodeVec` (internal/stellar/xdr.go:284), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleDecodeVec` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar DecodeVec` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:284 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 69. docs(stellar): add a runnable example for ErrSimulationFailed.Error

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ErrSimulationFailed.Error` is exported from `internal/stellar` at internal/stellar/invoke.go:35 (`func (e *ErrSimulationFailed) Error() string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleErrSimulationFailed_Error` for `ErrSimulationFailed.Error` (internal/stellar/invoke.go:35, a method on `ErrSimulationFailed`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleErrSimulationFailed_Error` exists in `internal/stellar/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ErrSimulationFailed.Error` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/invoke.go:35 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/stellar/invoke.go yet, so create `internal/stellar/example_test.go` in package `stellar` for it.
- The name must be exactly `ExampleErrSimulationFailed_Error` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleError` would silently document the package instead of `ErrSimulationFailed.Error`.

**Entry point:** `internal/stellar/invoke.go`

---

## 70. docs(stellar): add a runnable example for ErrTransactionFailed.Error

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ErrTransactionFailed.Error` is exported from `internal/stellar` at internal/stellar/invoke.go:46 (`func (e *ErrTransactionFailed) Error() string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleErrTransactionFailed_Error` for `ErrTransactionFailed.Error` (internal/stellar/invoke.go:46, a method on `ErrTransactionFailed`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleErrTransactionFailed_Error` exists in `internal/stellar/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ErrTransactionFailed.Error` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/invoke.go:46 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/stellar/invoke.go yet, so create `internal/stellar/example_test.go` in package `stellar` for it.
- The name must be exactly `ExampleErrTransactionFailed_Error` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleError` would silently document the package instead of `ErrTransactionFailed.Error`.

**Entry point:** `internal/stellar/invoke.go`

---

## 71. docs(stellar): add a runnable example for ErrWrongScValType.Error

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ErrWrongScValType.Error` is exported from `internal/stellar` at internal/stellar/xdr.go:25 (`func (e *ErrWrongScValType) Error() string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleErrWrongScValType_Error` for `ErrWrongScValType.Error` (internal/stellar/xdr.go:25, a method on `ErrWrongScValType`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleErrWrongScValType_Error` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ErrWrongScValType.Error` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:25 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleErrWrongScValType_Error` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleError` would silently document the package instead of `ErrWrongScValType.Error`.

**Entry point:** `internal/stellar/xdr.go`

---

## 72. docs(stellar): add a runnable example for InvokeAndSubmitWithAuth

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `InvokeAndSubmitWithAuth` is exported from `internal/stellar` at internal/stellar/invoke.go:129 (`func InvokeAndSubmitWithAuth(`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleInvokeAndSubmitWithAuth` for `InvokeAndSubmitWithAuth` (internal/stellar/invoke.go:129), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleInvokeAndSubmitWithAuth` exists in `internal/stellar/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar InvokeAndSubmitWithAuth` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/invoke.go:129 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/stellar/invoke.go yet, so create `internal/stellar/example_test.go` in package `stellar` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/invoke.go`

---

## 73. docs(stellar): add a runnable example for MapEntry

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `MapEntry` is exported from `internal/stellar` at internal/stellar/xdr.go:161 (`func MapEntry(key string, val xdr.ScVal) xdr.ScMapEntry`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMapEntry` for `MapEntry` (internal/stellar/xdr.go:161), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMapEntry` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar MapEntry` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:161 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 74. docs(stellar): add a runnable example for MapField

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `MapField` is exported from `internal/stellar` at internal/stellar/xdr.go:314 (`func MapField(m map[string]xdr.ScVal, key string) (xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMapField` for `MapField` (internal/stellar/xdr.go:314), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMapField` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar MapField` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:314 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 75. docs(stellar): add a runnable example for MarshalScValBase64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `MarshalScValBase64` is exported from `internal/stellar` at internal/stellar/xdr.go:326 (`func MarshalScValBase64(v xdr.ScVal) (string, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMarshalScValBase64` for `MarshalScValBase64` (internal/stellar/xdr.go:326), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMarshalScValBase64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar MarshalScValBase64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:326 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 76. docs(stellar): add a runnable example for MatchesTopic

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `MatchesTopic` is exported from `internal/stellar` at internal/stellar/events.go:48 (`func MatchesTopic(topicB64 []string, want ...string) (bool, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMatchesTopic` for `MatchesTopic` (internal/stellar/events.go:48), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMatchesTopic` exists in `internal/stellar/events_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar MatchesTopic` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/events.go:48 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/events_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/events.go`

---

## 77. docs(stellar): add a runnable example for RPCError.Error

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `RPCError.Error` is exported from `internal/stellar` at internal/stellar/rpc.go:72 (`func (e *RPCError) Error() string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleRPCError_Error` for `RPCError.Error` (internal/stellar/rpc.go:72, a method on `RPCError`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleRPCError_Error` exists in `internal/stellar/rpc_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar RPCError.Error` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/rpc.go:72 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/rpc_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleRPCError_Error` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleError` would silently document the package instead of `RPCError.Error`.

**Entry point:** `internal/stellar/rpc.go`

---

## 78. docs(stellar): add a runnable example for ResolveDisputeArgs

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ResolveDisputeArgs` is exported from `internal/stellar` at internal/stellar/registry.go:202 (`func ResolveDisputeArgs(operator string, seq uint64, resolutionHash [32]byte, amountCredited *big.Int) ([]xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleResolveDisputeArgs` for `ResolveDisputeArgs` (internal/stellar/registry.go:202), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleResolveDisputeArgs` exists in `internal/stellar/registry_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ResolveDisputeArgs` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/registry.go:202 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/registry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/registry.go`

---

## 79. docs(stellar): add a runnable example for ScvAddress

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvAddress` is exported from `internal/stellar` at internal/stellar/xdr.go:115 (`func ScvAddress(address string) (xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvAddress` for `ScvAddress` (internal/stellar/xdr.go:115), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvAddress` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvAddress` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:115 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 80. docs(stellar): add a runnable example for ScvBool

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvBool` is exported from `internal/stellar` at internal/stellar/xdr.go:32 (`func ScvBool(b bool) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvBool` for `ScvBool` (internal/stellar/xdr.go:32), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvBool` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvBool` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:32 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 81. docs(stellar): add a runnable example for ScvBytes

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvBytes` is exported from `internal/stellar` at internal/stellar/xdr.go:67 (`func ScvBytes(b []byte) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvBytes` for `ScvBytes` (internal/stellar/xdr.go:67), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvBytes` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvBytes` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:67 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 82. docs(stellar): add a runnable example for ScvI128

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvI128` is exported from `internal/stellar` at internal/stellar/xdr.go:88 (`func ScvI128(v *big.Int) (xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvI128` for `ScvI128` (internal/stellar/xdr.go:88), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvI128` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvI128` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:88 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 83. docs(stellar): add a runnable example for ScvI32

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvI32` is exported from `internal/stellar` at internal/stellar/xdr.go:48 (`func ScvI32(v int32) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvI32` for `ScvI32` (internal/stellar/xdr.go:48), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvI32` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvI32` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:48 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 84. docs(stellar): add a runnable example for ScvI64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvI64` is exported from `internal/stellar` at internal/stellar/xdr.go:60 (`func ScvI64(v int64) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvI64` for `ScvI64` (internal/stellar/xdr.go:60), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvI64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvI64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:60 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 85. docs(stellar): add a runnable example for ScvMap

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvMap` is exported from `internal/stellar` at internal/stellar/xdr.go:168 (`func ScvMap(entries []xdr.ScMapEntry) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvMap` for `ScvMap` (internal/stellar/xdr.go:168), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvMap` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvMap` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:168 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 86. docs(stellar): add a runnable example for ScvString

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvString` is exported from `internal/stellar` at internal/stellar/xdr.go:80 (`func ScvString(s string) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvString` for `ScvString` (internal/stellar/xdr.go:80), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvString` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvString` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:80 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 87. docs(stellar): add a runnable example for ScvSymbol

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvSymbol` is exported from `internal/stellar` at internal/stellar/xdr.go:74 (`func ScvSymbol(s string) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvSymbol` for `ScvSymbol` (internal/stellar/xdr.go:74), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvSymbol` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvSymbol` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:74 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 88. docs(stellar): add a runnable example for ScvU32

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvU32` is exported from `internal/stellar` at internal/stellar/xdr.go:42 (`func ScvU32(v uint32) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvU32` for `ScvU32` (internal/stellar/xdr.go:42), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvU32` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvU32` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:42 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 89. docs(stellar): add a runnable example for ScvU64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvU64` is exported from `internal/stellar` at internal/stellar/xdr.go:54 (`func ScvU64(v uint64) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvU64` for `ScvU64` (internal/stellar/xdr.go:54), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvU64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvU64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:54 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 90. docs(stellar): add a runnable example for ScvVec

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvVec` is exported from `internal/stellar` at internal/stellar/xdr.go:152 (`func ScvVec(items []xdr.ScVal) xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvVec` for `ScvVec` (internal/stellar/xdr.go:152), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvVec` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvVec` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:152 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 91. docs(stellar): add a runnable example for ScvVoid

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `ScvVoid` is exported from `internal/stellar` at internal/stellar/xdr.go:37 (`func ScvVoid() xdr.ScVal`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleScvVoid` for `ScvVoid` (internal/stellar/xdr.go:37), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleScvVoid` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar ScvVoid` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:37 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 92. docs(stellar): add a runnable example for StatementRegistry.Anchor

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `StatementRegistry.Anchor` is exported from `internal/stellar` at internal/stellar/registry.go:91 (`func (r *StatementRegistry) Anchor(`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleStatementRegistry_Anchor` for `StatementRegistry.Anchor` (internal/stellar/registry.go:91, a method on `StatementRegistry`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleStatementRegistry_Anchor` exists in `internal/stellar/registry_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar StatementRegistry.Anchor` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/registry.go:91 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/registry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleStatementRegistry_Anchor` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleAnchor` would silently document the package instead of `StatementRegistry.Anchor`.

**Entry point:** `internal/stellar/registry.go`

---

## 93. docs(stellar): add a runnable example for StatementRegistry.ResolveDispute

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `StatementRegistry.ResolveDispute` is exported from `internal/stellar` at internal/stellar/registry.go:242 (`func (r *StatementRegistry) ResolveDispute(`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleStatementRegistry_ResolveDispute` for `StatementRegistry.ResolveDispute` (internal/stellar/registry.go:242, a method on `StatementRegistry`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleStatementRegistry_ResolveDispute` exists in `internal/stellar/registry_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar StatementRegistry.ResolveDispute` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/registry.go:242 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/registry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleStatementRegistry_ResolveDispute` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleResolveDispute` would silently document the package instead of `StatementRegistry.ResolveDispute`.

**Entry point:** `internal/stellar/registry.go`

---

## 94. docs(stellar): add a runnable example for UnmarshalScValBase64

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `UnmarshalScValBase64` is exported from `internal/stellar` at internal/stellar/xdr.go:335 (`func UnmarshalScValBase64(s string) (xdr.ScVal, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleUnmarshalScValBase64` for `UnmarshalScValBase64` (internal/stellar/xdr.go:335), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleUnmarshalScValBase64` exists in `internal/stellar/xdr_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/stellar/...` passes with the asserted output matching exactly
4. `go doc internal/stellar UnmarshalScValBase64` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/stellar/xdr.go:335 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/xdr_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/xdr.go`

---

## 95. docs(store): add a runnable example for Migrations

`complexity: low` · 165 points · area `store` · `good first issue`

**What's wrong.** `Migrations` is exported from `internal/store` at internal/store/migrations_fs.go:18 (`func Migrations() ([]Migration, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMigrations` for `Migrations` (internal/store/migrations_fs.go:18), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMigrations` exists in `internal/store/migrations_fs_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/store/...` passes with the asserted output matching exactly
4. `go doc internal/store Migrations` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/store/migrations_fs.go:18 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/store/migrations_fs_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/store/migrations_fs.go`

---

## 96. docs(config): add a runnable example for Load

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Load` is exported from `internal/config` at internal/config/config.go:143 (`func Load(lookup Lookup) (*Config, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleLoad` for `Load` (internal/config/config.go:143), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleLoad` exists in `internal/config/config_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/config/...` passes

**Guidance**

- The declaration is at internal/config/config.go:143 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/config/config.go`

---

## 97. docs(config): add a runnable example for LoadEnv

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `LoadEnv` is exported from `internal/config` at internal/config/config.go:122 (`func LoadEnv() (*Config, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleLoadEnv` for `LoadEnv` (internal/config/config.go:122), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleLoadEnv` exists in `internal/config/config_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/config/...` passes

**Guidance**

- The declaration is at internal/config/config.go:122 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/config/config.go`

---

## 98. docs(custody): add a runnable example for NewStore

`complexity: low` · 165 points · area `custody` · `good first issue`

**What's wrong.** `NewStore` is exported from `internal/custody` at internal/custody/store.go:76 (`func NewStore(pool *pgxpool.Pool) *Store`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewStore` for `NewStore` (internal/custody/store.go:76), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewStore` exists in `internal/custody/store_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/custody/...` passes

**Guidance**

- The declaration is at internal/custody/store.go:76 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/custody/store_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/custody/store.go`

---

## 99. docs(httpapi): add a runnable example for RequestInfo.Validate

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `RequestInfo.Validate` is exported from `internal/httpapi` at internal/httpapi/metering.go:80 (`func (info RequestInfo) Validate() error`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleRequestInfo_Validate` for `RequestInfo.Validate` (internal/httpapi/metering.go:80, a method on `RequestInfo`), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleRequestInfo_Validate` exists in `internal/httpapi/metering_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:80 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleRequestInfo_Validate` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleValidate` would silently document the package instead of `RequestInfo.Validate`.

**Entry point:** `internal/httpapi/metering.go`

---

## 100. docs(indexer): add a runnable example for New

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `New` is exported from `internal/indexer` at internal/indexer/ingest.go:136 (`func New(cfg Config) (*Ingestor, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNew` for `New` (internal/indexer/ingest.go:136), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNew` exists in `internal/indexer/ingest_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/ingest.go:136 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/ingest.go`

---

## 101. docs(indexer): add a runnable example for NewReconciler

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `NewReconciler` is exported from `internal/indexer` at internal/indexer/reconcile.go:61 (`func NewReconciler(pool *pgxpool.Pool) *Reconciler`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewReconciler` for `NewReconciler` (internal/indexer/reconcile.go:61), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewReconciler` exists in `internal/indexer/reconcile_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/reconcile.go:61 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/reconcile_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/reconcile.go`

---

## 102. docs(meter): add a runnable example for NewAnchorer

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `NewAnchorer` is exported from `internal/meter` at internal/meter/anchor.go:97 (`func NewAnchorer(pool *pgxpool.Pool) *Anchorer`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewAnchorer` for `NewAnchorer` (internal/meter/anchor.go:97), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewAnchorer` exists in `internal/meter/anchor_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/anchor.go:97 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/anchor.go`

---

## 103. docs(meter): add a runnable example for NewCatalog

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `NewCatalog` is exported from `internal/meter` at internal/meter/pricing.go:55 (`func NewCatalog(entries ...CatalogVersion) (*Catalog, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewCatalog` for `NewCatalog` (internal/meter/pricing.go:55), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewCatalog` exists in `internal/meter/pricing_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/pricing.go:55 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/pricing.go`

---

## 104. docs(meter): add a runnable example for NewPeriodCloser

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `NewPeriodCloser` is exported from `internal/meter` at internal/meter/period.go:47 (`func NewPeriodCloser(pool *pgxpool.Pool) *PeriodCloser`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewPeriodCloser` for `NewPeriodCloser` (internal/meter/period.go:47), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewPeriodCloser` exists in `internal/meter/period_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/period.go:47 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/period_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/period.go`

---

## 105. docs(meter): add a runnable example for NewPricer

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `NewPricer` is exported from `internal/meter` at internal/meter/pricing.go:139 (`func NewPricer(catalog *Catalog) *Pricer`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewPricer` for `NewPricer` (internal/meter/pricing.go:139), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewPricer` exists in `internal/meter/pricing_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/pricing.go:139 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/pricing.go`

---

## 106. docs(meter): add a runnable example for ParseSchedule

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `ParseSchedule` is exported from `internal/meter` at internal/meter/schedule.go:81 (`func ParseSchedule(raw []byte) (*Schedule, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleParseSchedule` for `ParseSchedule` (internal/meter/schedule.go:81), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleParseSchedule` exists in `internal/meter/schedule_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/schedule.go:81 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/schedule_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/schedule.go`

---

## 107. docs(settle): add a runnable example for NewCalculator

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `NewCalculator` is exported from `internal/settle` at internal/settle/exposure.go:66 (`func NewCalculator(pool *pgxpool.Pool) *Calculator`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewCalculator` for `NewCalculator` (internal/settle/exposure.go:66), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewCalculator` exists in `internal/settle/exposure_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/settle/...` passes

**Guidance**

- The declaration is at internal/settle/exposure.go:66 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/exposure_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/exposure.go`

---

## 108. docs(settle): add a runnable example for NewDaemon

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `NewDaemon` is exported from `internal/settle` at internal/settle/daemon.go:72 (`func NewDaemon(pool *pgxpool.Pool, calc *Calculator, submitter *Submitter, channelFactory ChannelFactory, signer *keypair.Full, ledgers LedgerSource, policy Policy) *Daemon`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewDaemon` for `NewDaemon` (internal/settle/daemon.go:72), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewDaemon` exists in `internal/settle/daemon_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/settle/...` passes

**Guidance**

- The declaration is at internal/settle/daemon.go:72 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/daemon_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/daemon.go`

---

## 109. docs(settle): add a runnable example for NewMetrics

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `NewMetrics` is exported from `internal/settle` at internal/settle/metrics.go:41 (`func NewMetrics(pool *pgxpool.Pool, ledgers LedgerSource, daemon *Daemon) *Metrics`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewMetrics` for `NewMetrics` (internal/settle/metrics.go:41), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewMetrics` exists in `internal/settle/metrics_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/settle/...` passes

**Guidance**

- The declaration is at internal/settle/metrics.go:41 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/metrics_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/metrics.go`

---

## 110. docs(settle): add a runnable example for NewSubmitter

`complexity: low` · 165 points · area `settle` · `good first issue`

**What's wrong.** `NewSubmitter` is exported from `internal/settle` at internal/settle/submit.go:69 (`func NewSubmitter(pool *pgxpool.Pool) *Submitter`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewSubmitter` for `NewSubmitter` (internal/settle/submit.go:69), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewSubmitter` exists in `internal/settle/submit_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/settle/...` passes

**Guidance**

- The declaration is at internal/settle/submit.go:69 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/settle/submit_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/settle/submit.go`

---

## 111. docs(stellar): add a runnable example for NewChannel

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `NewChannel` is exported from `internal/stellar` at internal/stellar/channel.go:56 (`func NewChannel(client *Client, contractID, networkPassphrase string) *Channel`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewChannel` for `NewChannel` (internal/stellar/channel.go:56), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewChannel` exists in `internal/stellar/channel_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/stellar/...` passes

**Guidance**

- The declaration is at internal/stellar/channel.go:56 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/channel_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/channel.go`

---

## 112. docs(stellar): add a runnable example for NewClient

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `NewClient` is exported from `internal/stellar` at internal/stellar/rpc.go:37 (`func NewClient(url string, httpClient *http.Client) *Client`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewClient` for `NewClient` (internal/stellar/rpc.go:37), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewClient` exists in `internal/stellar/rpc_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/stellar/...` passes

**Guidance**

- The declaration is at internal/stellar/rpc.go:37 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/rpc_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/rpc.go`

---

## 113. docs(stellar): add a runnable example for NewPriceBook

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `NewPriceBook` is exported from `internal/stellar` at internal/stellar/pricebook.go:49 (`func NewPriceBook(client *Client, contractID, networkPassphrase string) *PriceBook`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewPriceBook` for `NewPriceBook` (internal/stellar/pricebook.go:49), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewPriceBook` exists in `internal/stellar/pricebook_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/stellar/...` passes

**Guidance**

- The declaration is at internal/stellar/pricebook.go:49 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/pricebook_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/pricebook.go`

---

## 114. docs(stellar): add a runnable example for NewStatementRegistry

`complexity: low` · 165 points · area `stellar` · `good first issue`

**What's wrong.** `NewStatementRegistry` is exported from `internal/stellar` at internal/stellar/registry.go:85 (`func NewStatementRegistry(client *Client, contractID, networkPassphrase string) *StatementRegistry`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewStatementRegistry` for `NewStatementRegistry` (internal/stellar/registry.go:85), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewStatementRegistry` exists in `internal/stellar/registry_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/stellar/...` passes

**Guidance**

- The declaration is at internal/stellar/registry.go:85 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/stellar/registry_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/stellar/registry.go`

---

## 115. docs(store): add a runnable example for NewRequestStore

`complexity: low` · 165 points · area `store` · `good first issue`

**What's wrong.** `NewRequestStore` is exported from `internal/store` at internal/store/requests.go:23 (`func NewRequestStore(pool *pgxpool.Pool) *RequestStore`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewRequestStore` for `NewRequestStore` (internal/store/requests.go:23), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewRequestStore` exists in `internal/store/requests_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/store/...` passes

**Guidance**

- The declaration is at internal/store/requests.go:23 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/store/requests_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/store/requests.go`

---

## 116. docs(store): add a runnable example for ParseMigrations

`complexity: low` · 165 points · area `store` · `good first issue`

**What's wrong.** `ParseMigrations` is exported from `internal/store` at internal/store/migrate.go:39 (`func ParseMigrations(fsys fs.FS) ([]Migration, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleParseMigrations` for `ParseMigrations` (internal/store/migrate.go:39), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleParseMigrations` exists in `internal/store/migrate_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/store/...` passes

**Guidance**

- The declaration is at internal/store/migrate.go:39 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/store/migrate_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/store/migrate.go`

---

## 117. docs(config): add a runnable example for Config

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Config` is exported from `internal/config` at internal/config/config.go:58 (`type Config struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleConfig` for `Config` (internal/config/config.go:58), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleConfig` exists in `internal/config/config_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/config/...` passes

**Guidance**

- The declaration is at internal/config/config.go:58 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/config/config.go`

---

## 118. docs(config): add a runnable example for Lookup

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Lookup` is exported from `internal/config` at internal/config/config.go:119 (`type Lookup func(key string) (string, bool)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleLookup` for `Lookup` (internal/config/config.go:119), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleLookup` exists in `internal/config/config_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/config/...` passes

**Guidance**

- The declaration is at internal/config/config.go:119 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/config/config_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/config/config.go`

---

## 119. docs(config): add a runnable example for Secret

`complexity: low` · 165 points · area `config` · `good first issue`

**What's wrong.** `Secret` is exported from `internal/config` at internal/config/secret.go:8 (`type Secret string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleSecret` for `Secret` (internal/config/secret.go:8), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleSecret` exists in `internal/config/example_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/config/...` passes

**Guidance**

- The declaration is at internal/config/secret.go:8 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/config/secret.go yet, so create `internal/config/example_test.go` in package `config` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/config/secret.go`

---

## 120. docs(custody): add a runnable example for ChannelReader

`complexity: low` · 165 points · area `custody` · `good first issue`

**What's wrong.** `ChannelReader` is exported from `internal/custody` at internal/custody/verify.go:34 (`type ChannelReader interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleChannelReader` for `ChannelReader` (internal/custody/verify.go:34), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleChannelReader` exists in `internal/custody/verify_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/custody/...` passes

**Guidance**

- The declaration is at internal/custody/verify.go:34 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/custody/verify_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/custody/verify.go`

---

## 121. docs(custody): add a runnable example for Record

`complexity: low` · 165 points · area `custody` · `good first issue`

**What's wrong.** `Record` is exported from `internal/custody` at internal/custody/store.go:56 (`type Record struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleRecord` for `Record` (internal/custody/store.go:56), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleRecord` exists in `internal/custody/store_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/custody/...` passes

**Guidance**

- The declaration is at internal/custody/store.go:56 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/custody/store_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/custody/store.go`

---

## 122. docs(custody): add a runnable example for Store

`complexity: low` · 165 points · area `custody` · `good first issue`

**What's wrong.** `Store` is exported from `internal/custody` at internal/custody/store.go:71 (`type Store struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleStore` for `Store` (internal/custody/store.go:71), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleStore` exists in `internal/custody/store_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/custody/...` passes

**Guidance**

- The declaration is at internal/custody/store.go:71 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/custody/store_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/custody/store.go`

---

## 123. docs(custody): add a runnable example for SubmittedCommitment

`complexity: low` · 165 points · area `custody` · `good first issue`

**What's wrong.** `SubmittedCommitment` is exported from `internal/custody` at internal/custody/store.go:40 (`type SubmittedCommitment struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleSubmittedCommitment` for `SubmittedCommitment` (internal/custody/store.go:40), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleSubmittedCommitment` exists in `internal/custody/store_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/custody/...` passes

**Guidance**

- The declaration is at internal/custody/store.go:40 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/custody/store_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/custody/store.go`

---

## 124. docs(httpapi): add a runnable example for Config

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `Config` is exported from `internal/httpapi` at internal/httpapi/metering.go:114 (`type Config struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleConfig` for `Config` (internal/httpapi/metering.go:114), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleConfig` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:114 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 125. docs(httpapi): add a runnable example for LedgerSource

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `LedgerSource` is exported from `internal/httpapi` at internal/httpapi/metering.go:36 (`type LedgerSource interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleLedgerSource` for `LedgerSource` (internal/httpapi/metering.go:36), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleLedgerSource` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:36 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 126. docs(httpapi): add a runnable example for RecordedRequest

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `RecordedRequest` is exported from `internal/httpapi` at internal/httpapi/metering.go:49 (`type RecordedRequest struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleRecordedRequest` for `RecordedRequest` (internal/httpapi/metering.go:49), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleRecordedRequest` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:49 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 127. docs(httpapi): add a runnable example for Recorder

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `Recorder` is exported from `internal/httpapi` at internal/httpapi/metering.go:43 (`type Recorder interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleRecorder` for `Recorder` (internal/httpapi/metering.go:43), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleRecorder` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:43 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 128. docs(httpapi): add a runnable example for RequestInfo

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `RequestInfo` is exported from `internal/httpapi` at internal/httpapi/metering.go:68 (`type RequestInfo struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleRequestInfo` for `RequestInfo` (internal/httpapi/metering.go:68), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleRequestInfo` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:68 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 129. docs(indexer): add a runnable example for Config

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Config` is exported from `internal/indexer` at internal/indexer/ingest.go:80 (`type Config struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleConfig` for `Config` (internal/indexer/ingest.go:80), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleConfig` exists in `internal/indexer/ingest_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/ingest.go:80 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/ingest.go`

---

## 130. docs(indexer): add a runnable example for EventsSource

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `EventsSource` is exported from `internal/indexer` at internal/indexer/ingest.go:70 (`type EventsSource interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleEventsSource` for `EventsSource` (internal/indexer/ingest.go:70), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleEventsSource` exists in `internal/indexer/ingest_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/ingest.go:70 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/ingest.go`

---

## 131. docs(indexer): add a runnable example for Ingestor

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Ingestor` is exported from `internal/indexer` at internal/indexer/ingest.go:122 (`type Ingestor struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleIngestor` for `Ingestor` (internal/indexer/ingest.go:122), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleIngestor` exists in `internal/indexer/ingest_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/ingest.go:122 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/ingest.go`

---

## 132. docs(indexer): add a runnable example for Kind

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Kind` is exported from `internal/indexer` at internal/indexer/ingest.go:45 (`type Kind string`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleKind` for `Kind` (internal/indexer/ingest.go:45), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleKind` exists in `internal/indexer/ingest_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/ingest.go:45 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/ingest.go`

---

## 133. docs(indexer): add a runnable example for Reconciler

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Reconciler` is exported from `internal/indexer` at internal/indexer/reconcile.go:56 (`type Reconciler struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleReconciler` for `Reconciler` (internal/indexer/reconcile.go:56), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleReconciler` exists in `internal/indexer/reconcile_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/reconcile.go:56 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/reconcile_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/reconcile.go`

---

## 134. docs(indexer): add a runnable example for Result

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Result` is exported from `internal/indexer` at internal/indexer/reconcile.go:41 (`type Result struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleResult` for `Result` (internal/indexer/reconcile.go:41), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleResult` exists in `internal/indexer/reconcile_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/reconcile.go:41 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/reconcile_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/reconcile.go`

---

## 135. docs(merkle): add a runnable example for Proof

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `Proof` is exported from `internal/merkle` at internal/merkle/tree.go:21 (`type Proof [][32]byte`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleProof` for `Proof` (internal/merkle/tree.go:21), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleProof` exists in `internal/merkle/tree_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/merkle/...` passes

**Guidance**

- The declaration is at internal/merkle/tree.go:21 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/tree_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/tree.go`

---

## 136. docs(merkle): add a runnable example for Record

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `Record` is exported from `internal/merkle` at internal/merkle/leaf.go:26 (`type Record struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleRecord` for `Record` (internal/merkle/leaf.go:26), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleRecord` exists in `internal/merkle/leaf_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/merkle/...` passes

**Guidance**

- The declaration is at internal/merkle/leaf.go:26 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/leaf_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/leaf.go`

---

## 137. docs(merkle): add a runnable example for Tree

`complexity: low` · 165 points · area `merkle` · `good first issue`

**What's wrong.** `Tree` is exported from `internal/merkle` at internal/merkle/tree.go:29 (`type Tree struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleTree` for `Tree` (internal/merkle/tree.go:29), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleTree` exists in `internal/merkle/tree_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/merkle/...` passes

**Guidance**

- The declaration is at internal/merkle/tree.go:29 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/merkle/tree_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/merkle/tree.go`

---

## 138. docs(meter): add a runnable example for AnchorResult

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `AnchorResult` is exported from `internal/meter` at internal/meter/anchor.go:328 (`type AnchorResult struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleAnchorResult` for `AnchorResult` (internal/meter/anchor.go:328), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleAnchorResult` exists in `internal/meter/anchor_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/anchor.go:328 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/anchor.go`

---

## 139. docs(meter): add a runnable example for AnchorSubmitter

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `AnchorSubmitter` is exported from `internal/meter` at internal/meter/anchor.go:73 (`type AnchorSubmitter interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleAnchorSubmitter` for `AnchorSubmitter` (internal/meter/anchor.go:73), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleAnchorSubmitter` exists in `internal/meter/anchor_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/anchor.go:73 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/anchor.go`

---

## 140. docs(meter): add a runnable example for Anchorer

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Anchorer` is exported from `internal/meter` at internal/meter/anchor.go:92 (`type Anchorer struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleAnchorer` for `Anchorer` (internal/meter/anchor.go:92), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleAnchorer` exists in `internal/meter/anchor_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/anchor.go:92 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/anchor.go`

---

## 141. docs(meter): add a runnable example for Catalog

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Catalog` is exported from `internal/meter` at internal/meter/pricing.go:47 (`type Catalog struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleCatalog` for `Catalog` (internal/meter/pricing.go:47), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleCatalog` exists in `internal/meter/pricing_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/pricing.go:47 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/pricing.go`

---

## 142. docs(meter): add a runnable example for CatalogSource

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `CatalogSource` is exported from `internal/meter` at internal/meter/period.go:225 (`type CatalogSource interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleCatalogSource` for `CatalogSource` (internal/meter/period.go:225), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleCatalogSource` exists in `internal/meter/period_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/period.go:225 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/period_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/period.go`

---

## 143. docs(meter): add a runnable example for CatalogVersion

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `CatalogVersion` is exported from `internal/meter` at internal/meter/pricing.go:36 (`type CatalogVersion struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleCatalogVersion` for `CatalogVersion` (internal/meter/pricing.go:36), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleCatalogVersion` exists in `internal/meter/pricing_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/pricing.go:36 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/pricing.go`

---

## 144. docs(meter): add a runnable example for LedgerSource

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `LedgerSource` is exported from `internal/meter` at internal/meter/period.go:234 (`type LedgerSource interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleLedgerSource` for `LedgerSource` (internal/meter/period.go:234), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleLedgerSource` exists in `internal/meter/period_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/period.go:234 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/period_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/period.go`

---

## 145. docs(meter): add a runnable example for Period

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Period` is exported from `internal/meter` at internal/meter/period.go:27 (`type Period struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExamplePeriod` for `Period` (internal/meter/period.go:27), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExamplePeriod` exists in `internal/meter/period_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/meter/...` passes

**Guidance**

- The declaration is at internal/meter/period.go:27 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/period_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/meter/period.go`

---

## 146. docs(convert): add a runnable example for NumericToBigInt

`complexity: low` · 165 points · area `convert` · `good first issue`

**What's wrong.** `NumericToBigInt` is exported from `internal/convert` at internal/convert/numeric.go:23 (`func NumericToBigInt(n pgtype.Numeric) (*big.Int, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleNumericToBigInt` for `NumericToBigInt` (internal/convert/numeric.go:23), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleNumericToBigInt` exists in `internal/convert/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/convert/...` passes with the asserted output matching exactly
4. `go doc internal/convert NumericToBigInt` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/convert/numeric.go:23 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/convert/numeric.go yet, so create `internal/convert/example_test.go` in package `convert` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/convert/numeric.go`

---

## 147. docs(indexer): add a runnable example for Ingestor.ConsecutiveTickFailures

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Ingestor.ConsecutiveTickFailures` is exported from `internal/indexer` at internal/indexer/ingest.go:151 (`func (ix *Ingestor) ConsecutiveTickFailures() uint64`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleIngestor_ConsecutiveTickFailures` for `Ingestor.ConsecutiveTickFailures` (internal/indexer/ingest.go:151, a method on `Ingestor`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleIngestor_ConsecutiveTickFailures` exists in `internal/indexer/ingest_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/indexer/...` passes with the asserted output matching exactly
4. `go doc internal/indexer Ingestor.ConsecutiveTickFailures` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/indexer/ingest.go:151 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/indexer/ingest_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleIngestor_ConsecutiveTickFailures` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleConsecutiveTickFailures` would silently document the package instead of `Ingestor.ConsecutiveTickFailures`.

**Entry point:** `internal/indexer/ingest.go`

---

## 148. docs(indexer): add a runnable example for Metrics.ServeHTTP

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `Metrics.ServeHTTP` is exported from `internal/indexer` at internal/indexer/metrics.go:22 (`func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleMetrics_ServeHTTP` for `Metrics.ServeHTTP` (internal/indexer/metrics.go:22, a method on `Metrics`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleMetrics_ServeHTTP` exists in `internal/indexer/example_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/indexer/...` passes with the asserted output matching exactly
4. `go doc internal/indexer Metrics.ServeHTTP` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/indexer/metrics.go:22 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/indexer/metrics.go yet, so create `internal/indexer/example_test.go` in package `indexer` for it.
- The name must be exactly `ExampleMetrics_ServeHTTP` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleServeHTTP` would silently document the package instead of `Metrics.ServeHTTP`.

**Entry point:** `internal/indexer/metrics.go`

---

## 149. docs(meter): add a runnable example for Catalog.Price

`complexity: low` · 165 points · area `meter` · `good first issue`

**What's wrong.** `Catalog.Price` is exported from `internal/meter` at internal/meter/pricing.go:144 (`func (c *Catalog) Price(ledger uint32, method, pathTemplate string, units uint64) (*PricedRequest, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleCatalog_Price` for `Catalog.Price` (internal/meter/pricing.go:144, a method on `Catalog`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleCatalog_Price` exists in `internal/meter/pricing_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/meter/...` passes with the asserted output matching exactly
4. `go doc internal/meter Catalog.Price` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/meter/pricing.go:144 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/meter/pricing_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleCatalog_Price` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExamplePrice` would silently document the package instead of `Catalog.Price`.

**Entry point:** `internal/meter/pricing.go`

---

## 150. docs(pricebook): add a runnable example for Source.TTL

`complexity: low` · 165 points · area `pricebook` · `good first issue`

**What's wrong.** `Source.TTL` is exported from `internal/pricebook` at internal/pricebook/pricebook.go:175 (`func (s *Source) TTL() time.Duration`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is deterministic, so the example can assert its exact output and will fail the build the moment the behaviour changes — which is what makes an example worth more here than another line of prose.

**Proposed fix.** Add `ExampleSource_TTL` for `Source.TTL` (internal/pricebook/pricebook.go:175, a method on `Source`), which calls it with a concrete literal input and asserts the result through an `// Output:` comment, so `go test` executes the example and it cannot silently rot.

**Acceptance criteria**

1. `ExampleSource_TTL` exists in `internal/pricebook/pricebook_test.go` and has an `// Output:` comment, so `go test` executes and checks it
2. The example passes a concrete literal input rather than calling a helper that hides what is being passed
3. `go test ./internal/pricebook/...` passes with the asserted output matching exactly
4. `go doc internal/pricebook Source.TTL` shows the example attached to this identifier, not to the package

**Guidance**

- The declaration is at internal/pricebook/pricebook.go:175 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/pricebook/pricebook_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- The name must be exactly `ExampleSource_TTL` — Go attaches an example to a method only via the `ExampleType_Method` form. Naming it `ExampleTTL` would silently document the package instead of `Source.TTL`.

**Entry point:** `internal/pricebook/pricebook.go`

---

## 151. docs(anchor): add a runnable example for NewDriver

`complexity: low` · 165 points · area `anchor` · `good first issue`

**What's wrong.** `NewDriver` is exported from `internal/anchor` at internal/anchor/anchor.go:102 (`func NewDriver(cfg Config) (*Driver, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewDriver` for `NewDriver` (internal/anchor/anchor.go:102), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewDriver` exists in `internal/anchor/anchor_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/anchor/...` passes

**Guidance**

- The declaration is at internal/anchor/anchor.go:102 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/anchor/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/anchor/anchor.go`

---

## 152. docs(httpserver): add a runnable example for New

`complexity: low` · 165 points · area `httpserver` · `good first issue`

**What's wrong.** `New` is exported from `internal/httpserver` at internal/httpserver/server.go:20 (`func New(addr string, handler http.Handler, timeouts Timeouts) (*http.Server, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNew` for `New` (internal/httpserver/server.go:20), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNew` exists in `internal/httpserver/server_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/httpserver/...` passes

**Guidance**

- The declaration is at internal/httpserver/server.go:20 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpserver/server_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpserver/server.go`

---

## 153. docs(indexer): add a runnable example for NewMetrics

`complexity: low` · 165 points · area `indexer` · `good first issue`

**What's wrong.** `NewMetrics` is exported from `internal/indexer` at internal/indexer/metrics.go:16 (`func NewMetrics(ingestor *Ingestor) *Metrics`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewMetrics` for `NewMetrics` (internal/indexer/metrics.go:16), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewMetrics` exists in `internal/indexer/example_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/indexer/...` passes

**Guidance**

- The declaration is at internal/indexer/metrics.go:16 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/indexer/metrics.go yet, so create `internal/indexer/example_test.go` in package `indexer` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/indexer/metrics.go`

---

## 154. docs(pricebook): add a runnable example for NewHTTPFetcher

`complexity: low` · 165 points · area `pricebook` · `good first issue`

**What's wrong.** `NewHTTPFetcher` is exported from `internal/pricebook` at internal/pricebook/fetch.go:35 (`func NewHTTPFetcher(client *http.Client) *HTTPFetcher`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewHTTPFetcher` for `NewHTTPFetcher` (internal/pricebook/fetch.go:35), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewHTTPFetcher` exists in `internal/pricebook/example_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/pricebook/...` passes

**Guidance**

- The declaration is at internal/pricebook/fetch.go:35 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- There is no `_test.go` beside internal/pricebook/fetch.go yet, so create `internal/pricebook/example_test.go` in package `pricebook` for it.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/pricebook/fetch.go`

---

## 155. docs(pricebook): add a runnable example for NewSource

`complexity: low` · 165 points · area `pricebook` · `good first issue`

**What's wrong.** `NewSource` is exported from `internal/pricebook` at internal/pricebook/pricebook.go:149 (`func NewSource(cfg Config) (*Source, error)`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is a parse or construction entry point, so the valuable half of the example is the rejection: showing which sentinel a malformed input produces documents the contract far better than the signature does.

**Proposed fix.** Add `ExampleNewSource` for `NewSource` (internal/pricebook/pricebook.go:149), which shows one accepted input and one rejected input, with the rejection's error identified by the sentinel it matches.

**Acceptance criteria**

1. `ExampleNewSource` exists in `internal/pricebook/pricebook_test.go` and covers one accepted and one rejected input
2. The rejected case identifies the sentinel via `errors.Is`, not by comparing an error string
3. The example has an `// Output:` comment so `go test` executes and checks it
4. `go test ./internal/pricebook/...` passes

**Guidance**

- The declaration is at internal/pricebook/pricebook.go:149 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/pricebook/pricebook_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/pricebook/pricebook.go`

---

## 156. docs(anchor): add a runnable example for Config

`complexity: low` · 165 points · area `anchor` · `good first issue`

**What's wrong.** `Config` is exported from `internal/anchor` at internal/anchor/anchor.go:63 (`type Config struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleConfig` for `Config` (internal/anchor/anchor.go:63), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleConfig` exists in `internal/anchor/anchor_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/anchor/...` passes

**Guidance**

- The declaration is at internal/anchor/anchor.go:63 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/anchor/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/anchor/anchor.go`

---

## 157. docs(anchor): add a runnable example for Driver

`complexity: low` · 165 points · area `anchor` · `good first issue`

**What's wrong.** `Driver` is exported from `internal/anchor` at internal/anchor/anchor.go:90 (`type Driver struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleDriver` for `Driver` (internal/anchor/anchor.go:90), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleDriver` exists in `internal/anchor/anchor_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/anchor/...` passes

**Guidance**

- The declaration is at internal/anchor/anchor.go:90 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/anchor/anchor_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/anchor/anchor.go`

---

## 158. docs(httpapi): add a runnable example for CatalogSource

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `CatalogSource` is exported from `internal/httpapi` at internal/httpapi/metering.go:53 (`type CatalogSource interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleCatalogSource` for `CatalogSource` (internal/httpapi/metering.go:53), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleCatalogSource` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:53 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 159. docs(httpapi): add a runnable example for PeriodOpener

`complexity: low` · 165 points · area `httpapi` · `good first issue`

**What's wrong.** `PeriodOpener` is exported from `internal/httpapi` at internal/httpapi/metering.go:67 (`type PeriodOpener interface`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExamplePeriodOpener` for `PeriodOpener` (internal/httpapi/metering.go:67), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExamplePeriodOpener` exists in `internal/httpapi/metering_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpapi/...` passes

**Guidance**

- The declaration is at internal/httpapi/metering.go:67 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpapi/metering_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpapi/metering.go`

---

## 160. docs(httpserver): add a runnable example for Timeouts

`complexity: low` · 165 points · area `httpserver` · `good first issue`

**What's wrong.** `Timeouts` is exported from `internal/httpserver` at internal/httpserver/server.go:12 (`type Timeouts struct`) and has no example. The repository has **zero** `Example` functions across all 239 exported identifiers, so `go doc` shows signatures and prose but never one executable call. It is an exported type with no example, so nothing in the package documentation shows which fields a caller is expected to set versus which the package fills in. That is exactly the question an example answers and a doc comment usually does not.

**Proposed fix.** Add `ExampleTimeouts` for `Timeouts` (internal/httpserver/server.go:12), which constructs it and uses it the way a caller actually would, so the difference between the zero value and a properly-constructed one is visible.

**Acceptance criteria**

1. `ExampleTimeouts` exists in `internal/httpserver/server_test.go` and constructs the type the way a caller would
2. The example sets every field a caller is required to set, and leaves the ones the package fills in alone
3. The example either asserts an `// Output:` comment, or carries a comment saying why output is not asserted
4. `go test ./internal/httpserver/...` passes

**Guidance**

- The declaration is at internal/httpserver/server.go:12 — read its doc comment first. This repository uses doc comments to record design decisions, and the example should agree with what is there rather than restate it.
- `internal/httpserver/server_test.go` already tests this file — add the example there, or in a sibling `example_test.go` in the same package. `go test` only runs `Example*` functions that live alongside the package's other tests.
- An example whose `// Output:` comment does not match is a build failure. That is the point — do not delete the comment to make it pass.

**Entry point:** `internal/httpserver/server.go`

---

