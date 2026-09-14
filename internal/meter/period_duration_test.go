package meter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeCatalogSource struct {
	catalogs map[string]*Catalog
	errs     map[string]error
}

func (f *fakeCatalogSource) CatalogFor(_ context.Context, operator string) (*Catalog, error) {
	if err, ok := f.errs[operator]; ok {
		return nil, err
	}
	c, ok := f.catalogs[operator]
	if !ok {
		return nil, errors.New("fakeCatalogSource: no catalog configured for " + operator)
	}
	return c, nil
}

// insertOpenPeriodWithCreatedAt inserts an open periods row backdated to
// createdAt, so CloseDuePeriods' age check can be exercised without
// actually waiting.
func insertOpenPeriodWithCreatedAt(t *testing.T, pool *pgxpool.Pool, scope Scope, priceVersion, periodStart uint32, createdAt time.Time) int64 {
	t.Helper()
	var channel *string
	if scope.Channel != "" {
		channel = &scope.Channel
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO periods (operator, consumer, protocol, channel, price_version, period_start, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'open', $7)
		RETURNING id`,
		scope.Operator, scope.Consumer, scope.Protocol, channel, priceVersion, periodStart, createdAt,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert backdated open period: %v", err)
	}
	return id
}

// TestCloseDuePeriods_ClosesOnDurationAloneWithNoVersionChange is the
// literal ask: a period closes because it's been open longer than
// maxAge, with the price version completely unchanged throughout — no
// price-change trigger involved at all.
func TestCloseDuePeriods_ClosesOnDurationAloneWithNoVersionChange(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := singleVersionCatalog(t) // one version, effective ledger 1000, forever
	scope := testScope()

	maxAge := 720 * time.Hour
	oldEnough := time.Now().Add(-maxAge - time.Hour)
	periodID := insertOpenPeriodWithCreatedAt(t, pool, scope, 1, 1000, oldEnough)

	catalogs := &fakeCatalogSource{catalogs: map[string]*Catalog{testOperator: catalog}}
	closed, err := pc.CloseDuePeriods(context.Background(), catalogs, 5000, maxAge)
	if err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}
	if len(closed) != 1 || closed[0].ID != periodID {
		t.Fatalf("closed = %+v, want exactly period %d", closed, periodID)
	}
	if closed[0].PriceVersion != 1 {
		t.Errorf("PriceVersion = %d, want 1 (unchanged — this was a pure calendar close)", closed[0].PriceVersion)
	}
	if closed[0].PeriodEnd == nil || *closed[0].PeriodEnd != 5000 {
		t.Errorf("PeriodEnd = %v, want 5000 (the current ledger — no version boundary to respect)", closed[0].PeriodEnd)
	}

	status, _ := periodStatus(t, pool, periodID)
	if status != "closed" {
		t.Errorf("period status = %q, want closed", status)
	}
}

func TestCloseDuePeriods_LeavesPeriodsUnderDurationOpen(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := singleVersionCatalog(t)
	scope := testScope()

	maxAge := 720 * time.Hour
	recent := time.Now().Add(-time.Hour) // well under maxAge
	periodID := insertOpenPeriodWithCreatedAt(t, pool, scope, 1, 1000, recent)

	catalogs := &fakeCatalogSource{catalogs: map[string]*Catalog{testOperator: catalog}}
	closed, err := pc.CloseDuePeriods(context.Background(), catalogs, 5000, maxAge)
	if err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("closed = %+v, want none (period is not yet due)", closed)
	}
	status, _ := periodStatus(t, pool, periodID)
	if status != "open" {
		t.Errorf("period status = %q, want open", status)
	}
}

// TestCloseDuePeriods_RespectsVersionBoundaryIfAlsoChanged confirms the
// safety net: even though the age scan is calendar-driven, a period whose
// price version has ALSO moved on since it opened must still close at the
// correct version boundary, never at currentLedger directly — the same
// invariant EnsureOpenPeriod's own price-change path protects.
func TestCloseDuePeriods_RespectsVersionBoundaryIfAlsoChanged(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := threeVersionMeterCatalog(t) // versions at 1000, 2000, 3000
	scope := testScope()

	maxAge := 720 * time.Hour
	oldEnough := time.Now().Add(-maxAge - time.Hour)
	// Opened under version 1, but the catalog (and "current ledger") has
	// since moved all the way to version 3.
	periodID := insertOpenPeriodWithCreatedAt(t, pool, scope, 1, 1000, oldEnough)

	catalogs := &fakeCatalogSource{catalogs: map[string]*Catalog{testOperator: catalog}}
	closed, err := pc.CloseDuePeriods(context.Background(), catalogs, 3500, maxAge)
	if err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}
	if len(closed) != 1 || closed[0].ID != periodID {
		t.Fatalf("closed = %+v, want exactly period %d", closed, periodID)
	}
	if closed[0].PeriodEnd == nil || *closed[0].PeriodEnd != 1999 {
		t.Errorf("PeriodEnd = %v, want 1999 (version 1's own boundary, not currentLedger 3500 or version 2's 2999)",
			closed[0].PeriodEnd)
	}

	startVersion, err := catalog.VersionAt(closed[0].PeriodStart)
	if err != nil {
		t.Fatalf("VersionAt(period_start): %v", err)
	}
	endVersion, err := catalog.VersionAt(*closed[0].PeriodEnd)
	if err != nil {
		t.Fatalf("VersionAt(period_end): %v", err)
	}
	if startVersion.Version != 1 || endVersion.Version != 1 {
		t.Errorf("closed period resolves to versions %d..%d, want both version 1", startVersion.Version, endVersion.Version)
	}
}

func TestCloseDuePeriods_DoesNotOpenReplacementPeriod(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)
	catalog := singleVersionCatalog(t)
	scope := testScope()

	maxAge := 720 * time.Hour
	oldEnough := time.Now().Add(-maxAge - time.Hour)
	insertOpenPeriodWithCreatedAt(t, pool, scope, 1, 1000, oldEnough)

	catalogs := &fakeCatalogSource{catalogs: map[string]*Catalog{testOperator: catalog}}
	if _, err := pc.CloseDuePeriods(context.Background(), catalogs, 5000, maxAge); err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}

	var openCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM periods WHERE status = 'open'`).Scan(&openCount); err != nil {
		t.Fatalf("count open periods: %v", err)
	}
	if openCount != 0 {
		t.Errorf("open periods = %d, want 0 (CloseDuePeriods must not eagerly open a replacement)", openCount)
	}
}

func TestCloseDuePeriods_MultipleScopesUseTheirOwnOperatorsCatalog(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)

	scopeA := Scope{Operator: testOperator, Consumer: testConsumer, Protocol: "x402"}
	otherOperator := "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
	scopeB := Scope{Operator: otherOperator, Consumer: testConsumer, Protocol: "x402"}

	catalogA := singleVersionCatalog(t) // version 1, effective ledger 1000
	sched, err := ParseSchedule([]byte(`{"rules":[{"method":"GET","path_template":"/v1/x","unit_price":"1"}]}`))
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	catalogB, err := NewCatalog(CatalogVersion{Version: 5, EffectiveLedger: 2000, Schedule: sched})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	maxAge := 720 * time.Hour
	oldEnough := time.Now().Add(-maxAge - time.Hour)
	periodA := insertOpenPeriodWithCreatedAt(t, pool, scopeA, 1, 1000, oldEnough)
	periodB := insertOpenPeriodWithCreatedAt(t, pool, scopeB, 5, 2000, oldEnough)

	catalogs := &fakeCatalogSource{catalogs: map[string]*Catalog{testOperator: catalogA, otherOperator: catalogB}}
	closed, err := pc.CloseDuePeriods(context.Background(), catalogs, 9000, maxAge)
	if err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}
	if len(closed) != 2 {
		t.Fatalf("closed %d periods, want 2", len(closed))
	}

	byID := map[int64]Period{}
	for _, p := range closed {
		byID[p.ID] = p
	}
	if byID[periodA].PriceVersion != 1 {
		t.Errorf("period A PriceVersion = %d, want 1 (its own operator's catalog)", byID[periodA].PriceVersion)
	}
	if byID[periodB].PriceVersion != 5 {
		t.Errorf("period B PriceVersion = %d, want 5 (its own operator's catalog)", byID[periodB].PriceVersion)
	}
}

func TestCloseDuePeriods_OneCatalogFailureDoesNotStopOthers(t *testing.T) {
	pool := testMeterPool(t)
	pc := NewPeriodCloser(pool)

	scopeA := Scope{Operator: testOperator, Consumer: testConsumer, Protocol: "x402"}
	otherOperator := "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
	scopeB := Scope{Operator: otherOperator, Consumer: testConsumer, Protocol: "x402"}
	catalogB := singleVersionCatalog(t)

	maxAge := 720 * time.Hour
	oldEnough := time.Now().Add(-maxAge - time.Hour)
	periodA := insertOpenPeriodWithCreatedAt(t, pool, scopeA, 1, 1000, oldEnough)
	periodB := insertOpenPeriodWithCreatedAt(t, pool, scopeB, 1, 1000, oldEnough)

	catalogs := &fakeCatalogSource{
		catalogs: map[string]*Catalog{otherOperator: catalogB},
		errs:     map[string]error{testOperator: errors.New("price_book read failed")},
	}
	closed, err := pc.CloseDuePeriods(context.Background(), catalogs, 5000, maxAge)
	if err != nil {
		t.Fatalf("CloseDuePeriods returned unexpected error: %v", err)
	}
	if len(closed) != 1 || closed[0].ID != periodB {
		t.Fatalf("closed = %+v, want exactly period B (%d)", closed, periodB)
	}

	statusA, _ := periodStatus(t, pool, periodA)
	if statusA != "open" {
		t.Errorf("period A status = %q, want open (its catalog lookup failed, so it must be skipped, not closed)", statusA)
	}
}
