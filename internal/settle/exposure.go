// Package settle is the settler: the component that decides whether the
// operator gets paid (§6). It watches every channel with status IN
// ('open', 'closing'), computes how much verified revenue is sitting
// unswept, and decides on every tick whether to settle now or wait.
//
// Every decision here favours durability over throughput and settling
// early over settling optimally (§1) — a missed deadline is lost revenue,
// permanently, the instant the funder's refund goes through.
package settle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChannelNotFound is returned by Calculator.Calculate for a channel
// address with no row in channels.
var ErrChannelNotFound = errors.New("settle: channel not found")

// Exposure summarizes how much of a channel's verified revenue is sitting
// unswept, and for how long, as of the moment it was computed.
type Exposure struct {
	Channel string
	// HighestVerified is the highest cumulative_amount among this
	// channel's valid commitments. Nil if none exist yet — an invalid
	// (valid=false) commitment never counts here, per §6 ordering rule 2:
	// never settle against an unverified commitment.
	HighestVerified *big.Int
	// LastSettledAmount mirrors channels.last_settled_amount (§5): the
	// cumulative amount already swept on chain, as last observed by the
	// indexer.
	LastSettledAmount *big.Int
	// Unsettled is HighestVerified - LastSettledAmount, floored at zero —
	// it must never present as negative, even from a stale or racing read.
	Unsettled *big.Int
	// OldestUnsettledSince is when the oldest still-unsettled valid
	// commitment (cumulative_amount > LastSettledAmount) was received.
	// Nil if there is no unsettled commitment.
	OldestUnsettledSince *time.Time
}

// Age returns how long the oldest unsettled commitment has been waiting,
// as of now. Zero if there is no unsettled commitment.
func (e *Exposure) Age(now time.Time) time.Duration {
	if e.OldestUnsettledSince == nil {
		return 0
	}
	return now.Sub(*e.OldestUnsettledSince)
}

// Calculator computes Exposure from the commitments and channels tables
// (custody and the indexer's own writes, respectively — this package only
// reads them).
type Calculator struct {
	pool *pgxpool.Pool
}

// NewCalculator returns a Calculator backed by pool.
func NewCalculator(pool *pgxpool.Pool) *Calculator {
	return &Calculator{pool: pool}
}

const exposureSQL = `
SELECT
    c.last_settled_amount,
    (SELECT MAX(cumulative_amount) FROM commitments WHERE channel = c.address AND valid = true) AS highest_verified,
    (SELECT MIN(received_at) FROM commitments
        WHERE channel = c.address AND valid = true AND cumulative_amount > c.last_settled_amount) AS oldest_unsettled_since
FROM channels c
WHERE c.address = $1`

// Calculate returns channel's current Exposure.
func (calc *Calculator) Calculate(ctx context.Context, channel string) (*Exposure, error) {
	var lastSettled, highestVerified pgtype.Numeric
	var oldestUnsettledSince *time.Time

	err := calc.pool.QueryRow(ctx, exposureSQL, channel).Scan(&lastSettled, &highestVerified, &oldestUnsettledSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("settle: calculate exposure for %s: %w", channel, ErrChannelNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("settle: calculate exposure for %s: %w", channel, err)
	}

	lastSettledAmount, err := numericToBigInt(lastSettled)
	if err != nil {
		return nil, fmt.Errorf("settle: calculate exposure for %s: last_settled_amount: %w", channel, err)
	}

	unsettled := big.NewInt(0)
	var highestVerifiedAmount *big.Int
	if highestVerified.Valid {
		highestVerifiedAmount, err = numericToBigInt(highestVerified)
		if err != nil {
			return nil, fmt.Errorf("settle: calculate exposure for %s: highest_verified: %w", channel, err)
		}
		unsettled = new(big.Int).Sub(highestVerifiedAmount, lastSettledAmount)
		if unsettled.Sign() < 0 {
			unsettled = big.NewInt(0)
		}
	}

	return &Exposure{
		Channel:              channel,
		HighestVerified:      highestVerifiedAmount,
		LastSettledAmount:    lastSettledAmount,
		Unsettled:            unsettled,
		OldestUnsettledSince: oldestUnsettledSince,
	}, nil
}
