package meter

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/merkle"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

type fakeStatementReader struct {
	seqs       []uint64
	statements map[uint64]*stellar.Statement
	listErr    error
}

func (f *fakeStatementReader) ListStatements(context.Context, string, string) ([]uint64, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.seqs, nil
}

func (f *fakeStatementReader) GetStatement(_ context.Context, _ string, seq uint64) (*stellar.Statement, error) {
	stmt, ok := f.statements[seq]
	if !ok {
		return nil, errors.New("statement_registry: get_statement: not found")
	}
	return stmt, nil
}

func attemptStatus(t *testing.T, pool *pgxpool.Pool, periodID int64) (status string, count int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM anchor_attempts WHERE period_id = $1`, periodID).Scan(&count); err != nil {
		t.Fatalf("count anchor_attempts: %v", err)
	}
	if count == 0 {
		return "", 0
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM anchor_attempts WHERE period_id = $1 ORDER BY id DESC LIMIT 1`, periodID).Scan(&status); err != nil {
		t.Fatalf("query latest anchor_attempts status: %v", err)
	}
	return status, count
}

// computeRealUsageRoot independently recomputes the exact usage root
// AnchorPeriod/ReconcileAnchoring would, from the same fixed set of
// requests TestAnchorPeriod_UsageRootMatchesIndependentMerkleComputation
// already establishes matches (amounts 1000/2000/500, seeds 1/2/3).
func computeRealUsageRoot(t *testing.T, consumer string) [32]byte {
	t.Helper()
	leaves := make([][32]byte, 3)
	for i, want := range []struct {
		seed   byte
		units  uint64
		amount int64
	}{
		{1, 3, 1000}, {2, 5, 2000}, {3, 1, 500},
	} {
		requestID := [32]byte{}
		requestID[0] = want.seed
		endpointHash := [32]byte{}
		endpointHash[0] = want.seed ^ 0xFF
		leaf, err := merkle.Leaf(merkle.Record{
			Amount: big.NewInt(want.amount), Consumer: consumer, EndpointHash: endpointHash,
			Ledger: 1500 + uint32(i)*100, PriceVersion: 1, RequestID: requestID, Units: want.units,
		})
		if err != nil {
			t.Fatalf("merkle.Leaf: %v", err)
		}
		leaves[i] = leaf
	}
	tree, err := merkle.BuildTree(leaves)
	if err != nil {
		t.Fatalf("merkle.BuildTree: %v", err)
	}
	return tree.Root
}

func insertPeriodWithThreeRequests(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	periodID := insertClosedPeriod(t, pool, "x402", 1, 1000, 1999)
	insertAnchorTestRequest(t, pool, periodID, 1, 3, 1000, 1, 1500)
	insertAnchorTestRequest(t, pool, periodID, 2, 5, 2000, 1, 1600)
	insertAnchorTestRequest(t, pool, periodID, 3, 1, 500, 1, 1700)
	return periodID
}

func TestAnchorPeriod_AlreadyAnchoredIsIdempotent(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE periods SET status = 'anchored', statement_seq = 42 WHERE id = $1`, periodID); err != nil {
		t.Fatalf("pre-mark period anchored: %v", err)
	}

	fake := &fakeAnchorSubmitter{seq: 999, hash: "should-not-be-used"}
	a := NewAnchorer(pool)
	result, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if err != nil {
		t.Fatalf("AnchorPeriod returned unexpected error: %v", err)
	}
	if result.StatementSeq != 42 {
		t.Errorf("StatementSeq = %d, want 42 (the already-recorded value)", result.StatementSeq)
	}
	if len(fake.calls) != 0 {
		t.Error("Anchor was called for an already-anchored period")
	}
}

func TestAnchorPeriod_ExistingSubmittingAttemptRefuses(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status) VALUES ($1, 'submitting')`, periodID); err != nil {
		t.Fatalf("insert submitting attempt: %v", err)
	}

	fake := &fakeAnchorSubmitter{seq: 1, hash: "should-not-be-used"}
	a := NewAnchorer(pool)
	_, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if !errors.Is(err, ErrAnchorInFlight) {
		t.Fatalf("error = %v, want ErrAnchorInFlight", err)
	}
	if len(fake.calls) != 0 {
		t.Error("Anchor was called despite an unresolved submitting attempt already existing")
	}
}

// TestAnchorPeriod_ExistingConfirmedAttemptFinalizesLocally is the narrow
// crash window internal/settle's own equivalent doesn't need to handle
// (settle's ChannelSettler call and its DB write aren't split this way):
// the chain call succeeded and the attempt was marked confirmed, but the
// process crashed before periods itself was updated. AnchorPeriod must
// finish that update from the already-confirmed attempt, with no new
// chain call.
func TestAnchorPeriod_ExistingConfirmedAttemptFinalizesLocally(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status, statement_seq, tx_hash, confirmed_at)
		 VALUES ($1, 'confirmed', 55, 'realhash', now())`, periodID); err != nil {
		t.Fatalf("insert confirmed attempt: %v", err)
	}

	fake := &fakeAnchorSubmitter{seq: 1, hash: "should-not-be-used"}
	a := NewAnchorer(pool)
	result, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if err != nil {
		t.Fatalf("AnchorPeriod returned unexpected error: %v", err)
	}
	if result.StatementSeq != 55 || result.TxHash != "realhash" {
		t.Errorf("result = %+v, want seq 55, hash realhash", result)
	}
	if len(fake.calls) != 0 {
		t.Error("Anchor was called despite an already-confirmed attempt existing")
	}

	status, _ := periodStatus(t, pool, periodID)
	if status != "anchored" {
		t.Errorf("period status = %q, want anchored", status)
	}
}

func TestAnchorPeriod_PermanentErrorMarksAttemptFailed(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)

	fake := &fakeAnchorSubmitter{err: &stellar.ErrTransactionFailed{Hash: "deadbeef"}}
	a := NewAnchorer(pool)
	_, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if err == nil {
		t.Fatal("AnchorPeriod returned nil error")
	}

	status, _ := periodStatus(t, pool, periodID)
	if status != "closed" {
		t.Errorf("period status = %q, want closed (unchanged)", status)
	}
	attemptStat, count := attemptStatus(t, pool, periodID)
	if count != 1 || attemptStat != "failed" {
		t.Errorf("attempt status = %q (count=%d), want failed (count=1)", attemptStat, count)
	}
}

// TestAnchorPeriod_TransientErrorLeavesAttemptSubmitting confirms a
// non-permanent failure does NOT mark the attempt failed — it is left
// exactly as 'submitting', since whether it actually reached the chain is
// genuinely unknown, matching internal/settle.SubmitWithRetry's identical
// treatment of a transient failure at the deadline.
func TestAnchorPeriod_TransientErrorLeavesAttemptSubmitting(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)

	fake := &fakeAnchorSubmitter{err: errors.New("rpc: connection reset")}
	a := NewAnchorer(pool)
	_, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if err == nil {
		t.Fatal("AnchorPeriod returned nil error")
	}

	status, _ := periodStatus(t, pool, periodID)
	if status != "closed" {
		t.Errorf("period status = %q, want closed", status)
	}
	attemptStat, count := attemptStatus(t, pool, periodID)
	if count != 1 || attemptStat != "submitting" {
		t.Errorf("attempt status = %q (count=%d), want submitting (count=1) — a transient failure must not be marked failed", attemptStat, count)
	}

	// A second AnchorPeriod call must refuse, not blindly retry.
	_, err = a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if !errors.Is(err, ErrAnchorInFlight) {
		t.Fatalf("second AnchorPeriod error = %v, want ErrAnchorInFlight", err)
	}
}

func TestReconcileAnchoring_NoActiveAttemptIsNoop(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)

	a := NewAnchorer(pool)
	result, err := a.ReconcileAnchoring(context.Background(), periodID, &fakeStatementReader{})
	if err != nil {
		t.Fatalf("ReconcileAnchoring returned unexpected error: %v", err)
	}
	if result.Resolved {
		t.Error("Resolved = true, want false (nothing to reconcile)")
	}
}

// TestReconcileAnchoring_FindsMatchingStatementAndConfirms is the core
// case: an anchor call landed on chain, but the process crashed before
// recording the outcome. Reconciliation must find it by content — not by
// a known seq — and finish the job.
func TestReconcileAnchoring_FindsMatchingStatementAndConfirms(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status) VALUES ($1, 'submitting')`, periodID); err != nil {
		t.Fatalf("insert submitting attempt: %v", err)
	}

	realRoot := computeRealUsageRoot(t, testConsumer)
	statements := &fakeStatementReader{
		seqs: []uint64{10, 11, 12},
		statements: map[uint64]*stellar.Statement{
			10: {PeriodStart: 1000, PeriodEnd: 5000, UsageRoot: [32]byte{0xAA}}, // unrelated statement
			11: {PeriodStart: 1000, PeriodEnd: 1999, UsageRoot: realRoot},       // the real match
			12: {PeriodStart: 1000, PeriodEnd: 1999, UsageRoot: [32]byte{0xBB}}, // same bounds, wrong root
		},
	}

	a := NewAnchorer(pool)
	result, err := a.ReconcileAnchoring(context.Background(), periodID, statements)
	if err != nil {
		t.Fatalf("ReconcileAnchoring returned unexpected error: %v", err)
	}
	if !result.Resolved || !result.Anchored || result.StatementSeq != 11 {
		t.Fatalf("result = %+v, want Resolved=true Anchored=true StatementSeq=11", result)
	}

	status, seq := periodStatus(t, pool, periodID)
	if status != "anchored" {
		t.Errorf("period status = %q, want anchored", status)
	}
	if seq == nil || *seq != 11 {
		t.Errorf("period statement_seq = %v, want 11", seq)
	}
	attemptStat, _ := attemptStatus(t, pool, periodID)
	if attemptStat != "confirmed" {
		t.Errorf("attempt status = %q, want confirmed", attemptStat)
	}
}

func TestReconcileAnchoring_NoMatchMarksFailed(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status) VALUES ($1, 'submitting')`, periodID); err != nil {
		t.Fatalf("insert submitting attempt: %v", err)
	}

	statements := &fakeStatementReader{
		seqs: []uint64{10},
		statements: map[uint64]*stellar.Statement{
			10: {PeriodStart: 1000, PeriodEnd: 5000, UsageRoot: [32]byte{0xAA}}, // no match
		},
	}

	a := NewAnchorer(pool)
	result, err := a.ReconcileAnchoring(context.Background(), periodID, statements)
	if err != nil {
		t.Fatalf("ReconcileAnchoring returned unexpected error: %v", err)
	}
	if !result.Resolved || result.Anchored {
		t.Fatalf("result = %+v, want Resolved=true Anchored=false", result)
	}

	status, _ := periodStatus(t, pool, periodID)
	if status != "closed" {
		t.Errorf("period status = %q, want closed (still eligible for a fresh attempt)", status)
	}
	attemptStat, _ := attemptStatus(t, pool, periodID)
	if attemptStat != "failed" {
		t.Errorf("attempt status = %q, want failed", attemptStat)
	}
}

// TestReconcileAnchoring_ThenFreshAnchorPeriodSucceeds confirms a failed
// (reconciled-away) attempt does not permanently block the period: a
// fresh AnchorPeriod call afterward must proceed normally.
func TestReconcileAnchoring_ThenFreshAnchorPeriodSucceeds(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertPeriodWithThreeRequests(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status) VALUES ($1, 'submitting')`, periodID); err != nil {
		t.Fatalf("insert submitting attempt: %v", err)
	}

	a := NewAnchorer(pool)
	statements := &fakeStatementReader{} // no statements at all: nothing to match
	if _, err := a.ReconcileAnchoring(context.Background(), periodID, statements); err != nil {
		t.Fatalf("ReconcileAnchoring returned unexpected error: %v", err)
	}

	fake := &fakeAnchorSubmitter{seq: 77, hash: "freshhash"}
	result, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500))
	if err != nil {
		t.Fatalf("AnchorPeriod returned unexpected error after reconciliation: %v", err)
	}
	if result.StatementSeq != 77 {
		t.Errorf("StatementSeq = %d, want 77", result.StatementSeq)
	}
	if len(fake.calls) != 1 {
		t.Errorf("Anchor called %d times, want 1", len(fake.calls))
	}

	status, seq := periodStatus(t, pool, periodID)
	if status != "anchored" || seq == nil || *seq != 77 {
		t.Errorf("period status/seq = %q/%v, want anchored/77", status, seq)
	}
}
