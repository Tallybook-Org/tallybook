package settle

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/store"
)

// testSettlePool connects to the local docker-compose Postgres with every
// migration applied, in a fresh database scoped to this test alone — the
// same pattern internal/store's, internal/custody's, and internal/indexer's
// own tests use.
func testSettlePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, "postgres://tallybook:tallybook@localhost:5433/tallybook?sslmode=disable")
	if err != nil {
		t.Skipf("skipping: cannot reach local postgres (is `docker compose up -d` running?): %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Skipf("skipping: cannot reach local postgres (is `docker compose up -d` running?): %v", err)
	}

	dbName := fmt.Sprintf("tb_test_settle_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create test database %s: %v", dbName, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, "postgres://tallybook:tallybook@localhost:5433/tallybook?sslmode=disable")
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

	migrations, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations returned unexpected error: %v", err)
	}
	if _, err := store.Migrate(context.Background(), pool, migrations); err != nil {
		t.Fatalf("store.Migrate returned unexpected error: %v", err)
	}
	return pool
}

const (
	testChannel  = "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"
	testOperator = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
	testFunder   = "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
	testToken    = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
)

// insertTestChannel inserts a channels row with the given status,
// last_settled_amount, and (optionally) close/deadline ledgers.
func insertTestChannel(t *testing.T, pool *pgxpool.Pool, address, status string, lastSettledAmount int64, closeStartedLedger, refundDeadlineLedger *int32) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO channels (address, operator, funder, token, deposited, withdrawn,
		                       refund_waiting_period, status, last_settled_amount,
		                       close_started_ledger, refund_deadline_ledger, updated_at)
		VALUES ($1, $2, $3, $4, 100000000, $5, 1440, $6, $5, $7, $8, now())`,
		address, testOperator, testFunder, testToken, lastSettledAmount, status,
		closeStartedLedger, refundDeadlineLedger,
	)
	if err != nil {
		t.Fatalf("insert test channel: %v", err)
	}
}

// insertTestCommitment inserts a commitments row directly (bypassing
// custody.Store, which this package doesn't depend on) with a fixed
// 64-byte signature and signer key, valid unless told otherwise, at
// receivedAt.
func insertTestCommitment(t *testing.T, pool *pgxpool.Pool, channel string, amount int64, valid bool, receivedAt time.Time) {
	t.Helper()
	sig := make([]byte, 64)
	sig[0] = byte(amount) // vary the bytes a little per row; content is never checked here
	signerKey := make([]byte, 32)
	amountNum := pgtype.Numeric{Int: big.NewInt(amount), Exp: 0, Valid: true}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO commitments (channel, cumulative_amount, signature, signer_key, received_at, verified_at, valid)
		VALUES ($1, $2, $3, $4, $5, $5, $6)`,
		channel, amountNum, sig, signerKey, receivedAt, valid,
	)
	if err != nil {
		t.Fatalf("insert test commitment: %v", err)
	}
}
