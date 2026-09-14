package settle

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"
)

type settleCall struct {
	amount *big.Int
	sig    [64]byte
}

type fakeChannelSettler struct {
	hash  string
	err   error
	calls []settleCall
}

// Settle never dereferences signer — Submit and ChannelSettler only pass
// it through — so tests pass nil (typed *keypair.Full) rather than
// generating a real Stellar keypair.
func (f *fakeChannelSettler) Settle(_ context.Context, _ *keypair.Full, amount *big.Int, sig [64]byte) (string, error) {
	f.calls = append(f.calls, settleCall{amount: amount, sig: sig})
	return f.hash, f.err
}

type fakeWithdrawnReader struct {
	withdrawn *big.Int
	err       error
	called    bool
}

func (f *fakeWithdrawnReader) Withdrawn(context.Context) (*big.Int, error) {
	f.called = true
	return f.withdrawn, f.err
}

// insertValidCommitmentForSettle inserts a valid commitment with a fully
// deterministic, returned signature, so a test can assert Submit looked up
// and used exactly this signature rather than trusting some other value.
func insertValidCommitmentForSettle(t *testing.T, pool *pgxpool.Pool, channel string, amount int64) [64]byte {
	t.Helper()
	var sig [64]byte
	for i := range sig {
		sig[i] = byte(i + 1)
	}
	signerKey := make([]byte, 32)
	amountNum := pgtype.Numeric{Int: big.NewInt(amount), Exp: 0, Valid: true}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO commitments (channel, cumulative_amount, signature, signer_key, received_at, verified_at, valid)
		VALUES ($1, $2, $3, $4, now(), now(), true)`,
		channel, amountNum, sig[:], signerKey,
	)
	if err != nil {
		t.Fatalf("insert valid commitment: %v", err)
	}
	return sig
}

func settlementStatus(t *testing.T, pool *pgxpool.Pool, channel string, amount int64) (status string, exists bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT status FROM settlements WHERE channel = $1 AND cumulative_amount = $2`, channel, amount,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("query settlement status: %v", err)
	}
	return status, true
}

func settlementCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM settlements`).Scan(&count); err != nil {
		t.Fatalf("count settlements: %v", err)
	}
	return count
}

func TestSubmit_HappyPath_RecordsIntentThenConfirms(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	wantSig := insertValidCommitmentForSettle(t, pool, testChannel, 5000)

	fake := &fakeChannelSettler{hash: "deadbeef"}
	s := NewSubmitter(pool)

	rec, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	if rec.Status != "confirmed" {
		t.Errorf("Status = %q, want %q", rec.Status, "confirmed")
	}
	if rec.TxHash != "deadbeef" {
		t.Errorf("TxHash = %q, want %q", rec.TxHash, "deadbeef")
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Settle called %d times, want 1", len(fake.calls))
	}
	if fake.calls[0].amount.Cmp(big.NewInt(5000)) != 0 {
		t.Errorf("Settle called with amount %s, want 5000", fake.calls[0].amount)
	}
	if fake.calls[0].sig != wantSig {
		t.Errorf("Settle called with sig %x, want the commitment's own signature %x", fake.calls[0].sig, wantSig)
	}

	status, exists := settlementStatus(t, pool, testChannel, 5000)
	if !exists || status != "confirmed" {
		t.Errorf("settlements row status = %q (exists=%v), want confirmed", status, exists)
	}
}

func TestSubmit_NoValidCommitment_RefusesAndRecordsNothing(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	// No commitment at all for (testChannel, 5000).

	fake := &fakeChannelSettler{hash: "should-not-be-used"}
	s := NewSubmitter(pool)

	_, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if !errors.Is(err, ErrCommitmentNotFound) {
		t.Fatalf("error = %v, want ErrCommitmentNotFound", err)
	}
	if len(fake.calls) != 0 {
		t.Error("Settle was called despite no valid commitment existing")
	}
	if count := settlementCount(t, pool); count != 0 {
		t.Errorf("settlements has %d rows, want 0 — intent must not be recorded without a real signature to use", count)
	}
}

func TestSubmit_InvalidCommitmentIsTreatedAsNotFound(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertTestCommitment(t, pool, testChannel, 5000, false, time.Now().UTC().Truncate(time.Microsecond)) // valid=false

	fake := &fakeChannelSettler{}
	s := NewSubmitter(pool)

	_, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if !errors.Is(err, ErrCommitmentNotFound) {
		t.Fatalf("error = %v, want ErrCommitmentNotFound (an invalid commitment must never be usable)", err)
	}
	if len(fake.calls) != 0 {
		t.Error("Settle was called despite the only commitment being invalid")
	}
}

func TestSubmit_SendFailure_MarksFailedAndReturnsRecordAndError(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, testChannel, 5000)

	boom := errors.New("stellar: transaction abc failed on chain")
	fake := &fakeChannelSettler{hash: "partial-hash", err: boom}
	s := NewSubmitter(pool)

	rec, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap %v", err, boom)
	}
	if rec == nil {
		t.Fatal("Submit returned a nil record alongside the error, want the failed record")
	}
	if rec.Status != "failed" {
		t.Errorf("rec.Status = %q, want %q", rec.Status, "failed")
	}

	status, exists := settlementStatus(t, pool, testChannel, 5000)
	if !exists || status != "failed" {
		t.Errorf("settlements row status = %q (exists=%v), want failed", status, exists)
	}
}

// TestSubmit_IdempotentOnConfirmed and TestSubmit_IdempotentOnFailed are
// the literal §6 ordering rule 4 coverage: "Every settle attempt is keyed
// by (channel, cumulative_amount). Retrying is always safe."

func TestSubmit_IdempotentOnConfirmed(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, testChannel, 5000)
	fake := &fakeChannelSettler{hash: "hash1"}
	s := NewSubmitter(pool)

	first, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if err != nil {
		t.Fatalf("first Submit returned unexpected error: %v", err)
	}

	second, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if err != nil {
		t.Fatalf("second Submit returned unexpected error: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("Settle called %d times across two Submits for the same key, want 1", len(fake.calls))
	}
	if second.ID != first.ID || second.Status != "confirmed" || second.TxHash != first.TxHash {
		t.Errorf("second Submit's record = %+v, want it identical to the first: %+v", second, first)
	}
}

func TestSubmit_IdempotentOnFailed(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, testChannel, 5000)
	fake := &fakeChannelSettler{err: errors.New("contract rejected")}
	s := NewSubmitter(pool)

	_, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if err == nil {
		t.Fatal("first Submit returned nil error, want the send failure")
	}

	second, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if err != nil {
		t.Fatalf("second Submit on an already-failed settlement returned an error, want nil (§6: \"do not retry blindly\"): %v", err)
	}
	if second.Status != "failed" {
		t.Errorf("second Submit's Status = %q, want %q", second.Status, "failed")
	}
	if len(fake.calls) != 1 {
		t.Errorf("Settle called %d times, want 1 — a failed settlement must not be retried automatically", len(fake.calls))
	}
}

// TestSubmit_ExistingSubmittingRowRefusesWithoutCallingSettle is the crash
// scenario ordering rule 3 exists for: a 'submitting' row is already on
// file (as if a previous process crashed between recording intent and
// resolving the outcome). Submit must not guess — it refuses, rather than
// either silently re-attempting (risking a double settle) or silently
// treating it as done.
func TestSubmit_ExistingSubmittingRowRefusesWithoutCallingSettle(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, testChannel, 5000)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settlements (channel, cumulative_amount, status) VALUES ($1, $2, 'submitting')`,
		testChannel, int64(5000),
	); err != nil {
		t.Fatalf("insert pre-existing submitting row: %v", err)
	}

	fake := &fakeChannelSettler{hash: "should-not-be-called"}
	s := NewSubmitter(pool)

	rec, err := s.Submit(context.Background(), fake, nil, testChannel, big.NewInt(5000))
	if !errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("error = %v, want ErrSettlementInFlight", err)
	}
	if rec == nil || rec.Status != "submitting" {
		t.Errorf("record = %+v, want the existing submitting record returned alongside the error", rec)
	}
	if len(fake.calls) != 0 {
		t.Error("Settle was called despite an unresolved submitting row already existing")
	}
}

func TestReconcileSubmitting_ConfirmsWhenChainShowsItLanded(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settlements (channel, cumulative_amount, status) VALUES ($1, $2, 'submitting')`,
		testChannel, int64(500),
	); err != nil {
		t.Fatalf("insert submitting row: %v", err)
	}

	reader := &fakeWithdrawnReader{withdrawn: big.NewInt(500)}
	s := NewSubmitter(pool)

	resolved, err := s.ReconcileSubmitting(context.Background(), testChannel, reader)
	if err != nil {
		t.Fatalf("ReconcileSubmitting returned unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Status != "confirmed" {
		t.Fatalf("resolved = %+v, want one confirmed record", resolved)
	}
	status, exists := settlementStatus(t, pool, testChannel, 500)
	if !exists || status != "confirmed" {
		t.Errorf("settlements row status = %q (exists=%v), want confirmed", status, exists)
	}
}

func TestReconcileSubmitting_FailsWhenChainDoesNotShowIt(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settlements (channel, cumulative_amount, status) VALUES ($1, $2, 'submitting')`,
		testChannel, int64(500),
	); err != nil {
		t.Fatalf("insert submitting row: %v", err)
	}

	reader := &fakeWithdrawnReader{withdrawn: big.NewInt(0)} // nothing landed
	s := NewSubmitter(pool)

	resolved, err := s.ReconcileSubmitting(context.Background(), testChannel, reader)
	if err != nil {
		t.Fatalf("ReconcileSubmitting returned unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Status != "failed" {
		t.Fatalf("resolved = %+v, want one failed record", resolved)
	}
	status, exists := settlementStatus(t, pool, testChannel, 500)
	if !exists || status != "failed" {
		t.Errorf("settlements row status = %q (exists=%v), want failed", status, exists)
	}
}

func TestReconcileSubmitting_NoSubmittingRowsNeverReadsTheChain(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	reader := &fakeWithdrawnReader{withdrawn: big.NewInt(999)}
	s := NewSubmitter(pool)

	resolved, err := s.ReconcileSubmitting(context.Background(), testChannel, reader)
	if err != nil {
		t.Fatalf("ReconcileSubmitting returned unexpected error: %v", err)
	}
	if resolved != nil {
		t.Errorf("resolved = %+v, want nil", resolved)
	}
	if reader.called {
		t.Error("Withdrawn was called even though there was nothing to reconcile")
	}
}

func TestReconcileSubmitting_MultipleRowsResolvedIndependently(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	for _, amount := range []int64{300, 700} {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO settlements (channel, cumulative_amount, status) VALUES ($1, $2, 'submitting')`,
			testChannel, amount,
		); err != nil {
			t.Fatalf("insert submitting row for %d: %v", amount, err)
		}
	}

	reader := &fakeWithdrawnReader{withdrawn: big.NewInt(300)} // covers 300 but not 700
	s := NewSubmitter(pool)

	resolved, err := s.ReconcileSubmitting(context.Background(), testChannel, reader)
	if err != nil {
		t.Fatalf("ReconcileSubmitting returned unexpected error: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved %d records, want 2", len(resolved))
	}

	status300, _ := settlementStatus(t, pool, testChannel, 300)
	status700, _ := settlementStatus(t, pool, testChannel, 700)
	if status300 != "confirmed" {
		t.Errorf("300's status = %q, want confirmed", status300)
	}
	if status700 != "failed" {
		t.Errorf("700's status = %q, want failed", status700)
	}
}
