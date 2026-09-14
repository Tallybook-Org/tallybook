package settle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"
)

// LedgerSource supplies the current ledger sequence Decide needs to
// evaluate deadline pressure. Implemented by a small adapter over
// internal/stellar.Client.GetLatestLedger in production (there is no
// literal CurrentLedger method on that client — verified against
// rpc.go — so the adapter lives at the wiring layer, not here); a narrow
// interface, matching internal/httpapi's own LedgerSource, keeps this
// package decoupled from the Stellar RPC client directly.
type LedgerSource interface {
	CurrentLedger(ctx context.Context) (uint32, error)
}

// LiveChannel is what the daemon needs from a live channel binding: both
// pieces Submitter already depends on (ChannelSettler to settle,
// ChannelWithdrawnReader to reconcile a prior crashed attempt), combined,
// since the daemon needs both for every channel it watches.
type LiveChannel interface {
	ChannelSettler
	ChannelWithdrawnReader
}

// ChannelFactory builds a LiveChannel bound to one channel address.
// one-way-channel is deployed once per channel (§4's factory), so the
// daemon needs a fresh binding per address rather than one shared
// client — implemented by a small closure around stellar.NewChannel in
// production.
type ChannelFactory func(address string) LiveChannel

// tickRetryBudget bounds how long Tick spends retrying one channel's
// settlement in-line before moving on and letting the next tick pick it
// back up. §6's "keep retrying until the deadline forces escalation" is
// provided by the tick loop itself re-evaluating and re-attempting every
// watched channel on every tick, not by one Tick call blocking
// indefinitely on a single channel — with many channels watched at once,
// one stuck settlement must never starve the rest.
const tickRetryBudget = 20 * time.Second

// tightenedTickInterval is how often Run ticks once any channel is under
// deadline pressure, instead of the configured interval (§6: "keep
// retrying at a tightened interval"). This package deliberately does not
// convert a ledger-number deadline into a precise wall-clock countdown —
// §6 itself warns against trusting unverified ledger-close-time
// arithmetic — so "tightened" here means "check much more often," not a
// computed ETA.
const tightenedTickInterval = 5 * time.Second

// Daemon watches every channel with status IN ('open', 'closing') and, on
// every tick, decides whether to settle each one now (§6).
type Daemon struct {
	pool           *pgxpool.Pool
	calc           *Calculator
	submitter      *Submitter
	channelFactory ChannelFactory
	signer         *keypair.Full
	ledgers        LedgerSource
	policy         Policy
}

// NewDaemon returns a Daemon.
func NewDaemon(pool *pgxpool.Pool, calc *Calculator, submitter *Submitter, channelFactory ChannelFactory, signer *keypair.Full, ledgers LedgerSource, policy Policy) *Daemon {
	return &Daemon{
		pool: pool, calc: calc, submitter: submitter, channelFactory: channelFactory,
		signer: signer, ledgers: ledgers, policy: policy,
	}
}

const watchedChannelsSQL = `
SELECT address, status, refund_deadline_ledger FROM channels WHERE status IN ('open', 'closing')`

type watchedChannel struct {
	address              string
	status               string
	refundDeadlineLedger *uint32
}

func (d *Daemon) watchedChannels(ctx context.Context) ([]watchedChannel, error) {
	rows, err := d.pool.Query(ctx, watchedChannelsSQL)
	if err != nil {
		return nil, fmt.Errorf("settle: daemon: list watched channels: %w", err)
	}
	defer rows.Close()

	var channels []watchedChannel
	for rows.Next() {
		var c watchedChannel
		var deadline *int32
		if err := rows.Scan(&c.address, &c.status, &deadline); err != nil {
			return nil, fmt.Errorf("settle: daemon: scan watched channel: %w", err)
		}
		if deadline != nil {
			v := uint32(*deadline)
			c.refundDeadlineLedger = &v
		}
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settle: daemon: list watched channels: %w", err)
	}
	return channels, nil
}

// Tick evaluates every watched channel once and settles whichever ones
// Decide says to. It returns how many channels it attempted to settle and
// whether any watched channel is currently under deadline pressure (Run
// uses this to decide whether to tighten its own interval). One channel's
// error — computing its exposure, or settling it — is logged and does not
// stop the rest of the tick.
func (d *Daemon) Tick(ctx context.Context) (attempted int, underDeadlinePressure bool, err error) {
	currentLedger, err := d.ledgers.CurrentLedger(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("settle: daemon: current ledger: %w", err)
	}

	channels, err := d.watchedChannels(ctx)
	if err != nil {
		return 0, false, err
	}

	now := time.Now()
	for _, c := range channels {
		exposure, calcErr := d.calc.Calculate(ctx, c.address)
		if calcErr != nil {
			slog.ErrorContext(ctx, "settle: daemon: calculate exposure, skipping this channel this tick",
				"channel", c.address, "error", calcErr)
			continue
		}

		state := ChannelState{
			Address: c.address, Status: c.status,
			RefundDeadlineLedger: c.refundDeadlineLedger, Exposure: exposure,
		}
		decision := Decide(d.policy, state, currentLedger, now)
		if decision.Trigger == TriggerDeadlinePressure {
			underDeadlinePressure = true
		}
		if !decision.ShouldSettle {
			continue
		}

		attempted++
		live := d.channelFactory(c.address)
		opts := DefaultRetryOptions(now.Add(tickRetryBudget))
		rec, submitErr := SubmitWithRetry(ctx, d.submitter, live, d.signer, c.address, decision.Amount, opts)
		if submitErr != nil {
			slog.ErrorContext(ctx, "settle: daemon: settle attempt did not complete this tick",
				"channel", c.address, "amount", decision.Amount, "trigger", decision.Trigger, "error", submitErr)
			continue
		}
		slog.InfoContext(ctx, "settle: daemon: settled channel",
			"channel", c.address, "amount", decision.Amount, "trigger", decision.Trigger, "tx_hash", rec.TxHash)
	}

	return attempted, underDeadlinePressure, nil
}

// Run ticks Daemon every interval until ctx is cancelled, tightening to
// tightenedTickInterval whenever the last tick found any channel under
// deadline pressure and widening back to interval once none remain. A
// failed tick is logged and retried on the next interval, never fatal —
// matching internal/indexer's own daemon-loop philosophy (Ingestor.Run): a
// paused settler means deadlines get closer without anything being swept,
// not that the process should give up.
func (d *Daemon) Run(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		next := interval
		_, underPressure, err := d.Tick(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "settle: daemon: tick failed", "error", err)
		} else if underPressure {
			next = tightenedTickInterval
			slog.WarnContext(ctx, "settle: daemon: a watched channel is under deadline pressure, tightening tick interval",
				"interval", next)
		}
		timer.Reset(next)
	}
}
