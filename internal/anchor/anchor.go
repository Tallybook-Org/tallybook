// Package anchor drives closed billing periods onto the chain as
// statement_registry statements.
//
// A billing period is the unit the operator's revenue is aggregated in:
// requests roll up into a period, and the period becomes a statement
// (internal/meter's Anchorer builds the merkle usage root and submits
// it). Closing a period is not enough — until the period is anchored,
// the usage it records is an off-chain claim nobody can verify, and the
// on-chain dispute mechanism has nothing to point at.
//
// This package is the loop that closes that gap: it finds the periods
// that are closed but not yet anchored, works out the two arguments
// AnchorPeriod deliberately does not derive for itself (the token the
// statement is denominated in, and how much of it has already settled),
// and submits them.
//
// The two arguments are protocol-specific, and the reasoning behind each
// choice is recorded where the choice is made — see tokenFor and
// settledAmountFor.
package anchor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/convert"
	"github.com/Tallybook-Org/tallybook/internal/meter"
)

// Sentinel errors this package returns.
var (
	// ErrNoToken means no token could be resolved for a period's
	// statement: either the period's protocol needs a configured token
	// and none is configured, or the payment channel the period belongs
	// to has not been seen by the indexer yet. The period is not lost —
	// it stays closed and is picked up on a later pass.
	ErrNoToken = errors.New("anchor: no token for this period's statement")

	// ErrUnknownChannel means a period names a payment channel that is
	// not in the channels table — the indexer has not seen the channel's
	// events yet, or never will.
	ErrUnknownChannel = errors.New("anchor: unknown payment channel")
)

// Protocol names, exactly as the periods table's protocol column and its
// CHECK constraint expect (§5) — the same strings internal/httpapi and
// internal/meter use.
const (
	protocolX402       = "x402"
	protocolMPPCharge  = "mpp_charge"
	protocolMPPSession = "mpp_session"
)

// Config configures a Driver.
type Config struct {
	// Anchorer anchors periods and reconciles unresolved attempts.
	// Required.
	Anchorer *meter.Anchorer
	// Registry submits the on-chain anchor call. Required.
	Registry meter.AnchorSubmitter
	// Reader resolves an unresolved attempt against the chain's real
	// state. Optional: without it, an attempt left 'submitting' by a
	// crash is reported but not reconciled, and the period it belongs to
	// stays blocked against a fresh attempt.
	Reader meter.StatementReader
	// Signer authorizes the anchor call as the operator. Required.
	Signer *keypair.Full
	// Token is the SEP-41 token address that x402 and mpp_charge
	// statements are denominated in. Empty means those periods are not
	// anchored: the token a request was paid in is not recorded
	// anywhere in this codebase, and guessing it would be worse than
	// not anchoring.
	Token string
	// Pool reads the payment channels table. Required: an mpp_session
	// period's token and settled amount both come from its channel.
	Pool *pgxpool.Pool
}

// Driver anchors closed periods.
//
// It is safe for concurrent use.
type Driver struct {
	anchorer *meter.Anchorer
	registry meter.AnchorSubmitter
	reader   meter.StatementReader
	signer   *keypair.Full
	token    string
	pool     *pgxpool.Pool
}

// NewDriver returns a Driver built from cfg. It fails on a missing
// dependency rather than returning a Driver that would nil-panic on its
// first anchor: this is startup-time wiring.
func NewDriver(cfg Config) (*Driver, error) {
	if cfg.Anchorer == nil {
		return nil, errors.New("anchor: driver config: anchorer is nil")
	}
	if cfg.Registry == nil {
		return nil, errors.New("anchor: driver config: registry is nil")
	}
	if cfg.Signer == nil {
		return nil, errors.New("anchor: driver config: signer is nil")
	}
	if cfg.Pool == nil {
		return nil, errors.New("anchor: driver config: pool is nil")
	}
	return &Driver{
		anchorer: cfg.Anchorer,
		registry: cfg.Registry,
		reader:   cfg.Reader,
		signer:   cfg.Signer,
		token:    cfg.Token,
		pool:     cfg.Pool,
	}, nil
}

// ReconcileUnresolved resolves every anchor attempt left 'submitting' —
// an attempt whose outcome is unknown because the process died between
// recording the intent and recording the result.
//
// §6's ordering rule 3 requires this to happen before anything else
// after a restart: an attempt that actually landed must not be retried
// into a second statement, and one that did not must not block a fresh
// attempt forever. It returns the number of attempts it resolved.
func (d *Driver) ReconcileUnresolved(ctx context.Context) (int, error) {
	if d.reader == nil {
		return 0, nil
	}

	ids, err := d.anchorer.UnresolvedAttempts(ctx)
	if err != nil {
		return 0, err
	}

	resolved := 0
	for _, id := range ids {
		result, err := d.anchorer.ReconcileAnchoring(ctx, id, d.reader)
		if err != nil {
			slog.ErrorContext(ctx, "anchor: reconcile unresolved attempt", "period_id", id, "error", err)
			continue
		}
		if !result.Resolved {
			continue
		}
		resolved++
		slog.InfoContext(ctx, "anchor: reconciled unresolved attempt",
			"period_id", id, "anchored", result.Anchored, "statement_seq", result.StatementSeq)
	}
	return resolved, nil
}

// AnchorDue anchors every closed period that has not been anchored yet.
// It returns the number of periods it anchored.
//
// One period's failure does not stop the rest: the failure is logged
// and the period stays closed, to be picked up on a later pass. That is
// the same posture internal/meter's CloseDuePeriods takes.
func (d *Driver) AnchorDue(ctx context.Context) (int, error) {
	candidates, err := d.anchorer.DueForAnchoring(ctx)
	if err != nil {
		return 0, err
	}

	anchored := 0
	for _, candidate := range candidates {
		token, err := d.tokenFor(ctx, candidate)
		if err != nil {
			slog.ErrorContext(ctx, "anchor: resolve statement token, skipping this period this pass",
				"period_id", candidate.PeriodID, "protocol", candidate.Protocol, "error", err)
			continue
		}

		settled, err := d.settledAmountFor(ctx, candidate)
		if err != nil {
			slog.ErrorContext(ctx, "anchor: resolve settled amount, skipping this period this pass",
				"period_id", candidate.PeriodID, "protocol", candidate.Protocol, "error", err)
			continue
		}

		result, err := d.anchorer.AnchorPeriod(ctx, candidate.PeriodID, d.signer, d.registry, token, settled)
		if err != nil {
			slog.ErrorContext(ctx, "anchor: anchor period", "period_id", candidate.PeriodID, "error", err)
			continue
		}
		anchored++
		slog.InfoContext(ctx, "anchor: anchored period",
			"period_id", candidate.PeriodID, "statement_seq", result.StatementSeq, "tx_hash", result.TxHash)
	}
	return anchored, nil
}

// Run reconciles unresolved attempts and anchors due periods on every
// tick, until ctx is cancelled. A failed pass is logged and retried on
// the next tick, never fatal — matching every other daemon loop in this
// codebase: a paused anchoring pass means periods accumulate closed but
// unanchored, not that the process should give up.
func (d *Driver) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if _, err := d.ReconcileUnresolved(ctx); err != nil {
			slog.ErrorContext(ctx, "anchor: reconcile unresolved attempts", "error", err)
		}
		if _, err := d.AnchorDue(ctx); err != nil {
			slog.ErrorContext(ctx, "anchor: anchor due periods", "error", err)
		}
	}
}

// tokenFor resolves the SEP-41 token a period's statement is
// denominated in.
//
// An mpp_session period's statement is denominated in its channel's
// token, which the channel itself reports and the indexer records — so
// it is read from there, not configured.
//
// An x402 or mpp_charge period has no channel: the token each request
// was paid in is a property of the payment, and nothing in this
// codebase records it, so it has to be configured. Without a configured
// token those periods are skipped rather than anchored in a token
// nobody chose.
func (d *Driver) tokenFor(ctx context.Context, candidate meter.AnchorCandidate) (string, error) {
	switch candidate.Protocol {
	case protocolMPPSession:
		channel, err := d.channel(ctx, candidate.Channel)
		if err != nil {
			return "", err
		}
		return channel.Token, nil
	case protocolX402, protocolMPPCharge:
		if d.token == "" {
			return "", fmt.Errorf("%w: %s statements need a configured token", ErrNoToken, candidate.Protocol)
		}
		return d.token, nil
	default:
		return "", fmt.Errorf("anchor: unknown protocol %q", candidate.Protocol)
	}
}

// settledAmountFor resolves how much of a period's billed amount has
// already settled on chain.
//
// An x402 or mpp_charge request is paid for individually, at the moment
// it is served: the amount billed is the amount collected, so the whole
// billed amount is reported as settled.
//
// An mpp_session period's revenue is collected by the settler, in sweeps
// against the channel, and the sweeps are cumulative — the channels
// table records what has been withdrawn from the channel so far, across
// every period that channel has served, not this period's share of it.
// The channel's total is therefore reported, capped at what this period
// billed: the chain rejects a statement whose settled amount exceeds
// its billed amount, and a period cannot have settled more than it
// billed.
func (d *Driver) settledAmountFor(ctx context.Context, candidate meter.AnchorCandidate) (*big.Int, error) {
	billed, err := d.anchorer.BilledAmount(ctx, candidate.PeriodID)
	if err != nil {
		return nil, fmt.Errorf("anchor: period %d: billed amount: %w", candidate.PeriodID, err)
	}

	switch candidate.Protocol {
	case protocolX402, protocolMPPCharge:
		return billed, nil
	case protocolMPPSession:
		channel, err := d.channel(ctx, candidate.Channel)
		if err != nil {
			return nil, err
		}
		if channel.LastSettledAmount.Cmp(billed) < 0 {
			return channel.LastSettledAmount, nil
		}
		return billed, nil
	default:
		return nil, fmt.Errorf("anchor: unknown protocol %q", candidate.Protocol)
	}
}

// channelState is what the channels table holds about one payment
// channel.
type channelState struct {
	// Token is the SEP-41 token the channel is denominated in.
	Token string
	// LastSettledAmount is the cumulative amount swept out of the
	// channel so far, as last observed by the indexer.
	LastSettledAmount *big.Int
}

const selectChannelSQL = `
SELECT token, last_settled_amount FROM channels WHERE address = $1`

// channel reads one payment channel's state.
func (d *Driver) channel(ctx context.Context, address string) (*channelState, error) {
	if address == "" {
		return nil, fmt.Errorf("%w: period names no channel", ErrUnknownChannel)
	}
	var token string
	var lastSettled pgtype.Numeric
	err := d.pool.QueryRow(ctx, selectChannelSQL, address).Scan(&token, &lastSettled)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnknownChannel, address, err)
	}
	amount, err := convert.NumericToBigInt(lastSettled)
	if err != nil {
		return nil, fmt.Errorf("anchor: channel %s: last_settled_amount: %w", address, err)
	}
	return &channelState{Token: token, LastSettledAmount: amount}, nil
}
