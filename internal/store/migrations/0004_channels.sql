-- channels: what the settler watches (§6). Columns match §5 exactly.
--
-- No step in the build sequence creates this migration explicitly —
-- "periods, statements, settlements, chain_events: see build sequence"
-- deferred four tables to later steps, but channels wasn't one of them; it
-- was given in full in §5's initial schema block, the same tier as
-- requests and commitments, both already migrated. Nothing later in the
-- sequence adds it either. It's added here, ahead of chain_events and
-- settlements (step 33), because settlements' natural foreign key to a
-- channel (§6 ordering rule 4: "every settle attempt is keyed by (channel,
-- cumulative_amount)") needs this table to already exist, and because
-- step 36 ("add channel state tracking from events") needs it too.
CREATE TABLE channels (
    address                TEXT PRIMARY KEY,
    operator               TEXT NOT NULL,
    funder                 TEXT NOT NULL,
    token                  TEXT NOT NULL,
    deposited              NUMERIC(39, 0) NOT NULL,
    withdrawn              NUMERIC(39, 0) NOT NULL DEFAULT 0,
    refund_waiting_period  INTEGER NOT NULL,
    close_started_ledger   INTEGER,
    refund_deadline_ledger INTEGER,
    status                 TEXT NOT NULL,
    last_settled_amount    NUMERIC(39, 0) NOT NULL DEFAULT 0,
    last_settled_ledger    INTEGER,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 'closed' is part of the literal enum §5's own comment lists
    -- ("open | closing | closed | refunded") and is accepted here, but
    -- internal/indexer's event-driven state machine does not yet produce
    -- it: only Open, Close (close_start), and Refund events are decoded
    -- (events.go), and only the first two unambiguously map to 'open' and
    -- 'closing'. Whether a recipient-initiated close() that leaves nothing
    -- to refund should land on 'closed' instead of 'refunded' depends on
    -- one-way-channel's exact event-emission behavior in that case, which
    -- hasn't been independently verified — see channels.go. Allowing the
    -- value here costs nothing and avoids a second migration once that's
    -- confirmed.
    CONSTRAINT channels_status_check CHECK (status IN ('open', 'closing', 'closed', 'refunded')),
    CONSTRAINT channels_deposited_nonnegative CHECK (deposited >= 0),
    CONSTRAINT channels_withdrawn_nonnegative CHECK (withdrawn >= 0),
    CONSTRAINT channels_last_settled_amount_nonnegative CHECK (last_settled_amount >= 0),
    CONSTRAINT channels_refund_waiting_period_nonnegative CHECK (refund_waiting_period >= 0),
    -- A deadline is only meaningful once closing has actually started.
    CONSTRAINT channels_deadline_requires_close_started CHECK (
        refund_deadline_ledger IS NULL OR close_started_ledger IS NOT NULL
    )
);
CREATE INDEX ON channels (status, refund_deadline_ledger);
