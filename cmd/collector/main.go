// Command collector runs Tallybook's HTTP metering layer: it wraps a paid
// API, records every request, prices it against the operator's published
// schedule, and holds custody of payment-channel commitments (CLAUDE.md
// §1).
//
// This file is composition only. Every behavior it wires together already
// exists, tested, in internal/... — internal/config for environment
// loading, internal/store for migrations and request persistence,
// internal/stellar for the Soroban RPC client, and internal/httpapi for
// the metering middleware itself. Nothing here is new business logic.
//
// What this file deliberately does NOT do: mount any actual protected
// route. Tallybook meters "a paid API" generically — CLAUDE.md never
// specifies what that API is, and §7 has no env var naming an upstream to
// reverse-proxy or a route table to serve. An operator wires their own
// handlers with httpapi.Middleware (see readyDependencies below for
// exactly what it needs); this binary's job is starting up cleanly,
// migrating the schema, and serving /healthz and /readyz while that
// wiring happens at the deployment layer.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/config"
	"github.com/Tallybook-Org/tallybook/internal/httpapi"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
	"github.com/Tallybook-Org/tallybook/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("collector: fatal", "error", err)
		os.Exit(1)
	}
}

// run does the actual work, taking ctx rather than constructing its own
// signal-derived one — main's job is exactly that construction; run's is
// everything else, which keeps run callable from a test with a
// test-controlled context instead of real OS signals.
func run(ctx context.Context) error {
	cfg, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)
	slog.Info("collector: starting", "config", cfg)

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	migrations, err := store.Migrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	applied, err := store.Migrate(ctx, pool, migrations)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	slog.Info("collector: migrations applied", "count", len(applied))

	stellarClient := stellar.NewClient(cfg.StellarRPCURL, nil)
	requestStore := store.NewRequestStore(pool)

	// readyDependencies is exactly what an operator's route wiring needs
	// to call httpapi.Middleware for a real protected endpoint: a
	// LedgerSource (here, a thin adapter over GetLatestLedger — there is
	// no literal CurrentLedger method on *stellar.Client, verified
	// against rpc.go, so every package in this codebase that needs one
	// expects this adapter at the wiring layer, not lower down) and a
	// Recorder (requestStore, already satisfying httpapi.Recorder).
	// Constructed here, ready to use, but not mounted to any handler —
	// see this file's own package doc comment for why.
	_ = httpapi.Config{
		Ledger:   ledgerSource{stellarClient},
		Recorder: requestStore,
		Operator: cfg.OperatorAddress,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: cfg.CollectorAddr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("collector: listening", "addr", cfg.CollectorAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("collector: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// ledgerSource adapts *stellar.Client to httpapi.LedgerSource.
type ledgerSource struct {
	client *stellar.Client
}

func (l ledgerSource) CurrentLedger(ctx context.Context) (uint32, error) {
	result, err := l.client.GetLatestLedger(ctx)
	if err != nil {
		return 0, err
	}
	return result.Sequence, nil
}
