package meter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testMeterPool connects to the local docker-compose Postgres with every
// migration applied, in a fresh database scoped to this test alone — the
// same pattern every other package's own tests use.
func testMeterPool(t *testing.T) *pgxpool.Pool {
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

	dbName := fmt.Sprintf("tb_test_meter_%d", time.Now().UnixNano())
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

	applyMigrations(t, pool)
	return pool
}

// applyMigrations runs internal/store's migration files directly against
// pool, without importing internal/store: internal/store imports
// internal/httpapi (for httpapi.RecordedRequest in requests.go), and
// internal/httpapi imports internal/meter (for meter.Pricer in
// metering.go) — importing internal/store from a meter test would be an
// import cycle. Reading the same .sql files store's own Migrate applies,
// in the same numbered order, gets the identical schema without needing
// store's Go API at all.
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
		sqlBytes, err := os.ReadFile(filepath.Join("../store/migrations", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(context.Background(), string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

const (
	testOperator = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
	testConsumer = "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
)

func testScope() Scope {
	return Scope{Operator: testOperator, Consumer: testConsumer, Protocol: "x402"}
}

func singleVersionCatalog(t *testing.T) *Catalog {
	t.Helper()
	sched, err := ParseSchedule([]byte(`{"rules":[{"method":"GET","path_template":"/v1/x","unit_price":"1"}]}`))
	if err != nil {
		t.Fatalf("ParseSchedule returned unexpected error: %v", err)
	}
	catalog, err := NewCatalog(CatalogVersion{Version: 1, EffectiveLedger: 1000, Schedule: sched})
	if err != nil {
		t.Fatalf("NewCatalog returned unexpected error: %v", err)
	}
	return catalog
}

func TestEnsureOpenPeriod_OpensFirstPeriod(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := singleVersionCatalog(t)

	p, err := pc.EnsureOpenPeriod(context.Background(), testScope(), 1500, catalog)
	if err != nil {
		t.Fatalf("EnsureOpenPeriod returned unexpected error: %v", err)
	}
	if p.PriceVersion != 1 {
		t.Errorf("PriceVersion = %d, want 1", p.PriceVersion)
	}
	if p.PeriodStart != 1000 {
		t.Errorf("PeriodStart = %d, want 1000 (the version's own EffectiveLedger, not the observation ledger 1500)", p.PeriodStart)
	}
	if p.Status != "open" {
		t.Errorf("Status = %q, want open", p.Status)
	}
}

func TestEnsureOpenPeriod_ReturnsExistingWhenVersionMatches(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := singleVersionCatalog(t)
	scope := testScope()

	first, err := pc.EnsureOpenPeriod(context.Background(), scope, 1500, catalog)
	if err != nil {
		t.Fatalf("first EnsureOpenPeriod returned unexpected error: %v", err)
	}
	second, err := pc.EnsureOpenPeriod(context.Background(), scope, 1600, catalog)
	if err != nil {
		t.Fatalf("second EnsureOpenPeriod returned unexpected error: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second call returned a different period (%d), want the same one (%d)", second.ID, first.ID)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM periods`).Scan(&count); err != nil {
		t.Fatalf("count periods: %v", err)
	}
	if count != 1 {
		t.Errorf("periods has %d rows, want 1", count)
	}
}

func TestClosePeriod_NotFoundOrNotOpen(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)

	if err := pc.ClosePeriod(context.Background(), 999999, 5000); err == nil {
		t.Error("ClosePeriod accepted a nonexistent period id")
	}
}

func TestCatalog_EffectiveLedgerAfter(t *testing.T) {
	sched, err := ParseSchedule([]byte(`{"rules":[{"method":"GET","path_template":"/v1/x","unit_price":"1"}]}`))
	if err != nil {
		t.Fatalf("ParseSchedule returned unexpected error: %v", err)
	}
	catalog, err := NewCatalog(
		CatalogVersion{Version: 1, EffectiveLedger: 1000, Schedule: sched},
		CatalogVersion{Version: 2, EffectiveLedger: 2000, Schedule: sched},
		CatalogVersion{Version: 3, EffectiveLedger: 3000, Schedule: sched},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned unexpected error: %v", err)
	}

	if got, ok := catalog.EffectiveLedgerAfter(1); !ok || got != 2000 {
		t.Errorf("EffectiveLedgerAfter(1) = (%d, %v), want (2000, true)", got, ok)
	}
	if got, ok := catalog.EffectiveLedgerAfter(2); !ok || got != 3000 {
		t.Errorf("EffectiveLedgerAfter(2) = (%d, %v), want (3000, true)", got, ok)
	}
	if _, ok := catalog.EffectiveLedgerAfter(3); ok {
		t.Error("EffectiveLedgerAfter(3) = ok, want false (3 is the latest version)")
	}
	if _, ok := catalog.EffectiveLedgerAfter(99); ok {
		t.Error("EffectiveLedgerAfter(99) = ok, want false (99 is not in the catalog)")
	}
}
