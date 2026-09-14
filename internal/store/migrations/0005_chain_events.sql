-- chain_events: every Soroban event the indexer has ingested, across all
-- eight contract event kinds (events.go: price_book.publish;
-- statement_registry's anchor/dispute/resolve; one-way-channel's
-- Open/Close/Withdraw/Refund — CLAUDE.md's "five" undercounts, the real
-- total is eight, see events.go). Not given in §5 ("see build sequence");
-- designed here for what the indexer (steps 34-39) actually needs.
--
-- event_id is getEvents' own EventInfo.ID — globally unique and
-- ledger-ordered by construction — so re-ingesting the same event after a
-- resumed cursor overlaps the previous run is a harmless no-op via
-- ON CONFLICT DO NOTHING, not a duplicate row.
--
-- value_xdr keeps every event fully replayable regardless of kind.
-- operator/consumer/seq/amount_billed/amount_settled are a deliberate
-- denormalization, populated only for statement.anchor rows: reconciling
-- metered usage against what was anchored (step 38) needs to look up a
-- specific (operator, seq) statement's billed/settled amounts, and
-- decoding value_xdr for that on every reconciliation query — instead of
-- once, at ingest time, when the event is already being decoded to
-- classify it — would be pure waste.
CREATE TABLE chain_events (
    id               BIGSERIAL PRIMARY KEY,
    event_id         TEXT NOT NULL UNIQUE,
    ledger           INTEGER NOT NULL,
    ledger_closed_at TIMESTAMPTZ NOT NULL,
    contract_id      TEXT NOT NULL,
    tx_hash          TEXT NOT NULL,
    topic            TEXT[] NOT NULL, -- raw base64 ScVal per entry, as returned by getEvents
    kind             TEXT NOT NULL,
    value_xdr        TEXT NOT NULL,   -- raw base64 ScVal event data
    operator         TEXT,            -- statement.anchor only
    consumer         TEXT,            -- statement.anchor only
    seq              BIGINT,          -- statement.anchor only
    amount_billed    NUMERIC(39, 0),  -- statement.anchor only
    amount_settled   NUMERIC(39, 0),  -- statement.anchor only
    ingested_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chain_events_kind_check CHECK (kind IN (
        'price_book.publish',
        'statement.anchor', 'statement.dispute', 'statement.resolve',
        'channel.open', 'channel.close', 'channel.withdraw', 'channel.refund'
    ))
);
CREATE INDEX ON chain_events (ledger);
CREATE INDEX ON chain_events (contract_id, ledger);
CREATE INDEX ON chain_events (kind, operator, seq) WHERE kind = 'statement.anchor';

-- indexer_cursors: durable resume point for the ingestion loop (step 34),
-- one row per named loop (there is one loop today, "chain_events", but a
-- name rather than a single-row table costs nothing and leaves room for
-- more without another migration). `cursor` is getEvents' own opaque
-- pagination token (GetEventsResult.Cursor) — resuming from it, rather
-- than from `ledger` alone, picks up mid-page exactly where the last tick
-- left off. `ledger` is kept alongside purely for operators/metrics; it is
-- not itself used to resume once a cursor exists.
CREATE TABLE indexer_cursors (
    name       TEXT PRIMARY KEY,
    cursor     TEXT NOT NULL,
    ledger     INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
