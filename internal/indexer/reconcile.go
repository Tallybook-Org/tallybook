package indexer

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors for Reconciler.ReconcilePeriod.
var (
	// ErrPeriodNotFound means periodID does not exist in periods at all.
	ErrPeriodNotFound = errors.New("indexer: period not found")
	// ErrPeriodNotAnchored means the period exists but has never been
	// anchored on chain (periods.statement_seq is still NULL) — there is
	// nothing settled yet to reconcile against.
	ErrPeriodNotAnchored = errors.New("indexer: period has not been anchored yet")
	// ErrAnchorEventNotIngested means the period was anchored
	// (statement_seq is set) but this package has not yet ingested the
	// matching statement.anchor event into chain_events. This is an
	// expected, transient state right after an anchor call — the next
	// ingestion tick resolves it — not a data problem.
	ErrAnchorEventNotIngested = errors.New("indexer: anchor event for this period has not been ingested yet")
)

// Result is the outcome of reconciling one anchored period's metered usage
// (the collector's own record: sum of requests.charged_amount) against
// what statement_registry.anchor actually recorded for it on chain (§1:
// "metered usage can be reconciled against what actually settled").
//
// BilledMatch and SettledMatch are deliberately separate: a mismatch on
// AmountBilled means the collector's local accounting disagrees with what
// got anchored at all — a data integrity bug. A mismatch on AmountSettled
// alone (billed matches, settled doesn't) is expected whenever a dispute
// reduced what was actually collected (§4's resolve_dispute) — not a bug,
// but exactly the number an operator needs reconciliation to surface.
type Result struct {
	PeriodID      int64
	Operator      string
	Consumer      string
	StatementSeq  uint64
	MeteredAmount *big.Int
	AmountBilled  *big.Int
	AmountSettled *big.Int
	BilledMatch   bool
	SettledMatch  bool
}

// Reconciler reconciles periods against the events this package has
// ingested. It owns no write path of its own — reconciliation is a pure
// read-and-compare over data Tick already made durable.
type Reconciler struct {
	pool *pgxpool.Pool
}

// NewReconciler returns a Reconciler backed by pool.
func NewReconciler(pool *pgxpool.Pool) *Reconciler {
	return &Reconciler{pool: pool}
}

const reconcilePeriodSQL = `
SELECT p.operator, p.consumer, p.statement_seq,
       COALESCE(SUM(r.charged_amount), 0) AS metered_amount,
       ce.amount_billed, ce.amount_settled
FROM periods p
LEFT JOIN requests r ON r.period_id = p.id
LEFT JOIN chain_events ce
    ON ce.kind = 'statement.anchor' AND ce.operator = p.operator AND ce.seq = p.statement_seq
WHERE p.id = $1
GROUP BY p.operator, p.consumer, p.statement_seq, ce.amount_billed, ce.amount_settled`

// ReconcilePeriod compares periodID's metered usage against what was
// anchored on chain for it. See ErrPeriodNotFound, ErrPeriodNotAnchored,
// and ErrAnchorEventNotIngested for the three ways there may be nothing
// (yet) to compare.
func (r *Reconciler) ReconcilePeriod(ctx context.Context, periodID int64) (*Result, error) {
	var operator, consumer string
	var statementSeq *int64
	var metered, amountBilled, amountSettled pgtype.Numeric

	err := r.pool.QueryRow(ctx, reconcilePeriodSQL, periodID).
		Scan(&operator, &consumer, &statementSeq, &metered, &amountBilled, &amountSettled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("indexer: reconcile period %d: %w", periodID, ErrPeriodNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("indexer: reconcile period %d: %w", periodID, err)
	}
	if statementSeq == nil {
		return nil, fmt.Errorf("indexer: reconcile period %d: %w", periodID, ErrPeriodNotAnchored)
	}
	if !amountBilled.Valid || !amountSettled.Valid {
		return nil, fmt.Errorf("indexer: reconcile period %d: %w", periodID, ErrAnchorEventNotIngested)
	}

	meteredAmount, err := numericToBigInt(metered)
	if err != nil {
		return nil, fmt.Errorf("indexer: reconcile period %d: metered amount: %w", periodID, err)
	}
	billedAmount, err := numericToBigInt(amountBilled)
	if err != nil {
		return nil, fmt.Errorf("indexer: reconcile period %d: amount_billed: %w", periodID, err)
	}
	settledAmount, err := numericToBigInt(amountSettled)
	if err != nil {
		return nil, fmt.Errorf("indexer: reconcile period %d: amount_settled: %w", periodID, err)
	}

	return &Result{
		PeriodID:      periodID,
		Operator:      operator,
		Consumer:      consumer,
		StatementSeq:  uint64(*statementSeq),
		MeteredAmount: meteredAmount,
		AmountBilled:  billedAmount,
		AmountSettled: settledAmount,
		BilledMatch:   meteredAmount.Cmp(billedAmount) == 0,
		SettledMatch:  meteredAmount.Cmp(settledAmount) == 0,
	}, nil
}

// numericToBigInt converts a NUMERIC(39,0) column's scanned value to
// *big.Int. Every numeric column this package reconciles against
// (charged_amount, amount_billed, amount_settled) is declared with scale
// 0 (§5) — but that constrains the logical value, not how Postgres's wire
// encoding represents it: NUMERIC's binary format can express a whole
// number like 1000 as coefficient 1 with Exp=3 rather than coefficient
// 1000 with Exp=0 (confirmed against a real SUM() result, not assumed —
// an earlier version of this function treated any non-zero Exp as an
// error and broke on exactly this). A positive Exp is therefore just
// scaling, handled here; a negative Exp would mean a genuine fractional
// value slipped into a scale-0 column, which is still worth erroring on
// rather than silently truncating a money value.
func numericToBigInt(n pgtype.Numeric) (*big.Int, error) {
	if !n.Valid {
		return nil, errors.New("numeric value is NULL")
	}
	if n.Int == nil {
		return big.NewInt(0), nil
	}
	v := new(big.Int).Set(n.Int)
	switch {
	case n.Exp > 0:
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)
		v.Mul(v, scale)
	case n.Exp < 0:
		return nil, fmt.Errorf("numeric value has fractional exponent %d, want an integer (scale 0) column", n.Exp)
	}
	return v, nil
}
