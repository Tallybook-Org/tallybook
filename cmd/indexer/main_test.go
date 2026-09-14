package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// setSmokeTestEnv sets a complete, valid environment for run() using
// t.Setenv (automatically restored when the test ends), pointed at the
// same local docker-compose Postgres every other package's own
// integration tests use.
func setSmokeTestEnv(t *testing.T) {
	t.Helper()
	env := map[string]string{
		"TB_DATABASE_URL":           "postgres://tallybook:tallybook@localhost:5433/tallybook?sslmode=disable",
		"TB_STELLAR_RPC_URL":        "https://soroban-testnet.stellar.org",
		"TB_NETWORK_PASSPHRASE":     "Test SDF Network ; September 2015",
		"TB_PRICE_BOOK_ID":          "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW",
		"TB_STATEMENT_REGISTRY_ID":  "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M",
		"TB_OPERATOR_ADDRESS":       "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM",
		"TB_OPERATOR_SECRET_SOURCE": "env",
		"TB_OPERATOR_SECRET":        "S" + strings.Repeat("E", 55),
		"TB_SAFETY_MARGIN_LEDGERS":  "1440",
		"TB_MAX_EXPOSURE":           "10000000",
		"TB_MAX_EXPOSURE_AGE":       "24h",
		"TB_PERIOD_DURATION":        "720h",
		"TB_SETTLER_TICK":           "30s",
		"TB_INDEXER_START_LEDGER":   "4590000",
		"TB_COLLECTOR_ADDR":         ":8080",
		"TB_LOG_LEVEL":              "info",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func pingPostgresOrSkip(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "localhost:5433", 3*time.Second)
	if err != nil {
		t.Skipf("skipping: cannot reach local postgres (is `docker compose up -d` running?): %v", err)
	}
	conn.Close()
}

// TestRun_StartsAndStaysUpUntilCancelled is the smoke test: run() against
// real local Postgres must not exit on its own — the ingestion loop and
// the health server both come up, /healthz answers, and everything shuts
// down cleanly once the context is cancelled. The ingestion loop's first
// real getEvents call happens only once tickInterval elapses, well after
// this test has already confirmed liveness and moved to cancellation, so
// no live network round trip is required for the test to pass.
func TestRun_StartsAndStaysUpUntilCancelled(t *testing.T) {
	pingPostgresOrSkip(t)
	setSmokeTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx) }()

	var lastErr error
	up := false
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://localhost" + healthAddr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				up = true
				break
			}
		}
		lastErr = err
		select {
		case runErr := <-errCh:
			t.Fatalf("run() exited early with: %v", runErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !up {
		t.Fatalf("indexer never became healthy: %v", lastErr)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("run() returned an error after graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return within 5s of its context being cancelled")
	}
}
