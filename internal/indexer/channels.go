package indexer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

// applyChannelEvent updates the channels table (§5) for one channel
// lifecycle event, in the same transaction Tick already has open for the
// chain_events insert and cursor update — so channel state and the event
// that produced it can never be inconsistent with each other. Called from
// Tick for every event whose Kind is one of the four channel kinds; a
// no-op for anything else. operatorAddress is this deployment's own
// TB_OPERATOR_ADDRESS, needed by applyChannelOpen to reject a channel that
// isn't actually this operator's.
func applyChannelEvent(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo, kind Kind, operatorAddress string) error {
	switch kind {
	case KindChannelOpen:
		return applyChannelOpen(ctx, tx, ev, operatorAddress)
	case KindChannelClose:
		return applyChannelClose(ctx, tx, ev)
	case KindChannelWithdraw:
		return applyChannelWithdraw(ctx, tx, ev)
	case KindChannelRefund:
		return applyChannelRefund(ctx, tx, ev)
	default:
		return nil
	}
}

const insertChannelSQL = `
INSERT INTO channels (address, operator, funder, token, deposited, withdrawn, refund_waiting_period, status, updated_at)
VALUES ($1, $2, $3, $4, $5, 0, $6, 'open', now())
ON CONFLICT (address) DO NOTHING`

// applyChannelOpen creates the channels row for a newly-opened channel.
// The Open event's own emitting contract (EventInfo.ContractID) is the new
// channel instance's address — one-way-channel is deployed once per
// channel via §4's factory, so each instance emits only its own events.
//
// operator and funder are a deliberate mapping, not literal event fields:
// one-way-channel's Open event carries (from, commitment_key, to, token,
// amount, refund_waiting_period) — there is no "operator" field, because
// "operator" is Tallybook's own domain concept, not the contract's. In
// Tallybook's flow the channel's recipient (`to` — see channel.go's own
// "To returns the recipient's address") is the operator being paid, and
// the funder (`from`) is the consumer/payer depositing into the channel.
//
// decoded.To is checked against operatorAddress before anything is
// written, and the channel is discarded (not stored, not an error) if
// they don't match. This matters specifically because the channel filter
// has no contract ID restriction (see ingest.go's package doc comment):
// without this check, this package would start watching — and the
// settler, later, would start acting on — every channel on the network
// that happens to emit the right topic shape, regardless of who it
// actually pays out to. A channel whose recipient is some other operator
// entirely is not this deployment's to track.
//
// ON CONFLICT DO NOTHING makes re-ingesting the same Open event (a cursor
// overlap on resume) harmless, matching chain_events' own idempotency.
func applyChannelOpen(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo, operatorAddress string) error {
	decoded, err := stellar.DecodeChannelOpenEvent(ev.Value)
	if err != nil {
		return fmt.Errorf("indexer: decode channel Open event: %w", err)
	}

	if decoded.To != operatorAddress {
		slog.DebugContext(ctx, "indexer: Open event for a channel that isn't this operator's, ignoring",
			"channel", ev.ContractID, "to", decoded.To, "operator", operatorAddress)
		return nil
	}

	deposited := pgtype.Numeric{Int: decoded.Amount, Exp: 0, Valid: true}

	if _, err := tx.Exec(ctx, insertChannelSQL,
		ev.ContractID, decoded.To, decoded.From, decoded.Token, deposited, decoded.RefundWaitingPeriod,
	); err != nil {
		return fmt.Errorf("indexer: insert channel %s: %w", ev.ContractID, err)
	}
	return nil
}

const updateChannelCloseSQL = `
UPDATE channels
SET status = 'closing',
    close_started_ledger = $2,
    refund_deadline_ledger = $2 + refund_waiting_period,
    updated_at = now()
WHERE address = $1`

// applyChannelClose records that close_start was called: the deadline
// computation §6 depends on entirely. close_started_ledger is the Close
// event's own effective_at_ledger field, not EventInfo.Ledger (the ledger
// the event happened to be observed on) — using the contract's own value
// is correct even if event delivery is ever delayed relative to when the
// waiting period actually began. refund_deadline_ledger is computed here,
// once, from that same ledger plus the channel's already-known
// refund_waiting_period (captured at Open) — this is the number §6 calls
// "the single most important number this system produces."
func applyChannelClose(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo) error {
	decoded, err := stellar.DecodeChannelCloseEvent(ev.Value)
	if err != nil {
		return fmt.Errorf("indexer: decode channel Close event: %w", err)
	}

	tag, err := tx.Exec(ctx, updateChannelCloseSQL, ev.ContractID, decoded.EffectiveAtLedger)
	if err != nil {
		return fmt.Errorf("indexer: update channel %s on close: %w", ev.ContractID, err)
	}
	if tag.RowsAffected() == 0 {
		warnUnknownChannel(ctx, "Close", ev.ContractID)
	}
	return nil
}

const updateChannelWithdrawSQL = `
UPDATE channels
SET withdrawn = withdrawn + $2,
    last_settled_amount = withdrawn + $2,
    last_settled_ledger = $3,
    updated_at = now()
WHERE address = $1`

// applyChannelWithdraw folds one incremental withdrawal into the channel's
// cumulative totals. ChannelWithdrawEvent.Amount is a delta — "the
// difference between amount and what's already been withdrawn" per
// channel.go's Settle doc comment — so it accumulates via `withdrawn =
// withdrawn + $2` rather than replacing the column outright. Both SET
// targets reference the pre-update value of `withdrawn` (Postgres
// evaluates every expression in one UPDATE's SET list against the row as
// it was before the statement, not against each other's new values), so
// last_settled_amount ends up equal to the new cumulative withdrawn total
// in the same statement, with no second round trip and no read-modify-write
// race.
//
// last_settled_amount tracks the same cumulative total as withdrawn
// (rather than the incremental delta) because that's what the settler's
// exposure calculation (§6: "highest verified commitment minus
// last_settled_amount") needs to compare against a future commitment's own
// cumulative_amount.
func applyChannelWithdraw(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo) error {
	decoded, err := stellar.DecodeChannelWithdrawEvent(ev.Value)
	if err != nil {
		return fmt.Errorf("indexer: decode channel Withdraw event: %w", err)
	}
	amount := pgtype.Numeric{Int: decoded.Amount, Exp: 0, Valid: true}

	tag, err := tx.Exec(ctx, updateChannelWithdrawSQL, ev.ContractID, amount, ev.Ledger)
	if err != nil {
		return fmt.Errorf("indexer: update channel %s on withdraw: %w", ev.ContractID, err)
	}
	if tag.RowsAffected() == 0 {
		warnUnknownChannel(ctx, "Withdraw", ev.ContractID)
	}
	return nil
}

const updateChannelRefundSQL = `
UPDATE channels SET status = 'refunded', updated_at = now() WHERE address = $1`

// applyChannelRefund marks the channel terminally refunded — the transfer
// that makes every unsettled commitment worthless (§1). It does not touch
// withdrawn: a refund returns the remaining balance to the funder, not to
// the recipient, so it is not part of the recipient's cumulative take.
func applyChannelRefund(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo) error {
	if _, err := stellar.DecodeChannelRefundEvent(ev.Value); err != nil {
		return fmt.Errorf("indexer: decode channel Refund event: %w", err)
	}

	tag, err := tx.Exec(ctx, updateChannelRefundSQL, ev.ContractID)
	if err != nil {
		return fmt.Errorf("indexer: update channel %s on refund: %w", ev.ContractID, err)
	}
	if tag.RowsAffected() == 0 {
		warnUnknownChannel(ctx, "Refund", ev.ContractID)
	}
	return nil
}

// warnUnknownChannel logs, rather than errors, an update that matched no
// row. Given the topic-only channel filter (see the package doc comment),
// a Close/Withdraw/Refund event arriving before its channel's Open event
// has been ingested — or for a channel whose Open predates this indexer's
// own start ledger — is an expected, recoverable situation, not a bug:
// there is nothing to update yet, and failing the whole tick over it would
// make ingestion less resilient, not more correct.
func warnUnknownChannel(ctx context.Context, eventKind, channel string) {
	slog.WarnContext(ctx, "indexer: channel lifecycle event for unknown channel, skipping",
		"event", eventKind, "channel", channel)
}
