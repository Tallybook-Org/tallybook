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
	"errors"
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
	"github.com/Tallybook-Org/tallybook/internal/httpserver"
	"github.com/Tallybook-Org/tallybook/internal/indexer"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
	"github.com/Tallybook-Org/tallybook/internal/store"
)

// healthAddrDefault is the address the indexer falls back to when
// TB_INDEXER_ADDR is unset. §7 named no env var for it — TB_COLLECTOR_ADDR
// belongs to a different binary — so this binary shipped with a fixed
// value, which the variable now makes adjustable without changing what
// an existing deployment does. The poll interval it shipped with is
// config.DefaultIndexerTick, adjustable through TB_INDEXER_TICK.
const healthAddrDefault = config.DefaultIndexerAddr

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
	mux.Handle("GET /metrics", indexer.NewMetrics(ix))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if failures := ix.ConsecutiveTickFailures(); failures >= uint64(cfg.IndexerMaxConsecutiveFailures) {
			http.Error(w, fmt.Sprintf("indexer ingestion unhealthy: %d consecutive tick failures", failures), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv, err := httpserver.New(cfg.IndexerAddr, mux, httpserver.Timeouts{
		ReadHeader: cfg.HTTPReadHeaderTimeout,
		Read:       cfg.HTTPReadTimeout,
		Write:      cfg.HTTPWriteTimeout,
		Idle:       cfg.HTTPIdleTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct HTTP server: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("indexer: ingestion loop starting", "tick", cfg.IndexerTick)
		ix.Run(ctx, cfg.IndexerTick)
		slog.Info("indexer: ingestion loop stopped")
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("indexer: health server listening", "addr", cfg.IndexerAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
