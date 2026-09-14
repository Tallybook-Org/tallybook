package meter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope identifies which periods row to open or close: at most one open
// period at a time per (operator, consumer, protocol, channel) —
// periods' own partial unique index (0001_periods.sql).
type Scope struct {
	Operator string
	Consumer string
	Protocol string // matches httpapi's Protocol* constants: x402 | mpp_charge | mpp_session
	Channel  string // non-empty iff Protocol == "mpp_session"
}

// Period mirrors one periods row (§5), the fields PeriodCloser cares
// about.
type Period struct {
	ID           int64
	Scope        Scope
	PriceVersion uint32
	PeriodStart  uint32
	PeriodEnd    *uint32
	Status       string // open | closed | anchored
}

// PeriodCloser owns period lifecycle: which period a request observed at
// a given ledger belongs to, closing the currently open one and opening a
// fresh one whenever the price book version in effect changes — not only
// at some fixed calendar boundary, since anchor rejects a statement whose
// period spans a price change (§4 constraint 1: version_at(period_start)
// must equal version_at(period_end)).
type PeriodCloser struct {
	pool *pgxpool.Pool
}

// NewPeriodCloser returns a PeriodCloser backed by pool.
func NewPeriodCloser(pool *pgxpool.Pool) *PeriodCloser {
	return &PeriodCloser{pool: pool}
}

const findOpenPeriodSQL = `
SELECT id, price_version, period_start, period_end, status
FROM periods
WHERE operator = $1 AND consumer = $2 AND protocol = $3 AND channel IS NOT DISTINCT FROM $4 AND status = 'open'`

func (pc *PeriodCloser) findOpenPeriod(ctx context.Context, scope Scope, channel *string) (*Period, error) {
	var p Period
	var priceVersion, periodStart int32
	var periodEnd *int32
	err := pc.pool.QueryRow(ctx, findOpenPeriodSQL, scope.Operator, scope.Consumer, scope.Protocol, channel).
		Scan(&p.ID, &priceVersion, &periodStart, &periodEnd, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("meter: find open period: %w", err)
	}
	p.Scope = scope
	p.PriceVersion = uint32(priceVersion)
	p.PeriodStart = uint32(periodStart)
	if periodEnd != nil {
		v := uint32(*periodEnd)
		p.PeriodEnd = &v
	}
	return &p, nil
}

const insertPeriodSQL = `
INSERT INTO periods (operator, consumer, protocol, channel, price_version, period_start, status)
VALUES ($1, $2, $3, $4, $5, $6, 'open')
RETURNING id`

func (pc *PeriodCloser) openPeriod(ctx context.Context, scope Scope, channel *string, priceVersion, periodStart uint32) (*Period, error) {
	var id int64
	err := pc.pool.QueryRow(ctx, insertPeriodSQL,
		scope.Operator, scope.Consumer, scope.Protocol, channel, priceVersion, periodStart,
	).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost a race with a concurrent EnsureOpenPeriod for the same
			// scope between the earlier find and here. Re-fetch rather
			// than error — whichever call won gets to have opened the
			// period, and the loser just uses it, the same idempotent
			// posture internal/custody and internal/settle already take
			// on their own unique-keyed inserts.
			existing, findErr := pc.findOpenPeriod(ctx, scope, channel)
			if findErr != nil {
				return nil, findErr
			}
			if existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("meter: open period: %w", err)
	}
	return &Period{ID: id, Scope: scope, PriceVersion: priceVersion, PeriodStart: periodStart, Status: "open"}, nil
}

const closePeriodSQL = `
UPDATE periods SET status = 'closed', period_end = $2, closed_at = now() WHERE id = $1 AND status = 'open'`

// ClosePeriod closes periodID explicitly, at endLedger — for a caller
// with its own reason to end a period early (an operator-triggered
// billing-cycle boundary, say) that isn't a price version change.
// EnsureOpenPeriod handles the price-version-change case itself; this is
// the general-purpose primitive underneath it, also usable directly.
func (pc *PeriodCloser) ClosePeriod(ctx context.Context, periodID int64, endLedger uint32) error {
	tag, err := pc.pool.Exec(ctx, closePeriodSQL, periodID, endLedger)
	if err != nil {
		return fmt.Errorf("meter: close period %d: %w", periodID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meter: close period %d: not found or not open", periodID)
	}
	return nil
}

// EnsureOpenPeriod returns the period that a request observed at ledger,
// in scope, belongs to — opening one if none is open yet, or closing the
// current one and opening a fresh one if the price version now in effect
// (per catalog) differs from the open period's own recorded version.
//
// The new period's PeriodStart, and the just-closed period's PeriodEnd,
// are always the exact version-change boundary from catalog (the new
// version's own EffectiveLedger, and the ledger immediately before it) —
// never simply the observation ledger. Using the observation ledger would
// be wrong whenever it lands later than the actual boundary (the common
// case: nothing calls this the instant a new version takes effect, only
// whenever the next request happens to arrive), and would risk a closed
// period's range spanning more than one version if the catalog has moved
// on by more than one version since the period was opened — using
// Catalog.EffectiveLedgerAfter's own boundary, tied to the period's
// recorded version rather than whatever version is current now, is what
// prevents that.
func (pc *PeriodCloser) EnsureOpenPeriod(ctx context.Context, scope Scope, ledger uint32, catalog *Catalog) (*Period, error) {
	wantVersion, err := catalog.VersionAt(ledger)
	if err != nil {
		return nil, fmt.Errorf("meter: ensure open period: %w", err)
	}

	var channel *string
	if scope.Channel != "" {
		channel = &scope.Channel
	}

	existing, err := pc.findOpenPeriod(ctx, scope, channel)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		if existing.PriceVersion == wantVersion.Version {
			return existing, nil
		}

		nextEffective, ok := catalog.EffectiveLedgerAfter(existing.PriceVersion)
		if !ok {
			// Unreachable in practice: wantVersion.Version != existing's
			// version already proves a later version exists in catalog.
			// Treated as a hard error rather than silently misbehaving —
			// it means the catalog and the stored period disagree about
			// something more fundamental.
			return nil, fmt.Errorf(
				"meter: ensure open period: catalog has no version after %d, but ledger %d resolved to version %d",
				existing.PriceVersion, ledger, wantVersion.Version)
		}
		periodEnd := nextEffective - 1
		if periodEnd < existing.PeriodStart {
			return nil, fmt.Errorf(
				"meter: ensure open period: computed period_end %d is before period_start %d for period %d",
				periodEnd, existing.PeriodStart, existing.ID)
		}
		if err := pc.ClosePeriod(ctx, existing.ID, periodEnd); err != nil {
			return nil, err
		}
	}

	return pc.openPeriod(ctx, scope, channel, wantVersion.Version, wantVersion.EffectiveLedger)
}
