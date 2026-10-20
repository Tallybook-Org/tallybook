package anchor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/meter"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

const (
	testOperator = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
	testConsumer = "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
	testToken    = "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"
	testChannel  = "CDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
)

// testPool connects to the local docker-compose Postgres with every
// migration applied, in a fresh database scoped to this test alone — the
// same pattern every other package's own tests use.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const adminURL = "postgres://tallybook:tallybook@localhost:5433/tallybook?sslmode=disable"
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("skipping: cannot reach local postgres (is `docker compose up -d` running?): %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Skipf("skipping: cannot reach local postgres (is `docker compose up -d` running?): %v", err)
	}

	dbName := fmt.Sprintf("tb_test_anchor_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create test database %s: %v", dbName, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, adminURL)
		if err != nil {
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+dbName)
	})

	pool, err := pgxpool.New(ctx, "postgres://tallybook:tallybook@localhost:5433/"+dbName+"?sslmode=disable")
	if err != nil {
		t.Fatalf("connect to test database %s: %v", dbName, err)
	}
	t.Cleanup(pool.Close)

	applyMigrations(t, pool)
	return pool
}

// applyMigrations runs internal/store's migration files directly against
// pool, in the same numbered order store's own Migrate applies.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join("../store/migrations", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(context.Background(), string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

// anchorCall records what a Driver submitted.
type anchorCall struct {
	consumer                    string
	periodStart, periodEnd      uint32
	token                       string
	amountBilled, amountSettled *big.Int
	protocol                    stellar.Protocol
	channel                     *string
}

type fakeSubmitter struct {
	seq  uint64
	hash string
	err  error
	// failFirst is the number of leading calls that fail, so a test can
	// make one period's anchor fail while the rest succeed.
	failFirst int
	calls     []anchorCall
}

func (f *fakeSubmitter) Anchor(_ context.Context, _ *keypair.Full, consumer string, periodStart, periodEnd uint32,
	_ [32]byte, _ uint64, token string, amountBilled, amountSettled *big.Int,
	_ uint32, protocol stellar.Protocol, channel *string,
) (uint64, string, error) {
	f.calls = append(f.calls, anchorCall{
		consumer: consumer, periodStart: periodStart, periodEnd: periodEnd, token: token,
		amountBilled: amountBilled, amountSettled: amountSettled, protocol: protocol, channel: channel,
	})
	if f.failFirst > 0 {
		f.failFirst--
		return 0, "", errors.New("chain said no")
	}
	if f.err != nil {
		return 0, "", f.err
	}
	return f.seq, f.hash, nil
}

// fakeReader is a StatementReader that reports whatever statements the
// test puts in it.
type fakeReader struct {
	statements map[uint64]*stellar.Statement
}

func (f *fakeReader) ListStatements(context.Context, string, string) ([]uint64, error) {
	seqs := make([]uint64, 0, len(f.statements))
	for seq := range f.statements {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

func (f *fakeReader) GetStatement(_ context.Context, _ string, seq uint64) (*stellar.Statement, error) {
	stmt, ok := f.statements[seq]
	if !ok {
		return nil, fmt.Errorf("no statement %d", seq)
	}
	return stmt, nil
}

// insertClosedPeriod inserts a closed periods row and returns its id.
func insertClosedPeriod(t *testing.T, pool *pgxpool.Pool, protocol, channel string, priceVersion, periodStart, periodEnd uint32) int64 {
	t.Helper()
	var id int64
	var channelArg *string
	if channel != "" {
		channelArg = &channel
	}
	err := pool.QueryRow(context.Background(), `
		INSERT INTO periods (operator, consumer, protocol, channel, price_version, period_start, period_end, status, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'closed', now())
		RETURNING id`,
		testOperator, testConsumer, protocol, channelArg, priceVersion, periodStart, periodEnd,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert closed period: %v", err)
	}
	return id
}

// insertRequest inserts one requests row belonging to periodID.
func insertRequest(t *testing.T, pool *pgxpool.Pool, periodID int64, protocol, channel string, seed byte, amount int64) {
	t.Helper()
	requestID := make([]byte, 32)
	requestID[0] = seed
	endpointHash := make([]byte, 32)
	endpointHash[0] = seed ^ 0xFF
	var channelArg *string
	if channel != "" {
		channelArg = &channel
	}
	numeric := pgtype.Numeric{Int: big.NewInt(amount), Exp: 0, Valid: true}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO requests (request_id, operator, consumer, endpoint_hash, method, path_template,
		                       unit_count, price_version, charged_amount, protocol, channel, observed_ledger, period_id)
		VALUES ($1, $2, $3, $4, 'GET', '/v1/x', 1, 1, $5, $6, $7, 1500, $8)`,
		requestID, testOperator, testConsumer, endpointHash, numeric, protocol, channelArg, periodID,
	)
	if err != nil {
		t.Fatalf("insert request: %v", err)
	}
}

// insertChannel inserts a channels row.
func insertChannel(t *testing.T, pool *pgxpool.Pool, address, token string, lastSettled int64) {
	t.Helper()
	numeric := pgtype.Numeric{Int: big.NewInt(lastSettled), Exp: 0, Valid: true}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO channels (address, operator, funder, token, deposited, withdrawn, refund_waiting_period, status, last_settled_amount)
		VALUES ($1, $2, $3, $4, 0, 0, 100, 'open', $5)`,
		address, testOperator, testConsumer, token, numeric,
	)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}
}

func testSigner(t *testing.T) *keypair.Full {
	t.Helper()
	signer, err := keypair.FromRawSeed([32]byte{1})
	if err != nil {
		t.Fatalf("keypair.FromRawSeed: %v", err)
	}
	return signer
}

func periodStatus(t *testing.T, pool *pgxpool.Pool, periodID int64) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM periods WHERE id = $1`, periodID).
		Scan(&status); err != nil {
		t.Fatalf("query period status: %v", err)
	}
	return status
}

// TestDriver_AnchorDue_X402 covers the per-request protocols: the
// statement is denominated in the configured token, and the whole billed
// amount is reported as settled — an x402 request is paid for
// individually, at the moment it is served.
func TestDriver_AnchorDue_X402(t *testing.T) {
	pool := testPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", "", 1, 1000, 1999)
	insertRequest(t, pool, periodID, "x402", "", 1, 1000)
	insertRequest(t, pool, periodID, "x402", "", 2, 2000)

	submitter := &fakeSubmitter{seq: 7, hash: "abc"}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Token:    testToken,
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 1 {
		t.Fatalf("anchored %d periods, want 1", anchored)
	}
	if len(submitter.calls) != 1 {
		t.Fatalf("submitted %d anchors, want 1", len(submitter.calls))
	}
	call := submitter.calls[0]
	if call.token != testToken {
		t.Errorf("token = %q, want %q", call.token, testToken)
	}
	if call.amountBilled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("amountBilled = %s, want 3000", call.amountBilled)
	}
	if call.amountSettled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("amountSettled = %s, want 3000 — an x402 request is settled as it is served", call.amountSettled)
	}
	if call.protocol != stellar.ProtocolX402 {
		t.Errorf("protocol = %q, want %q", call.protocol, stellar.ProtocolX402)
	}
	if got := periodStatus(t, pool, periodID); got != "anchored" {
		t.Errorf("period status = %q, want anchored", got)
	}
}

// TestDriver_AnchorDue_MPPSession covers the session protocol: the
// statement is denominated in the channel's own token, and the settled
// amount is the channel's swept total, capped at what the period billed
// — the chain rejects a statement that claims more settled than billed.
func TestDriver_AnchorDue_MPPSession(t *testing.T) {
	pool := testPool(t)
	insertChannel(t, pool, testChannel, testToken, 500)
	periodID := insertClosedPeriod(t, pool, "mpp_session", testChannel, 1, 1000, 1999)
	insertRequest(t, pool, periodID, "mpp_session", testChannel, 1, 1000)

	submitter := &fakeSubmitter{seq: 9, hash: "def"}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 1 {
		t.Fatalf("anchored %d periods, want 1", anchored)
	}
	call := submitter.calls[0]
	if call.token != testToken {
		t.Errorf("token = %q, want the channel's own token %q", call.token, testToken)
	}
	if call.amountSettled.Cmp(big.NewInt(500)) != 0 {
		t.Errorf("amountSettled = %s, want 500 — the channel's swept total", call.amountSettled)
	}
	if call.protocol != stellar.ProtocolMppSession {
		t.Errorf("protocol = %q, want %q", call.protocol, stellar.ProtocolMppSession)
	}
	if call.channel == nil || *call.channel != testChannel {
		t.Errorf("channel = %v, want %q", call.channel, testChannel)
	}
}

// TestDriver_AnchorDue_CapsSettledAtBilled covers the cap: a channel
// that has swept more than this period billed cannot make the statement
// claim more settled than billed.
func TestDriver_AnchorDue_CapsSettledAtBilled(t *testing.T) {
	pool := testPool(t)
	insertChannel(t, pool, testChannel, testToken, 100000)
	periodID := insertClosedPeriod(t, pool, "mpp_session", testChannel, 1, 1000, 1999)
	insertRequest(t, pool, periodID, "mpp_session", testChannel, 1, 1000)

	submitter := &fakeSubmitter{seq: 9, hash: "def"}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	if _, err := driver.AnchorDue(context.Background()); err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if got := submitter.calls[0].amountSettled; got.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("amountSettled = %s, want 1000 — capped at the billed amount", got)
	}
}

// TestDriver_AnchorDue_SkipsPeriodWithoutAToken covers the protocols
// whose token cannot be derived: with no token configured, the period is
// left closed rather than anchored in a token nobody chose.
func TestDriver_AnchorDue_SkipsPeriodWithoutAToken(t *testing.T) {
	pool := testPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", "", 1, 1000, 1999)
	insertRequest(t, pool, periodID, "x402", "", 1, 1000)

	submitter := &fakeSubmitter{seq: 1, hash: "x"}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 0 {
		t.Errorf("anchored %d periods, want 0", anchored)
	}
	if len(submitter.calls) != 0 {
		t.Errorf("submitted %d anchors, want 0", len(submitter.calls))
	}
	if got := periodStatus(t, pool, periodID); got != "closed" {
		t.Errorf("period status = %q, want closed — the period stays due for a later pass", got)
	}
}

// TestDriver_AnchorDue_SkipsUnknownChannel covers a session period whose
// channel the indexer has not seen yet: the channel's token is not
// guessable, so the period waits.
func TestDriver_AnchorDue_SkipsUnknownChannel(t *testing.T) {
	pool := testPool(t)
	periodID := insertClosedPeriod(t, pool, "mpp_session", testChannel, 1, 1000, 1999)
	insertRequest(t, pool, periodID, "mpp_session", testChannel, 1, 1000)

	submitter := &fakeSubmitter{seq: 1, hash: "x"}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 0 {
		t.Errorf("anchored %d periods, want 0", anchored)
	}
	if len(submitter.calls) != 0 {
		t.Errorf("submitted %d anchors, want 0", len(submitter.calls))
	}
	if got := periodStatus(t, pool, periodID); got != "closed" {
		t.Errorf("period status = %q, want closed", got)
	}
}

// TestDriver_ReconcileUnresolved covers the restart path: an attempt
// left 'submitting' by a crash is resolved against the chain. With no
// matching statement on chain, it did not land — the attempt is marked
// failed and the period is freed for a fresh attempt.
func TestDriver_ReconcileUnresolved(t *testing.T) {
	pool := testPool(t)
	periodID := insertClosedPeriod(t, pool, "x402", "", 1, 1000, 1999)
	insertRequest(t, pool, periodID, "x402", "", 1, 1000)

	var attemptID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO anchor_attempts (period_id, status) VALUES ($1, 'submitting') RETURNING id`, periodID,
	).Scan(&attemptID); err != nil {
		t.Fatalf("insert anchor attempt: %v", err)
	}

	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: &fakeSubmitter{},
		Reader:   &fakeReader{statements: map[uint64]*stellar.Statement{}},
		Signer:   testSigner(t),
		Token:    testToken,
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	resolved, err := driver.ReconcileUnresolved(context.Background())
	if err != nil {
		t.Fatalf("ReconcileUnresolved returned unexpected error: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved %d attempts, want 1", resolved)
	}

	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM anchor_attempts WHERE id = $1`, attemptID).Scan(&status); err != nil {
		t.Fatalf("query attempt status: %v", err)
	}
	if status != "failed" {
		t.Errorf("attempt status = %q, want failed — no matching statement was on chain", status)
	}

	// With the attempt resolved, the period can be anchored afresh.
	submitter := &fakeSubmitter{seq: 3, hash: "ghi"}
	driver.registry = submitter
	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 1 {
		t.Errorf("anchored %d periods, want 1", anchored)
	}
}

func TestNewDriver_RejectsMissingDependencies(t *testing.T) {
	signer := testSigner(t)
	pool := testPool(t)
	registry := &fakeSubmitter{}

	if _, err := NewDriver(Config{Registry: registry, Signer: signer, Pool: pool}); err == nil {
		t.Error("NewDriver accepted a nil anchorer")
	}
	if _, err := NewDriver(Config{Anchorer: meter.NewAnchorer(pool), Signer: signer, Pool: pool}); err == nil {
		t.Error("NewDriver accepted a nil registry")
	}
	if _, err := NewDriver(Config{Anchorer: meter.NewAnchorer(pool), Registry: registry, Pool: pool}); err == nil {
		t.Error("NewDriver accepted a nil signer")
	}
	if _, err := NewDriver(Config{Anchorer: meter.NewAnchorer(pool), Registry: registry, Signer: signer}); err == nil {
		t.Error("NewDriver accepted a nil pool")
	}
}

// TestDriver_AnchorDue_OneFailureDoesNotStopTheRest covers the batch
// posture: one period's failure is logged and the rest of the batch
// still goes through.
func TestDriver_AnchorDue_OneFailureDoesNotStopTheRest(t *testing.T) {
	pool := testPool(t)
	first := insertClosedPeriod(t, pool, "x402", "", 1, 1000, 1999)
	insertRequest(t, pool, first, "x402", "", 1, 1000)
	second := insertClosedPeriod(t, pool, "x402", "", 1, 2000, 2999)
	insertRequest(t, pool, second, "x402", "", 2, 2000)

	submitter := &fakeSubmitter{seq: 1, hash: "x", failFirst: 1}
	driver, err := NewDriver(Config{
		Anchorer: meter.NewAnchorer(pool),
		Registry: submitter,
		Signer:   testSigner(t),
		Token:    testToken,
		Pool:     pool,
	})
	if err != nil {
		t.Fatalf("NewDriver returned unexpected error: %v", err)
	}

	anchored, err := driver.AnchorDue(context.Background())
	if err != nil {
		t.Fatalf("AnchorDue returned unexpected error: %v", err)
	}
	if anchored != 1 {
		t.Fatalf("anchored %d periods, want 1 — the second period's anchor still went through", anchored)
	}
	if len(submitter.calls) != 2 {
		t.Fatalf("submitted %d anchors, want 2", len(submitter.calls))
	}
	if got := periodStatus(t, pool, first); got != "closed" {
		t.Errorf("period status = %q, want closed — the failed attempt left it unanchored", got)
	}
	if got := periodStatus(t, pool, second); got != "anchored" {
		t.Errorf("period status = %q, want anchored", got)
	}
}
