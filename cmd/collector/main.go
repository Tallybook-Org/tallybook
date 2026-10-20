// Command collector runs Tallybook's HTTP metering layer: it wraps a paid
// API, records every request, prices it against the operator's published
// schedule, and holds custody of payment-channel commitments (CLAUDE.md
// §1).
//
// This file is composition only. Every behavior it wires together already
// exists, tested, in internal/... — internal/config for environment
// loading, internal/store for migrations and request persistence,
// internal/stellar for the Soroban RPC client, internal/pricebook for the
// operator's published schedule, internal/meter for pricing and the
// billing-period lifecycle, internal/anchor for anchoring closed periods,
// and internal/httpapi for the metering middleware itself. Nothing here
// is new business logic.
//
// What this file deliberately does NOT do: name a specific upstream to
// reverse-proxy. Tallybook meters "a paid API" generically — CLAUDE.md
// never specifies what that API is, and §7 has no env var naming an
// upstream. This binary therefore serves one example metered route (see
// routes.go) and exposes the machinery an operator's own route
// registration needs: httpapi.Middleware and its dependencies.
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
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/anchor"
	"github.com/Tallybook-Org/tallybook/internal/config"
	"github.com/Tallybook-Org/tallybook/internal/httpserver"
	"github.com/Tallybook-Org/tallybook/internal/meter"
	"github.com/Tallybook-Org/tallybook/internal/pricebook"
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

// catalogWarmupTimeout bounds the collector's startup warm-up of the
// operator's price catalog. The warm-up is an optimization — the
// background refresher keeps trying either way — so it must not hold the
// collector's startup for longer than this.
const catalogWarmupTimeout = 10 * time.Second

// run does the actual work, taking ctx rather than constructing its own
// signal-derived one internally — main's job is exactly that construction;
// run's is everything else, which keeps run callable from a test with a
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

	// The billing-period lifecycle: EnsureOpenPeriod opens the period a
	// request belongs to, CloseDuePeriods closes the ones that have run
	// their course (on a price version change, or on calendar grounds
	// alone — §4's constraint 1 rejects a statement whose period spans a
	// price change).
	periodCloser := meter.NewPeriodCloser(pool)

	// The operator's published price schedule: read off the chain, fetched
	// over HTTP, verified against the hash the chain recorded, and cached
	// so a metered request never waits on either.
	priceBook := stellar.NewPriceBook(stellarClient, cfg.PriceBookID, cfg.NetworkPassphrase)
	catalogs, err := pricebook.NewSource(pricebook.Config{
		Book: priceBook,
		Fetch: pricebook.NewHTTPFetcher(&http.Client{
			Timeout: pricebook.DefaultHTTPTimeout,
		}).Fetch,
	})
	if err != nil {
		return fmt.Errorf("construct price book source: %w", err)
	}

	// Anchoring: a closed period becomes a statement on chain, with the
	// merkle-rooted usage the consumer can verify (internal/meter).
	anchorer := meter.NewAnchorer(pool)
	registry := stellar.NewStatementRegistry(stellarClient, cfg.StatementRegistryID, cfg.NetworkPassphrase)
	signer, err := keypair.ParseFull(cfg.OperatorSecret.Reveal())
	if err != nil {
		return fmt.Errorf("parse operator signing key: %w", err)
	}
	anchorDriver, err := anchor.NewDriver(anchor.Config{
		Anchorer: anchorer,
		Registry: registry,
		Reader:   registry,
		Signer:   signer,
		Token:    cfg.AnchorToken,
		Pool:     pool,
	})
	if err != nil {
		return fmt.Errorf("construct anchor driver: %w", err)
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

	// The metered routes. The example route's caller identity comes from
	// a request header, which is not authentication — see routes.go.
	err = registerRoutes(mux, routeDeps{
		Operator: cfg.OperatorAddress,
		Catalogs: catalogs,
		Ledger:   ledgerSource{stellarClient},
		Recorder: requestStore,
		Periods:  periodCloser,
	}, exampleRoutes())
	if err != nil {
		return err
	}
	slog.Warn("collector: serving the example metered route /v1/example, which takes the caller's identity from a request header; replace it with a real API and real authentication before exposing this collector")

	srv, err := httpserver.New(cfg.CollectorAddr, mux, httpserver.Timeouts{
		ReadHeader: cfg.HTTPReadHeaderTimeout,
		Read:       cfg.HTTPReadTimeout,
		Write:      cfg.HTTPWriteTimeout,
		Idle:       cfg.HTTPIdleTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct HTTP server: %w", err)
	}

	// Warm the catalog before serving: the first request would otherwise
	// be the one that waits for the chain, and the collector's readiness
	// would depend on the price book being readable at that moment. The
	// warm-up gets its own short deadline — it is an optimization, not a
	// precondition, and a price book that cannot be read yet must not
	// hold the collector's startup hostage.
	warmupCtx, cancelWarmup := context.WithTimeout(ctx, catalogWarmupTimeout)
	if _, err := catalogs.Refresh(warmupCtx, cfg.OperatorAddress); err != nil {
		slog.Warn("collector: could not read the operator's price book yet; the collector will keep trying",
			"operator", cfg.OperatorAddress, "error", err)
	}
	cancelWarmup()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		slog.Info("collector: price book refresh loop starting", "interval", catalogs.TTL())
		catalogs.Run(ctx)
		slog.Info("collector: price book refresh loop stopped")
	}()
	go func() {
		defer wg.Done()
		slog.Info("collector: period closer loop starting", "tick", cfg.CollectorPeriodTick)
		periodCloser.Run(ctx, ledgerSource{stellarClient}, catalogs, cfg.PeriodDuration, cfg.CollectorPeriodTick)
		slog.Info("collector: period closer loop stopped")
	}()
	go func() {
		defer wg.Done()
		// Reconcile before anchoring: §6's ordering rule 3 — an attempt
		// left unresolved by a crash has to be settled against the chain
		// before anything new is submitted.
		if _, err := anchorDriver.ReconcileUnresolved(ctx); err != nil {
			slog.Error("collector: reconcile unresolved anchor attempts", "error", err)
		}
		slog.Info("collector: anchor loop starting", "tick", cfg.CollectorAnchorTick)
		anchorDriver.Run(ctx, cfg.CollectorAnchorTick)
		slog.Info("collector: anchor loop stopped")
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("collector: listening", "addr", cfg.CollectorAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("collector: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		wg.Wait() // every loop above returns as soon as ctx is done
		return shutdownErr
	case err := <-errCh:
		return err
	}
}

// ledgerSource adapts *stellar.Client to httpapi.LedgerSource and
// meter.LedgerSource.
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
