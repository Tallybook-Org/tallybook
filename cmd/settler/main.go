// Command settler runs Tallybook's settler: the daemon that watches open
// payment channels and sweeps earned revenue on chain before the payer
// can reclaim it (CLAUDE.md §1, §6 — "the component that decides whether
// the operator gets paid").
//
// This file is composition only. internal/settle already has everything
// that matters — Calculator, Submitter, Policy, Daemon, Metrics — tested
// independently; this just constructs each from config and internal/stellar,
// wires them together, and runs the daemon loop with graceful shutdown.
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
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/config"
	"github.com/Tallybook-Org/tallybook/internal/settle"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
	"github.com/Tallybook-Org/tallybook/internal/store"
)

// metricsAddr is where the settler serves /metrics, /healthz, and
// /readyz. §7 has no env var for it — TB_COLLECTOR_ADDR is the
// collector's own listen address, not this binary's — so this is a
// fixed default rather than newly-invented required configuration.
const metricsAddr = ":9101"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("settler: fatal", "error", err)
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
	slog.Info("settler: starting", "config", cfg)

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
	slog.Info("settler: migrations applied", "count", len(applied))

	signer, err := keypair.ParseFull(cfg.OperatorSecret.Reveal())
	if err != nil {
		return fmt.Errorf("parse operator signing key: %w", err)
	}

	stellarClient := stellar.NewClient(cfg.StellarRPCURL, nil)
	calc := settle.NewCalculator(pool)
	submitter := settle.NewSubmitter(pool)

	// one-way-channel is deployed once per channel (§4's factory), so
	// there is no single shared binding the way price_book/
	// statement_registry have — Daemon needs a fresh *stellar.Channel per
	// address it watches, which is exactly what ChannelFactory is for.
	channelFactory := func(address string) settle.LiveChannel {
		return stellar.NewChannel(stellarClient, address, cfg.NetworkPassphrase)
	}

	policy := settle.Policy{
		SafetyMarginLedgers: cfg.SafetyMarginLedgers,
		MaxExposure:         cfg.MaxExposure,
		MaxExposureAge:      cfg.MaxExposureAge,
	}
	daemon := settle.NewDaemon(pool, calc, submitter, channelFactory, signer, ledgerSource{stellarClient}, policy)
	metrics := settle.NewMetrics(pool, ledgerSource{stellarClient}, daemon)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
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
	srv := &http.Server{Addr: metricsAddr, Handler: mux}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("settler: daemon loop starting", "tick", cfg.SettlerTick)
		daemon.Run(ctx, cfg.SettlerTick)
		slog.Info("settler: daemon loop stopped")
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("settler: metrics listening", "addr", metricsAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("settler: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		wg.Wait() // daemon.Run itself returns as soon as ctx is done
		return shutdownErr
	case err := <-errCh:
		return err
	}
}

// ledgerSource adapts *stellar.Client to settle.LedgerSource.
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
