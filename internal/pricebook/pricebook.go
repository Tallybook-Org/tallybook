// Package pricebook assembles a meter.Catalog from the price_book
// contract's published schedule history.
//
// The collector prices every request against the schedule its operator
// published on chain (price_book, §4). price_book itself only stores the
// hash and the URI of each published version; the schedule document
// itself lives wherever the operator put it. This package is the adapter
// between the two: it reads an operator's published versions off the
// chain, fetches each version's schedule document over HTTP, verifies
// the document against the hash the chain recorded, parses it, and
// assembles the whole history into the catalog internal/meter prices
// against.
//
// Two properties matter more than anything else here:
//
//   - The hash is checked before the document is trusted. price_book
//     records the sha256 of the document as published; a document whose
//     hash does not match is not the operator's schedule, whatever it
//     says, and is refused rather than priced against.
//
//   - The request path never waits on the chain. A catalog is cached per
//     operator and refreshed in the background; a request reads the
//     cached catalog. Fetching the schedule on every request would make
//     every metered request latency-bound on a chain read and an HTTP
//     fetch, which is not a price this codebase is willing to pay for
//     billing bookkeeping.
package pricebook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tallybook-Org/tallybook/internal/meter"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

// Sentinel errors this package returns. Wrapped with %w alongside the
// specific version and operator, so a caller can match the class of
// failure with errors.Is while a human still gets a message naming what
// failed.
var (
	// ErrNoVersions means the operator has published no price book
	// version at all — there is no schedule to price anything against,
	// and nothing this package can do about it.
	ErrNoVersions = errors.New("pricebook: operator has published no price book version")

	// ErrScheduleHashMismatch means the fetched document's hash does not
	// match the schedule_hash price_book recorded for that version. The
	// document is not the operator's published schedule and is refused.
	ErrScheduleHashMismatch = errors.New("pricebook: fetched schedule does not match the published schedule_hash")

	// ErrVersionWentBackwards means price_book now reports an older
	// latest version than the one already cached — a price book that
	// un-publishes is not something this package knows how to interpret,
	// and continuing would silently re-price requests against a schedule
	// that may no longer be the operator's.
	ErrVersionWentBackwards = errors.New("pricebook: price book version went backwards")

	// ErrUnsupportedScheme means a version's URI is not an http or https
	// URL. The document is fetched over the network; a file:// or
	// data: URI is not something this package will read.
	ErrUnsupportedScheme = errors.New("pricebook: schedule URI must be an http or https URL")
)

// DefaultTTL is how long a cached catalog is served before a refresh is
// triggered, when the caller does not choose otherwise. It bounds how
// long the collector can keep pricing against a schedule the operator
// has already replaced — the price book takes effect on a future ledger
// (§4: a version cannot be published into the past), so a version
// published during this window is not yet in effect and the lag costs
// nothing; the lag that matters is bounded by this.
const DefaultTTL = 30 * time.Second

// backgroundRefreshTimeout bounds one background refresh. It is
// deliberately independent of the TTL: a refresh that re-reads a long
// version history, or one that fetches a schedule document from a slow
// origin, must not be cut short by the freshness window that decided to
// start it.
const backgroundRefreshTimeout = 2 * time.Minute

// VersionReader is the read side of the price_book contract this package
// needs. *stellar.PriceBook satisfies it.
type VersionReader interface {
	// Latest returns operator's current price book version number.
	Latest(ctx context.Context, operator string) (uint32, error)
	// GetVersion returns the full published record for operator's version.
	GetVersion(ctx context.Context, operator string, version uint32) (*stellar.PriceBookVersion, error)
}

// FetchFunc retrieves a published schedule document by URI. It is the
// seam the tests substitute: the production implementation is HTTPFetcher.
type FetchFunc func(ctx context.Context, uri string) ([]byte, error)

// Config configures a Source.
type Config struct {
	// Book reads the published versions off the chain. Required.
	Book VersionReader
	// Fetch retrieves a version's schedule document. Required.
	Fetch FetchFunc
	// TTL is how long a cached catalog is served before a refresh is
	// triggered. Zero means DefaultTTL.
	TTL time.Duration
	// Now is the clock the staleness check reads. Nil means
	// time.Now — the tests substitute their own.
	Now func() time.Time
}

// Source reads an operator's published schedule history and serves it as
// a meter.Catalog. It implements meter.CatalogSource, so it can be
// handed straight to the places that need one — the metering middleware,
// which prices against it, and the period closer, which decides a
// period's closing boundary from it.
//
// A Source is safe for concurrent use.
type Source struct {
	book  VersionReader
	fetch FetchFunc
	ttl   time.Duration
	now   func() time.Time

	// mu guards entries only — the map of operator to its cached
	// catalog. Everything inside an entry has its own synchronisation.
	mu      sync.Mutex
	entries map[string]*operatorEntry
}

// operatorEntry is one operator's cached catalog.
//
// The catalog pointer is atomic so the request path can read it without
// taking a lock and without ever seeing a half-built catalog: a refresh
// builds a whole new catalog and swaps the pointer in one store.
type operatorEntry struct {
	// refreshMu serializes refreshes for this operator, so two
	// concurrent refreshes never fetch the same version twice.
	refreshMu sync.Mutex
	// refreshing records whether a background refresh is already in
	// flight, so a burst of requests arriving on a stale cache starts
	// one refresh rather than one per request.
	refreshing atomic.Bool
	catalog    atomic.Pointer[meter.Catalog]
	// versions, latest and fetchedAt are guarded by refreshMu, except
	// fetchedAt, which the request path reads without the lock.
	versions  []meter.CatalogVersion
	latest    uint32
	fetchedAt atomic.Int64
}

// NewSource returns a Source that reads through book and fetch. It
// fails on a missing dependency rather than returning a Source that
// would nil-panic on its first read: this is startup-time wiring.
func NewSource(cfg Config) (*Source, error) {
	if cfg.Book == nil {
		return nil, errors.New("pricebook: source config: book is nil")
	}
	if cfg.Fetch == nil {
		return nil, errors.New("pricebook: source config: fetch is nil")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Source{
		book:    cfg.Book,
		fetch:   cfg.Fetch,
		ttl:     ttl,
		now:     now,
		entries: make(map[string]*operatorEntry),
	}, nil
}

// TTL returns how long a cached catalog is served before a refresh is
// triggered.
func (s *Source) TTL() time.Duration {
	return s.ttl
}

// entryFor returns the cache entry for operator, creating it if this is
// the first time the operator has been asked about.
func (s *Source) entryFor(operator string) *operatorEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[operator]
	if !ok {
		entry = &operatorEntry{}
		s.entries[operator] = entry
	}
	return entry
}

// CatalogFor returns operator's price catalog.
//
// It never waits on the chain once a catalog has been loaded: a cached
// catalog is returned as it is, and a refresh is started in the
// background if the cache has gone stale. The very first call for an
// operator has nothing to serve, so it does wait for the chain — there
// is no schedule to price against until it arrives, and failing the
// request closed is better than serving it unmetered.
//
// A catalog served from a stale cache is still a coherent catalog: a
// price book's versions are immutable, so a stale catalog prices
// requests against a schedule the operator has published, just possibly
// not the newest one. The background refresh closes that gap.
func (s *Source) CatalogFor(ctx context.Context, operator string) (*meter.Catalog, error) {
	entry := s.entryFor(operator)

	catalog := entry.catalog.Load()
	if catalog == nil {
		return s.refresh(ctx, operator, entry)
	}
	if s.stale(entry) {
		s.refreshInBackground(ctx, operator, entry)
	}
	return catalog, nil
}

// Refresh reloads operator's catalog from the chain, whatever the
// cache's freshness, and returns it. It is what the background loop
// calls; a caller that needs the very latest published version right
// now can call it directly.
func (s *Source) Refresh(ctx context.Context, operator string) (*meter.Catalog, error) {
	return s.refresh(ctx, operator, s.entryFor(operator))
}

// Run keeps every operator the source has been asked about fresh,
// refreshing each one's catalog once per TTL, until ctx is cancelled. A
// failed refresh is logged and retried on the next tick, never fatal —
// matching every other daemon loop in this codebase: a paused refresh
// means the collector keeps pricing against the last schedule it could
// read, not that the process should give up.
func (s *Source) Run(ctx context.Context) {
	ticker := time.NewTicker(s.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		for _, operator := range s.operators() {
			if _, err := s.refresh(ctx, operator, s.entryFor(operator)); err != nil {
				slog.ErrorContext(ctx, "pricebook: refresh catalog", "operator", operator, "error", err)
			}
		}
	}
}

// operators returns the operators the source knows about.
func (s *Source) operators() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for operator := range s.entries {
		out = append(out, operator)
	}
	return out
}

// stale reports whether the entry's catalog is older than the TTL.
func (s *Source) stale(entry *operatorEntry) bool {
	fetchedAt := entry.fetchedAt.Load()
	if fetchedAt == 0 {
		return true
	}
	return s.now().Sub(time.Unix(0, fetchedAt)) >= s.ttl
}

// refreshInBackground starts a refresh that does not block the caller.
// The refresh outlives the request that triggered it — its context is
// detached from the request's cancellation — and at most one is in
// flight per operator at a time.
func (s *Source) refreshInBackground(ctx context.Context, operator string, entry *operatorEntry) {
	if !entry.refreshing.CompareAndSwap(false, true) {
		return
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundRefreshTimeout)
	go func() {
		defer cancel()
		defer entry.refreshing.Store(false)
		if _, err := s.refresh(refreshCtx, operator, entry); err != nil {
			slog.ErrorContext(refreshCtx, "pricebook: refresh catalog", "operator", operator, "error", err)
		}
	}()
}

// refresh reads operator's published versions off the chain, fetches and
// verifies the schedule document of every version it has not seen yet,
// and assembles the result into a catalog.
//
// Only versions newer than the ones already cached are fetched: a
// published version is immutable, so re-reading and re-verifying the
// whole history on every refresh would be work with no possible outcome
// other than the same catalog.
func (s *Source) refresh(ctx context.Context, operator string, entry *operatorEntry) (*meter.Catalog, error) {
	entry.refreshMu.Lock()
	defer entry.refreshMu.Unlock()

	latest, err := s.book.Latest(ctx, operator)
	if err != nil {
		return nil, fmt.Errorf("pricebook: latest version for %s: %w", operator, err)
	}
	if latest == 0 {
		return nil, fmt.Errorf("pricebook: %s: %w", operator, ErrNoVersions)
	}
	if latest < entry.latest {
		return nil, fmt.Errorf("pricebook: %s: %w: chain reports %d, already have %d",
			operator, ErrVersionWentBackwards, latest, entry.latest)
	}

	versions := entry.versions
	added := false
	for version := entry.latest + 1; version <= latest; version++ {
		published, err := s.book.GetVersion(ctx, operator, version)
		if err != nil {
			return nil, fmt.Errorf("pricebook: version %d of %s: %w", version, operator, err)
		}
		schedule, err := s.scheduleFor(ctx, published)
		if err != nil {
			return nil, fmt.Errorf("pricebook: version %d of %s: %w", version, operator, err)
		}
		versions = append(versions, meter.CatalogVersion{
			Version:         published.Version,
			EffectiveLedger: published.EffectiveLedger,
			Schedule:        schedule,
		})
		added = true
	}

	if !added {
		// Nothing new was published. The cached catalog is still the
		// right one; only its freshness changes.
		entry.fetchedAt.Store(s.now().UnixNano())
		cached := entry.catalog.Load()
		if cached == nil {
			// Unreachable: a cached latest version implies a cached
			// catalog, the two are stored together. Guarded anyway,
			// because a nil catalog handed back with a nil error would
			// be dereferenced by the caller.
			return nil, fmt.Errorf("pricebook: %s: no catalog for the versions already fetched", operator)
		}
		return cached, nil
	}

	catalog, err := meter.NewCatalog(versions...)
	if err != nil {
		return nil, fmt.Errorf("pricebook: catalog for %s: %w", operator, err)
	}

	entry.versions = versions
	entry.latest = latest
	entry.fetchedAt.Store(s.now().UnixNano())
	entry.catalog.Store(catalog)
	return catalog, nil
}

// scheduleFor fetches a published version's schedule document, verifies
// it against the hash the chain recorded for that version, and parses it.
//
// The verification comes first: a document whose hash does not match is
// not the operator's schedule, and parsing it would only mean pricing
// requests against something the operator never published.
func (s *Source) scheduleFor(ctx context.Context, published *stellar.PriceBookVersion) (*meter.Schedule, error) {
	raw, err := s.fetch(ctx, published.URI)
	if err != nil {
		return nil, fmt.Errorf("fetch schedule from %q: %w", published.URI, err)
	}
	if got := meter.ScheduleHash(raw); got != published.ScheduleHash {
		return nil, fmt.Errorf("%w: published %x, fetched %x",
			ErrScheduleHashMismatch, published.ScheduleHash, got)
	}
	schedule, err := meter.ParseSchedule(raw)
	if err != nil {
		return nil, fmt.Errorf("parse schedule: %w", err)
	}
	return schedule, nil
}
