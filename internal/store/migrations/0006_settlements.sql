-- settlements: the settler's own record of intent (§6, built later — steps
-- 40-50). Not given in §5 ("see build sequence"); the shape here follows
-- §6's ordering rules directly rather than guessing ahead of that package:
--
-- Ordering rule 3 ("Record intent before submitting... write a settlements
-- row with status submitting and the intended amount before calling
-- sendTransaction. On restart, reconcile any submitting row against the
-- chain before doing anything else.") is why status defaults to
-- 'submitting' and tx_hash is nullable — a row exists, durably, before
-- sendTransaction is ever called, and tx_hash is only known once it
-- returns.
--
-- Ordering rule 4 ("Every settle attempt is keyed by (channel,
-- cumulative_amount). Retrying is always safe.") is UNIQUE(channel,
-- cumulative_amount): the same idempotency shape internal/custody already
-- uses for commitments, applied here to the settle side.
CREATE TABLE settlements (
    id                BIGSERIAL PRIMARY KEY,
    channel           TEXT NOT NULL REFERENCES channels (address),
    cumulative_amount NUMERIC(39, 0) NOT NULL,
    status            TEXT NOT NULL DEFAULT 'submitting',
    tx_hash           TEXT,
    failure_reason    TEXT,
    submitted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at      TIMESTAMPTZ,

    CONSTRAINT settlements_status_check CHECK (status IN ('submitting', 'confirmed', 'failed')),
    CONSTRAINT settlements_cumulative_amount_positive CHECK (cumulative_amount > 0),
    CONSTRAINT settlements_confirmed_requires_confirmed_at CHECK (
        status != 'confirmed' OR confirmed_at IS NOT NULL
    ),
    UNIQUE (channel, cumulative_amount)
);
CREATE INDEX ON settlements (status);
