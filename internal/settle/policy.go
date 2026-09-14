package settle

import (
	"math/big"
	"time"
)

// Trigger identifies why Decide chose to settle now, or the empty string
// if it chose to wait.
type Trigger string

const (
	// TriggerNone means Decide chose to wait.
	TriggerNone Trigger = ""
	// TriggerDeadlinePressure fires when the refund deadline is close
	// enough that missing it becomes a real risk. Overrides every other
	// consideration (§6: "This overrides everything").
	TriggerDeadlinePressure Trigger = "deadline_pressure"
	// TriggerExposureThreshold fires when unsettled revenue exceeds
	// MaxExposure.
	TriggerExposureThreshold Trigger = "exposure_threshold"
	// TriggerAgeThreshold fires when the oldest unsettled commitment has
	// waited longer than MaxExposureAge.
	TriggerAgeThreshold Trigger = "age_threshold"
	// TriggerChannelClosing fires the instant close_start has been
	// observed — settle immediately, don't wait for either threshold
	// (§6: "do not wait for thresholds").
	TriggerChannelClosing Trigger = "channel_closing"
)

// Policy holds the tunable sweep thresholds (§7: TB_SAFETY_MARGIN_LEDGERS,
// TB_MAX_EXPOSURE, TB_MAX_EXPOSURE_AGE).
type Policy struct {
	SafetyMarginLedgers uint32
	MaxExposure         *big.Int
	MaxExposureAge      time.Duration
}

// ChannelState is everything Decide needs about one channel, gathered by
// the caller from channels (lifecycle fields) and Calculator (Exposure).
// Kept separate from any database type so Decide itself is a pure
// function — no I/O, trivially testable.
type ChannelState struct {
	Address              string
	Status               string // open | closing | closed | refunded
	RefundDeadlineLedger *uint32
	Exposure             *Exposure
}

// Decision is the sweep policy's verdict for one channel at one tick.
type Decision struct {
	Channel      string
	ShouldSettle bool
	Trigger      Trigger
	// Amount is the cumulative amount to settle for — Exposure's own
	// HighestVerified — set only when ShouldSettle is true.
	Amount *big.Int
}

// Decide applies §6's decision rules and returns whether to settle
// channel now, and for how much. currentLedger and now are supplied by
// the caller (rather than read internally) so Decide stays a pure
// function of its inputs — no clock, no chain read, fully deterministic
// for a given state.
//
// Checks run in the order §6 lists its four triggers in, which this
// implementation also treats as priority order when more than one applies
// at once: deadline pressure is checked first (and, per §6, overrides
// everything regardless of any other trigger being true too), then the
// exposure threshold, then the age threshold, then channel closing.
// Channel closing being checked last does not make it weaker — §6 is
// explicit that it settles "immediately... do not wait for thresholds" —
// it only means that when a channel is closing AND already past a
// threshold, the more specific/urgent trigger is the one reported.
//
// Regardless of any trigger, Decide never settles when there is nothing
// unsettled: §6 — "Never settle an amount at or below last_settled_amount"
// — so a channel with zero Unsettled exposure always waits, no matter how
// close the deadline is.
func Decide(p Policy, state ChannelState, currentLedger uint32, now time.Time) Decision {
	decision := Decision{Channel: state.Address}

	if state.Exposure == nil || state.Exposure.HighestVerified == nil || state.Exposure.Unsettled.Sign() <= 0 {
		return decision
	}

	switch {
	case state.RefundDeadlineLedger != nil &&
		uint64(currentLedger)+uint64(p.SafetyMarginLedgers) >= uint64(*state.RefundDeadlineLedger):
		decision.Trigger = TriggerDeadlinePressure
	case p.MaxExposure != nil && state.Exposure.Unsettled.Cmp(p.MaxExposure) > 0:
		decision.Trigger = TriggerExposureThreshold
	case p.MaxExposureAge > 0 && state.Exposure.Age(now) > p.MaxExposureAge:
		decision.Trigger = TriggerAgeThreshold
	case state.Status == "closing":
		decision.Trigger = TriggerChannelClosing
	default:
		return decision
	}

	decision.ShouldSettle = true
	decision.Amount = state.Exposure.HighestVerified
	return decision
}
