package meter

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/merkle"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

type anchorCall struct {
	consumer                    string
	periodStart, periodEnd      uint32
	usageRoot                   [32]byte
	requestCount                uint64
	token                       string
	amountBilled, amountSettled *big.Int
	priceBookVersion            uint32
	protocol                    stellar.Protocol
	channel                     *string
}

type fakeAnchorSubmitter struct {
	seq   uint64
	hash  string
	err   error
	calls []anchorCall
}

func (f *fakeAnchorSubmitter) Anchor(_ context.Context, _ *keypair.Full, consumer string, periodStart, periodEnd uint32,
	usageRoot [32]byte, requestCount uint64, token string, amountBilled, amountSettled *big.Int,
	priceBookVersion uint32, protocol stellar.Protocol, channel *string,
) (uint64, string, error) {
	f.calls = append(f.calls, anchorCall{
		consumer: consumer, periodStart: periodStart, periodEnd: periodEnd, usageRoot: usageRoot,
		requestCount: requestCount, token: token, amountBilled: amountBilled, amountSettled: amountSettled,
		priceBookVersion: priceBookVersion, protocol: protocol, channel: channel,
	})
	if f.err != nil {
		return 0, "", f.err
	}
	return f.seq, f.hash, nil
}

// insertClosedPeriod inserts a closed periods row with the given
// protocol/price_version/bounds and returns its id.
func insertClosedPeriod(t *testing.T, pool *pgxpool.Pool, protocol string, priceVersion, periodStart, periodEnd uint32) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO periods (operator, consumer, protocol, price_version, period_start, period_end, status, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'closed', now())
		RETURNING id`,
		testOperator, testConsumer, protocol, priceVersion, periodStart, periodEnd,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert closed period: %v", err)
	}
	return id
}

// insertAnchorTestRequest inserts one requests row belonging to periodID.
// seed varies the request_id/endpoint_hash bytes per call so each row is
// distinct.
func insertAnchorTestRequest(t *testing.T, pool *pgxpool.Pool, periodID int64, seed byte, unitCount uint64, chargedAmount int64, priceVersion, observedLedger uint32) {
	t.Helper()
	requestID := make([]byte, 32)
	requestID[0] = seed
	endpointHash := make([]byte, 32)
	endpointHash[0] = seed ^ 0xFF
	amount := pgtype.Numeric{Int: big.NewInt(chargedAmount), Exp: 0, Valid: true}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO requests (request_id, operator, consumer, endpoint_hash, method, path_template,
		                       unit_count, price_version, charged_amount, protocol, observed_ledger, period_id)
		VALUES ($1, $2, $3, $4, 'GET', '/v1/x', $5, $6, $7, 'x402', $8, $9)`,
		requestID, testOperator, testConsumer, endpointHash, unitCount, priceVersion, amount, observedLedger, periodID,
	)
	if err != nil {
		t.Fatalf("insert request: %v", err)
	}
}

func periodStatus(t *testing.T, pool *pgxpool.Pool, periodID int64) (status string, seq *int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT status, statement_seq FROM periods WHERE id = $1`, periodID).
		Scan(&status, &seq); err != nil {
		t.Fatalf("query period status: %v", err)
	}
	return status, seq
}

func TestAnchorPeriod_HappyPath(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", 1, 1000, 1999)
	insertAnchorTestRequest(t, pool, periodID, 1, 3, 1000, 1, 1500)
	insertAnchorTestRequest(t, pool, periodID, 2, 5, 2000, 1, 1600)

	fake := &fakeAnchorSubmitter{seq: 7, hash: "anchortxhash"}
	a := NewAnchorer(pool)

	result, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3000))
	if err != nil {
		t.Fatalf("AnchorPeriod returned unexpected error: %v", err)
	}
	if result.StatementSeq != 7 || result.TxHash != "anchortxhash" {
		t.Errorf("result = %+v, want seq 7, hash anchortxhash", result)
	}
	if result.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", result.RequestCount)
	}
	if result.AmountBilled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("AmountBilled = %s, want 3000", result.AmountBilled)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("Anchor called %d times, want 1", len(fake.calls))
	}
	call := fake.calls[0]
	if call.consumer != testConsumer {
		t.Errorf("consumer = %q, want %q", call.consumer, testConsumer)
	}
	if call.periodStart != 1000 || call.periodEnd != 1999 {
		t.Errorf("period bounds = [%d, %d], want [1000, 1999]", call.periodStart, call.periodEnd)
	}
	if call.requestCount != 2 {
		t.Errorf("requestCount = %d, want 2", call.requestCount)
	}
	if call.token != "CTOKEN" {
		t.Errorf("token = %q, want CTOKEN", call.token)
	}
	if call.amountBilled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("amountBilled = %s, want 3000", call.amountBilled)
	}
	if call.amountSettled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("amountSettled = %s, want 3000", call.amountSettled)
	}
	if call.priceBookVersion != 1 {
		t.Errorf("priceBookVersion = %d, want 1", call.priceBookVersion)
	}
	if call.protocol != stellar.ProtocolX402 {
		t.Errorf("protocol = %q, want %q", call.protocol, stellar.ProtocolX402)
	}
	if call.channel != nil {
		t.Errorf("channel = %v, want nil for x402", call.channel)
	}

	status, seq := periodStatus(t, pool, periodID)
	if status != "anchored" {
		t.Errorf("period status = %q, want anchored", status)
	}
	if seq == nil || *seq != 7 {
		t.Errorf("period statement_seq = %v, want 7", seq)
	}
}

func TestAnchorPeriod_UsageRootMatchesIndependentMerkleComputation(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", 1, 1000, 1999)
	insertAnchorTestRequest(t, pool, periodID, 1, 3, 1000, 1, 1500)
	insertAnchorTestRequest(t, pool, periodID, 2, 5, 2000, 1, 1600)
	insertAnchorTestRequest(t, pool, periodID, 3, 1, 500, 1, 1700)

	fake := &fakeAnchorSubmitter{seq: 1, hash: "h"}
	a := NewAnchorer(pool)
	if _, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(3500)); err != nil {
		t.Fatalf("AnchorPeriod returned unexpected error: %v", err)
	}

	// Rebuild the same three leaves independently, in the same order
	// (ORDER BY id — insertion order here) AnchorPeriod itself uses, and
	// confirm the tree it built has the exact root AnchorPeriod submitted.
	wantLeaves := make([][32]byte, 3)
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
			Amount: big.NewInt(want.amount), Consumer: testConsumer, EndpointHash: endpointHash,
			Ledger: 1500 + uint32(i)*100, PriceVersion: 1, RequestID: requestID, Units: want.units,
		})
		if err != nil {
			t.Fatalf("merkle.Leaf: %v", err)
		}
		wantLeaves[i] = leaf
	}
	wantTree, err := merkle.BuildTree(wantLeaves)
	if err != nil {
		t.Fatalf("merkle.BuildTree: %v", err)
	}

	if fake.calls[0].usageRoot != wantTree.Root {
		t.Errorf("usageRoot = %x, want %x (independently computed from the same requests)", fake.calls[0].usageRoot, wantTree.Root)
	}
}

func TestAnchorPeriod_RejectsNonClosedPeriod(t *testing.T) {
	pool := testMeterPool(t)
	// An open period, not closed.
	catalog := singleVersionCatalog(t)
	pc := NewPeriodCloser(pool)
	p, err := pc.EnsureOpenPeriod(context.Background(), testScope(), 1500, catalog)
	if err != nil {
		t.Fatalf("EnsureOpenPeriod returned unexpected error: %v", err)
	}

	fake := &fakeAnchorSubmitter{seq: 1, hash: "h"}
	a := NewAnchorer(pool)
	_, err = a.AnchorPeriod(context.Background(), p.ID, nil, fake, "CTOKEN", big.NewInt(0))
	if !errors.Is(err, ErrPeriodNotClosed) {
		t.Fatalf("error = %v, want ErrPeriodNotClosed", err)
	}
	if len(fake.calls) != 0 {
		t.Error("Anchor was called for a period that isn't closed")
	}
}

func TestAnchorPeriod_RejectsEmptyPeriod(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", 1, 1000, 1999) // no requests

	fake := &fakeAnchorSubmitter{seq: 1, hash: "h"}
	a := NewAnchorer(pool)
	_, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(0))
	if !errors.Is(err, ErrPeriodEmpty) {
		t.Fatalf("error = %v, want ErrPeriodEmpty", err)
	}
	if len(fake.calls) != 0 {
		t.Error("Anchor was called for an empty period")
	}
}

func TestAnchorPeriod_ProtocolMapping(t *testing.T) {
	tests := []struct {
		stored string
		want   stellar.Protocol
	}{
		{"x402", stellar.ProtocolX402},
		{"mpp_charge", stellar.ProtocolMppCharge},
		{"mpp_session", stellar.ProtocolMppSession},
	}
	for _, tt := range tests {
		t.Run(tt.stored, func(t *testing.T) {
			pool := testMeterPool(t)
			var channel *string
			if tt.stored == "mpp_session" {
				c := "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"
				channel = &c
			}
			var periodID int64
			err := pool.QueryRow(context.Background(), `
				INSERT INTO periods (operator, consumer, protocol, channel, price_version, period_start, period_end, status, closed_at)
				VALUES ($1, $2, $3, $4, 1, 1000, 1999, 'closed', now()) RETURNING id`,
				testOperator, testConsumer, tt.stored, channel,
			).Scan(&periodID)
			if err != nil {
				t.Fatalf("insert period: %v", err)
			}
			insertAnchorTestRequest(t, pool, periodID, 1, 1, 100, 1, 1500)

			fake := &fakeAnchorSubmitter{seq: 1, hash: "h"}
			a := NewAnchorer(pool)
			if _, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(100)); err != nil {
				t.Fatalf("AnchorPeriod returned unexpected error: %v", err)
			}
			if fake.calls[0].protocol != tt.want {
				t.Errorf("protocol = %q, want %q", fake.calls[0].protocol, tt.want)
			}
		})
	}
}

func TestAnchorPeriod_ErrorLeavesPeriodClosedNotAnchored(t *testing.T) {
	pool := testMeterPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", 1, 1000, 1999)
	insertAnchorTestRequest(t, pool, periodID, 1, 1, 100, 1, 1500)

	fake := &fakeAnchorSubmitter{err: errors.New("stellar: transaction failed on chain")}
	a := NewAnchorer(pool)
	_, err := a.AnchorPeriod(context.Background(), periodID, nil, fake, "CTOKEN", big.NewInt(100))
	if err == nil {
		t.Fatal("AnchorPeriod returned nil error")
	}

	status, seq := periodStatus(t, pool, periodID)
	if status != "closed" {
		t.Errorf("period status = %q, want it to stay closed after a failed anchor attempt", status)
	}
	if seq != nil {
		t.Errorf("statement_seq = %v, want nil", seq)
	}
}
