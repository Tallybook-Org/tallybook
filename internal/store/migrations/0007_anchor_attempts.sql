-- anchor_attempts: the settler's own submitting/confirmed/failed pattern
-- (§6 ordering rules 3 & 4; internal/settle's settlements table), reused
-- for anchoring rather than reinvented: durable intent recorded before
-- the on-chain anchor call, so a crash between a successful call and
-- periods being updated leaves unambiguous evidence to reconcile from,
-- not a silent gap that risks a duplicate on-chain statement on a naive
-- retry.
--
-- Unlike settlements (keyed by (channel, cumulative_amount), a value
-- known before the call), an anchor attempt has no natural pre-call key:
-- statement_registry.anchor's return value — the seq — IS the
-- identifier, unknown until the call has already succeeded. So this
-- table is keyed by period_id instead, and reconciliation
-- (internal/meter's Anchorer.ReconcileAnchoring) resolves an unresolved
-- attempt by content, not by looking up a known seq directly: it
-- recomputes the same merkle usage_root the original attempt would have
-- (deterministic, from requests — which don't change once a period is
-- closed) and matches it, along with period_start/period_end, against
-- every statement list_statements(operator, consumer) currently returns.
CREATE TABLE anchor_attempts (
    id             BIGSERIAL PRIMARY KEY,
    period_id      BIGINT NOT NULL REFERENCES periods (id),
    status         TEXT NOT NULL DEFAULT 'submitting',
    statement_seq  BIGINT,
    tx_hash        TEXT,
    failure_reason TEXT,
    submitted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at   TIMESTAMPTZ,

    CONSTRAINT anchor_attempts_status_check CHECK (status IN ('submitting', 'confirmed', 'failed')),
    CONSTRAINT anchor_attempts_confirmed_requires_fields CHECK (
        status != 'confirmed' OR (confirmed_at IS NOT NULL AND statement_seq IS NOT NULL)
    )
);

-- At most one unresolved-or-successful attempt per period at a time. A
-- failed attempt does NOT block a fresh one — a new row is inserted for
-- the retry, the same way a failed settlement doesn't prevent settling a
-- later, different commitment — but two attempts must never race for the
-- same period, and a period must never end up with two rows both
-- claiming confirmed.
CREATE UNIQUE INDEX anchor_attempts_one_active_per_period ON anchor_attempts (period_id)
    WHERE status IN ('submitting', 'confirmed');

CREATE INDEX ON anchor_attempts (period_id, status);
