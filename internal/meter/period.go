package meter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

		periodEnd, err := closeBoundaryFor(catalog, existing.PriceVersion, ledger)
		if err != nil {
			return nil, fmt.Errorf("meter: ensure open period: %w", err)
		}
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

// closeBoundaryFor returns the ledger a period recorded under priceVersion
// must close at, given currentLedger and catalog: currentLedger itself if
// the price version at currentLedger still matches priceVersion (nothing
// to protect against), or the ledger just before the version that
// followed priceVersion otherwise. Shared by EnsureOpenPeriod (which
// detects a price change directly, from a live request) and
// CloseDuePeriods (which detects a calendar duration elapsing, and must
// independently guard against the version having ALSO moved on since the
// period opened — nothing else was necessarily watching this period in
// the meantime) — so neither trigger can ever produce a period whose
// range spans more than one price version.
func closeBoundaryFor(catalog *Catalog, priceVersion uint32, currentLedger uint32) (uint32, error) {
	currentVersion, err := catalog.VersionAt(currentLedger)
	if err != nil {
		return 0, err
	}
	if currentVersion.Version == priceVersion {
		return currentLedger, nil
	}

	nextEffective, ok := catalog.EffectiveLedgerAfter(priceVersion)
	if !ok {
		// Unreachable in practice: currentVersion.Version != priceVersion
		// already proves a later version exists in catalog. Treated as a
		// hard error rather than silently misbehaving — it means the
		// catalog and the stored period disagree about something more
		// fundamental.
		return 0, fmt.Errorf("catalog has no version after %d, but ledger %d resolved to version %d",
			priceVersion, currentLedger, currentVersion.Version)
	}
	return nextEffective - 1, nil
}

// CatalogSource resolves the price Catalog for a given operator.
// CloseDuePeriods needs one per period it considers, since each
// operator's price schedule is independent and a single scan spans every
// operator/consumer/protocol scope at once. Implemented by whatever wires
// together a live price_book reader in production (out of this package's
// scope, the same way EnsureOpenPeriod's own catalog parameter always
// has been); a narrow interface here keeps this package testable without
// one.
type CatalogSource interface {
	CatalogFor(ctx context.Context, operator string) (*Catalog, error)
}

// LedgerSource supplies the current ledger sequence CloseDuePeriods
// needs. Matches internal/httpapi's, internal/indexer's, and
// internal/settle's own identically-shaped interface — no shared import,
// per this codebase's established convention of each package defining
// exactly the narrow interface it needs.
type LedgerSource interface {
	CurrentLedger(ctx context.Context) (uint32, error)
}

const findDuePeriodsSQL = `
SELECT id, operator, consumer, protocol, channel, price_version, period_start
FROM periods
WHERE status = 'open' AND created_at < now() - $1::interval`

type duePeriod struct {
	id                        int64
	operator, consumer        string
	protocol                  string
	channel                   *string
	priceVersion, periodStart uint32
}

func (pc *PeriodCloser) findDuePeriods(ctx context.Context, maxAge time.Duration) ([]duePeriod, error) {
	// Expressed as a plain "<seconds> seconds" interval literal rather
	// than Go's own Duration.String() format (e.g. "720h0m0s") — Postgres
	// parses the former unambiguously; the latter isn't valid interval
	// syntax at all.
	interval := fmt.Sprintf("%f seconds", maxAge.Seconds())

	rows, err := pc.pool.Query(ctx, findDuePeriodsSQL, interval)
	if err != nil {
		return nil, fmt.Errorf("meter: find due periods: %w", err)
	}
	defer rows.Close()

	var out []duePeriod
	for rows.Next() {
		var p duePeriod
		var priceVersion, periodStart int32
		if err := rows.Scan(&p.id, &p.operator, &p.consumer, &p.protocol, &p.channel, &priceVersion, &periodStart); err != nil {
			return nil, fmt.Errorf("meter: find due periods: scan: %w", err)
		}
		p.priceVersion = uint32(priceVersion)
		p.periodStart = uint32(periodStart)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meter: find due periods: %w", err)
	}
	return out, nil
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CloseDuePeriods closes every currently-open period whose age (time
// since it was created) exceeds maxAge, regardless of whether its price
// version has changed — the calendar trigger §6 implies alongside the
// price-version one ("not only at month end"): an operator whose price
// never changes must still get periods closed (and, from there,
// anchored) on a regular cadence, not left open indefinitely. It does
// not open a replacement period; the next request for that scope opens
// one lazily via EnsureOpenPeriod, exactly as it always does.
//
// The closing boundary always goes through closeBoundaryFor — the same
// logic EnsureOpenPeriod uses when it detects a price change directly —
// so a calendar-triggered close can never produce a period whose range
// spans more than one price version either, even if the version has ALSO
// moved on since the period opened.
//
// One period's failure (an unresolvable catalog, a closeBoundaryFor
// error, a failed ClosePeriod call) is logged and does not stop the
// rest of the batch.
func (pc *PeriodCloser) CloseDuePeriods(ctx context.Context, catalogs CatalogSource, currentLedger uint32, maxAge time.Duration) ([]Period, error) {
	due, err := pc.findDuePeriods(ctx, maxAge)
	if err != nil {
		return nil, err
	}

	var closed []Period
	for _, p := range due {
		catalog, err := catalogs.CatalogFor(ctx, p.operator)
		if err != nil {
			slog.ErrorContext(ctx, "meter: period closer: resolve catalog, skipping this period this pass",
				"period_id", p.id, "operator", p.operator, "error", err)
			continue
		}

		periodEnd, err := closeBoundaryFor(catalog, p.priceVersion, currentLedger)
		if err != nil {
			slog.ErrorContext(ctx, "meter: period closer: compute close boundary, skipping this period this pass",
				"period_id", p.id, "error", err)
			continue
		}
		if periodEnd < p.periodStart {
			slog.ErrorContext(ctx, "meter: period closer: computed period_end before period_start, skipping",
				"period_id", p.id, "period_start", p.periodStart, "period_end", periodEnd)
			continue
		}

		if err := pc.ClosePeriod(ctx, p.id, periodEnd); err != nil {
			slog.ErrorContext(ctx, "meter: period closer: close period, skipping", "period_id", p.id, "error", err)
			continue
		}

		closed = append(closed, Period{
			ID: p.id,
			Scope: Scope{
				Operator: p.operator, Consumer: p.consumer, Protocol: p.protocol, Channel: derefOrEmpty(p.channel),
			},
			PriceVersion: p.priceVersion, PeriodStart: p.periodStart, PeriodEnd: &periodEnd, Status: "closed",
		})
	}
	return closed, nil
}

// Run calls CloseDuePeriods every interval until ctx is cancelled. A
// failed tick is logged and retried on the next interval, never fatal —
// matching every other daemon loop in this codebase
// (internal/indexer.Ingestor.Run, internal/settle.Daemon.Run): a paused
// period closer means periods accumulate past their duration without
// being closed, not that the process should give up.
func (pc *PeriodCloser) Run(ctx context.Context, ledgers LedgerSource, catalogs CatalogSource, maxAge, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		currentLedger, err := ledgers.CurrentLedger(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "meter: period closer: current ledger", "error", err)
			continue
		}

		closed, err := pc.CloseDuePeriods(ctx, catalogs, currentLedger, maxAge)
		if err != nil {
			slog.ErrorContext(ctx, "meter: period closer: close due periods", "error", err)
			continue
		}
		if len(closed) > 0 {
			slog.InfoContext(ctx, "meter: period closer: closed periods past their duration", "count", len(closed))
		}
	}
}
