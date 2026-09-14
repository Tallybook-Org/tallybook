# Maintainer Cadence — `tallybook`

A standing brief for the coding agent. This is not a build sequence with an end; it is how
this repository is maintained once the initial build (`CLAUDE.md`'s 57-step sequence) is
done. Structure and reasoning are adapted from `tallybook-contracts`' own
[`MAINTAINER.md`](https://github.com/Tallybook-Org/tallybook-contracts/blob/main/MAINTAINER.md) —
read that document too where this one says to; it is not duplicated here so the two can't
drift apart.

Read this before any maintenance session. Everything in `CLAUDE.md` still applies — the
on-chain interfaces, coding standards, and git rules are unchanged.

---

## 1. The point of this document

The repository has to stay genuinely active, and the activity has to be real. A padded
issue backlog or a commit log full of reformatting is visible to anyone who looks, and it
discounts everything else in the repo along with it. The bar for every action taken under
this brief is the same one `tallybook-contracts` uses:

> Would this action still be worth taking if nobody were watching the repo?

If no, do not take it.

---

## 2. Hard rules — never violate these

**Never close an issue a contributor could take.** The open backlog is the reason a
contributor would show up at all; closing it yourself to generate activity destroys the
thing that attracts them. This is not a closed list — it covers every open issue that isn't
in §3 — but the ones worth naming explicitly, because closing them would be an easy mistake
to justify in the moment:

- #2 and #3 — the two `stellar-cli` upstream-tracking issues. These stay open until the
  upstream bug they document is actually fixed upstream, not until this repo works around
  it.
- #5 and #6 — the production-wiring issues (`cmd/collector` never calling
  `meter.PeriodCloser`/`meter.Anchorer` or mounting a real route; the settler/indexer's
  hardcoded ports and intervals). Both are "the binary runs, but an operator can't actually
  run it for real yet" issues — exactly the kind of scoped, well-understood gap a strong
  contributor picks up first.
- #7 and #8 — the resilience/observability pair (the live Withdraw-decode wedge, and the
  fact that nothing makes that wedge visible from outside). Same reasoning: real,
  reproducible, not filler.
- Anything labelled `good first issue` or `help wanted`, once issues carry those labels.

**Never batch-create issues.** An issue exists because a real problem was observed this
session. A session that finds nothing files nothing — that's a correct, reportable outcome,
not a gap to fill.

**Never manufacture commits.** No reformatting for its own sake, no rewording comments that
were already correct, no dependency bumps without a reason, no whitespace churn. `git log`
should read as work.

**Never inflate complexity labels.** Complexity is a real estimate of scope, not a lever.

**Never change an on-chain contract interface** (`CLAUDE.md` §4) or restate its logic in
this repo. Those interfaces are deployed and immutable, owned by `tallybook-contracts` (or,
for `one-way-channel`, by neither repo — see §4 below).

---

## 3. What the maintainer *is* allowed to close

Work that genuinely isn't contributor-suitable because it needs credentials, repo admin
rights, or judgement a newcomer can't supply:

- CI workflow configuration and secrets.
- Branch protection and repo settings.
- Local-dev infrastructure that's blocking everyone, not a design decision (e.g. the
  `docker-compose.yml` host-port remap in `474613b` — an unrelated container on this
  machine held port 8080; that's an environment fact, not something to leave for a
  contributor to rediscover).
- Anything requiring a funded or live testnet operator key, or a real on-chain call only the
  maintainer can authorize (`TB_OPERATOR_SECRET`, §7).
- Reviewing and merging contributor PRs — the main job, once contributors arrive.
- Security fixes with a disclosed advisory.

If something outside this list needs doing urgently, do it, but say in the commit message
why it couldn't wait for a contributor.

---

## 4. Where real issues actually come from

Check these on the cadence in §5. File only what they actually surface.

### Upstream version movement — mostly shared with `tallybook-contracts`
`stellar-cli` upstream tracking is the same watch `tallybook-contracts`' own §4 already
describes (issues #2 and #3 here mirror the same underlying bugs that repo tracks against
its own CLI-driven workflows) — check it there, don't re-derive it here. This repo has one
version watch of its own that `tallybook-contracts` doesn't: `github.com/stellar/go-stellar-sdk`
(`go.mod`) — the Go analog of that repo's `soroban-sdk` pin-watch, same reasoning, different
ecosystem. Check its latest tag against the pinned version; file an issue only if a release
changes an API shape this repo actually calls (`internal/stellar`).

### The contracts this repo consumes, moving out from under it
- If `tallybook-contracts` redeploys `price_book` or `statement_registry` (a testnet reset,
  or a real upgrade), `TB_PRICE_BOOK_ID` / `TB_STATEMENT_REGISTRY_ID` in every doc and
  `.env.example` here go stale at once, same as that repo's own README references do on its
  side. Cross-check the two repos' README contract-ID tables match before assuming either
  one is current.
- **`stellar-experimental/one-way-channel` changing.** It's external and unaudited (§4 of
  `CLAUDE.md`, `SECURITY.md`) — nobody on this project controls its release cadence, and
  `internal/stellar/channel.go`'s bindings would break **silently**, not at compile time,
  if its function signatures or event shapes moved: Soroban contract calls aren't
  type-checked against the deployed wasm at build time. Watch its repository for commits;
  if its interface has moved, that's a real, urgent issue here regardless of whether
  anything downstream has noticed yet.
- **A new on-chain event shape the decoder hasn't seen.** Issue #7 is the concrete instance:
  a live `Withdraw` event carried 3 ScVal fields where `internal/stellar/events.go` assumed
  2. This is a real, recurring risk class for every `one-way-channel` event this repo
  decodes (`Open`, `Close`, `Withdraw`, `Refund`) — the contract is unaudited and external,
  so nothing guarantees the assumed shape stays fixed. Treat any decode failure surfaced in
  production logs (or by the weekly indexer-cursor check below) as a candidate for exactly
  this class of issue before assuming it's transient.

### Documentation drift
- A function signature, table, or env var in `README.md` that no longer matches
  `internal/config` or `CLAUDE.md` §7.
- A command in `docs/operator-runbook.md` that no longer runs as written — re-run it, don't
  eyeball it, the same discipline `tallybook-contracts`' own docs upkeep uses.
- An external link (to `tallybook-contracts`, to `one-way-channel`, to Soroban RPC docs)
  that has started 404ing.

### CI
- Any red build on `main` — fix it the same session.
- Action versions going stale, or Dependabot/advisory alerts.

### Once contributors exist
Reviewing PRs against the collector wiring work (#5, #6) and the resilience work (#7, #8)
is the best use of maintainer time at that point — these are exactly the issues §2 says to
never close yourself, so once someone else is working one, the maintainer's job on it is
review, not implementation.

---

## 5. The cadence

### Every session, before anything else
1. `git pull`.
2. Check for open contributor PRs — reviewing them takes priority over everything below.
3. Check CI (`build-vet-staticcheck-test`, `.github/workflows/ci.yml`) is green on `main`.
4. Check for new issues opened by other people.

### Weekly (about 30 minutes)

1. **Full local check, from a clean state** — the Go equivalent of
   `tallybook-contracts`' `make fmt-check && make build && make clippy && make test`:

   ```sh
   make up                    # postgres only — what the test suite needs
   go build ./...
   go vet ./...
   staticcheck ./...
   go test ./... -count=1     # -count=1 defeats the test cache: every run hits real postgres
   ```

2. **No `mdbook build` step — this repo has no docs site.** `tallybook-contracts` has one
   (`docs/`, built with `mdbook`); this repo's only prose documentation is `README.md`,
   `CONTRIBUTING.md`, `SECURITY.md`, `docs/operator-runbook.md`, and `CLAUDE.md` itself —
   plain files, nothing to build. Noting that explicitly here rather than carrying a
   `mdbook` step over blind, or leaving behind a step that silently does nothing.

3. **All three `cmd/` binaries still start cleanly, from a fresh build:**

   ```sh
   make down-all               # in case a stale set of containers is still up
   make up-all                 # docker compose up -d --build: postgres + all three services
   docker compose ps           # all four containers healthy
   curl -sf http://localhost:8081/healthz && curl -sf http://localhost:8081/readyz
   curl -sf http://localhost:9101/healthz && curl -sf http://localhost:9101/readyz
   curl -sf http://localhost:9102/healthz && curl -sf http://localhost:9102/readyz
   ```

   (Collector's published port is 8081, not 8080 — see the comment beside its `ports:`
   block in `docker-compose.yml`.) A binary that fails to start, or a `/readyz` that never
   turns green, is a real issue the same session it's found.

4. **Indexer cursor liveness — the manual substitute for issue #8's proposed `/metrics`,
   until that's built.** This is this repo's own check; `tallybook-contracts` has nothing
   analogous, because it has no long-running daemon. Issue #7 demonstrated exactly why this
   matters: a wedged indexer is indistinguishable from a healthy one by `/healthz` alone —
   both stay 200 — so until issue #8 ships a real staleness metric, this is checked by hand:

   ```sh
   docker exec tallybook-postgres-1 psql -U tallybook -d tallybook -t -c \
     "SELECT cursor, ledger, updated_at FROM indexer_cursors WHERE name = 'chain_events';"
   ```

   Record `updated_at` (and `ledger`) each week. If it hasn't advanced since the previous
   check **and** the indexer's own logs show repeating `ERROR indexer: tick failed` lines
   (`docker logs tallybook-indexer-1`) rather than genuine silence (no new chain activity is
   also a legitimate reason `ledger` doesn't move — check the log line, not just the
   number), that's a live instance of the #7 failure class: file it, or if it's the exact
   same wedge as #7, note it in #7 rather than opening a duplicate.

5. Report per §7 below. "Nothing to report" is a valid, expected outcome most weeks.

### Fortnightly (about an hour)

1. Re-run every live command in `docs/operator-runbook.md` — same discipline as
   `tallybook-contracts`' `guides/verifying-a-charge.md` re-runs. A `stellar-cli` release
   can silently break a documented command's flags or output shape.
2. Fetch every external link in `README.md`, `SECURITY.md`, and `docs/operator-runbook.md`.
   Fix 404s directly rather than filing them.
3. Diff `README.md`'s environment variable table and `CLAUDE.md` §7 against
   `internal/config/config.go`'s actual validated fields — catch drift before a contributor
   trusts stale documentation over the code.
4. Confirm `TB_PRICE_BOOK_ID` / `TB_STATEMENT_REGISTRY_ID` (as documented here) still match
   `tallybook-contracts`' own README table for its live deployment.

### Monthly

1. Review the issue backlog: anything now stale, obsolete, or superseded? Closing an
   obsolete issue is legitimate; closing a still-valid one is not (§2). State the reason in
   the closing comment.
2. Re-check complexity labels against current understanding of each issue's actual scope.
3. Anything sitting with no interest for a month may be badly scoped rather than unpopular —
   rewrite the description, don't delete the issue.
4. Report the month's real activity: commits, PRs reviewed, issues filed, issues closed and
   which §3 category each closure falls under.

---

## 6. Issue quality bar

Match the shape the first eight issues in this repo already use — narrative sections over a
checkbox template, because that's what a Go-behavior bug or a wiring gap needs to be
concretely reproducible, not what a contract-surface change needs:

- **Title** in commit style, lowercase, imperative: `type(scope): description` — or, for a
  bug discovered rather than a feature proposed, a plain description of the failure
  (`cmd/indexer stalls forever: ...`), matching how #7 and #8 are titled.
- **`## What's wrong`** — the defect, named by file and function
  (`internal/stellar/events.go`'s `ChannelWithdrawEvent`, not "the event decoder"). Include
  a real reproduction where one exists: an actual log line, an actual decoded payload, an
  actual command and its actual output — not a hypothetical one. #7's raw testnet event
  payload and #8's description of what `/healthz` actually does are the bar.
- **`## Impact`** — one or two concrete sentences on what breaks and for whom. If this is
  hard to write, the issue is probably filler — discard it (§2).
- **`## Proposed fix`** (or `## Suggested fix`) — concrete enough to start from, but
  explicit about anything that needs checking against an external source first (issue #7:
  the real field semantics need checking against `one-way-channel`'s own code, not
  guessing) rather than prescribing a fix the filer hasn't actually verified.
- **Labels** — one `complexity: *`, plus `bug` or `type: feature` (or another `type: *`),
  plus `good first issue` / `help wanted` where genuinely appropriate.
- **Cross-references** — `Related: #N` for anything genuinely connected (#8 references #7
  as its motivating instance), so the backlog reads as connected findings, not a flat list.

If a fix would require changing an on-chain contract interface, say so explicitly and mark
it as needing sign-off before implementation (`CLAUDE.md`'s own top-level rule) — never
decide that unilaterally.

---

## 7. What to do when there is nothing to do

Report that there is nothing to do. Do not invent work.

If the backlog is healthy and the weekly checks all pass, the highest-value next action is
usually still in this repo, unlike `tallybook-contracts`' own equivalent section (whose
next action was "go build the other repo," which no longer applies once both exist): pick
up #5 or #6 yourself only if genuinely nothing contributor-suitable is pending review, and
say plainly in the report that's why. The two demo-script gaps documented alongside issue #5
(no route wired to the metering middleware; no channel or commitment ever recorded against a
real payer) are the most concrete open threads — closing either one for real, against a real
payer or a real mounted route, is worth more than any amount of maintenance activity here.

---

## 8. Reporting format

End every maintenance session with:

```
Checked:      <what was inspected>
Moved:        <upstream or cross-repo changes found, or "none">
Filed:        <issues created, with why each is real, or "none">
Closed:       <issues closed, with which §3 category each falls under, or "none">
Fixed:        <commits made, or "none">
Needs you:    <anything requiring the maintainer's judgement or credentials>
```

A session that reports "none" across most fields is a successful session. Silence in the
log is better than noise in the repo.
