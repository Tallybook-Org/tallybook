// Command indexer runs Tallybook's chain indexer: it ingests Soroban
// events into Postgres so metered usage can be reconciled against what
// actually settled (CLAUDE.md §1).
//
// This file is composition only. internal/indexer's Ingestor already does
// everything — event classification, channel state tracking, cursor
// persistence, reconciliation queries — tested independently; this just
// constructs it from config and internal/stellar and runs its loop with
// graceful shutdown.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/config"
	"github.com/Tallybook-Org/tallybook/internal/indexer"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
	"github.com/Tallybook-Org/tallybook/internal/store"
)

// healthAddr is where the indexer serves /healthz and /readyz. §7 has no
// env var for it — TB_COLLECTOR_ADDR belongs to a different binary — so
// this is a fixed default rather than newly-invented required
// configuration, the same call cmd/settler makes for its own :9101.
const healthAddr = ":9102"

// tickInterval is how often Ingestor.Run polls getEvents. §7 has no
// env var for this either; a fixed, reasonable default rather than
// inventing required configuration for it.
const tickInterval = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("indexer: fatal", "error", err)
		os.Exit(1)
	}
}

// run does the actual work, taking ctx rather than constructing its own
// signal-derived one internally — see cmd/collector's identical pattern
// and its own doc comment for why.
func run(ctx context.Context) error {
	cfg, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)
	slog.Info("indexer: starting", "config", cfg)

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
	slog.Info("indexer: migrations applied", "count", len(applied))

	stellarClient := stellar.NewClient(cfg.StellarRPCURL, nil)
	ix, err := indexer.New(indexer.Config{
		Client:              stellarClient,
		Pool:                pool,
		PriceBookID:         cfg.PriceBookID,
		StatementRegistryID: cfg.StatementRegistryID,
		OperatorAddress:     cfg.OperatorAddress,
		StartLedger:         cfg.IndexerStartLedger,
	})
	if err != nil {
		return fmt.Errorf("construct ingestor: %w", err)
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
	srv := &http.Server{Addr: healthAddr, Handler: mux}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("indexer: ingestion loop starting", "tick", tickInterval)
		ix.Run(ctx, tickInterval)
		slog.Info("indexer: ingestion loop stopped")
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("indexer: health server listening", "addr", healthAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("indexer: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		wg.Wait() // Ingestor.Run itself returns as soon as ctx is done
		return shutdownErr
	case err := <-errCh:
		return err
	}
}
