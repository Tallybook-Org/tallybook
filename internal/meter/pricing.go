package meter

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// Sentinel errors for Catalog and Pricer.
var (
	// ErrNoVersionAt is returned when ledger predates every published
	// version in the catalog — there is nothing price_book.version_at (§4)
	// would have returned either, since the operator's first version was
	// not yet effective.
	ErrNoVersionAt = errors.New("meter: no price book version was effective at this ledger")

	// ErrUnknownEndpoint is returned when the version that applies at a
	// request's ledger has no rule for its (method, path_template).
	ErrUnknownEndpoint = errors.New("meter: no price schedule rule for this endpoint")

	// ErrEmptyCatalog is returned by NewCatalog for zero entries.
	ErrEmptyCatalog = errors.New("meter: catalog has no versions")

	// ErrDuplicateVersion is returned by NewCatalog when two entries share a
	// version number or an effective ledger.
	ErrDuplicateVersion = errors.New("meter: catalog has two entries for the same version or effective ledger")

	// ErrNilSchedule is returned by NewCatalog when an entry's Schedule is nil.
	ErrNilSchedule = errors.New("meter: catalog entry has a nil schedule")
)

// CatalogVersion is one published price_book version as far as pricing
// cares: which version number it is, the ledger it became effective on, and
// the validated Schedule that was live at that version.
type CatalogVersion struct {
	Version         uint32
	EffectiveLedger uint32
	Schedule        *Schedule
}

// Catalog is an operator's full published price-schedule history — every
// version price_book has ever recorded, each with the ledger it took effect
// on. VersionAt mirrors price_book.version_at's semantics (§4) locally, so
// the collector can decide which version prices a request observed at a
// given ledger without a chain round trip on every request.
type Catalog struct {
	versions []CatalogVersion // sorted by EffectiveLedger ascending
}

// NewCatalog validates entries and returns a Catalog. entries must be
// non-empty, have no two entries sharing a version number or an effective
// ledger, and have no nil Schedule. Order does not matter — NewCatalog
// sorts by EffectiveLedger internally.
func NewCatalog(entries ...CatalogVersion) (*Catalog, error) {
	if len(entries) == 0 {
		return nil, ErrEmptyCatalog
	}

	versions := make([]CatalogVersion, len(entries))
	copy(versions, entries)
	sort.Slice(versions, func(i, j int) bool { return versions[i].EffectiveLedger < versions[j].EffectiveLedger })

	seenVersion := make(map[uint32]bool, len(versions))
	seenLedger := make(map[uint32]bool, len(versions))
	for _, v := range versions {
		if v.Schedule == nil {
			return nil, fmt.Errorf("%w: version %d", ErrNilSchedule, v.Version)
		}
		if seenVersion[v.Version] {
			return nil, fmt.Errorf("%w: version %d appears twice", ErrDuplicateVersion, v.Version)
		}
		if seenLedger[v.EffectiveLedger] {
			return nil, fmt.Errorf("%w: effective ledger %d appears twice", ErrDuplicateVersion, v.EffectiveLedger)
		}
		seenVersion[v.Version] = true
		seenLedger[v.EffectiveLedger] = true
	}

	return &Catalog{versions: versions}, nil
}

// VersionAt returns the version that was effective at ledger: the entry
// with the greatest EffectiveLedger that is <= ledger. It returns
// ErrNoVersionAt if ledger predates the earliest entry.
func (c *Catalog) VersionAt(ledger uint32) (*CatalogVersion, error) {
	// versions is sorted ascending by EffectiveLedger; walk backward for the
	// last one that's still <= ledger. The catalog is expected to hold at
	// most a handful of versions per operator, so a linear scan needs no
	// binary search to stay cheap.
	for i := len(c.versions) - 1; i >= 0; i-- {
		if c.versions[i].EffectiveLedger <= ledger {
			v := c.versions[i]
			return &v, nil
		}
	}
	return nil, fmt.Errorf("%w: ledger %d, earliest version effective at %d",
		ErrNoVersionAt, ledger, c.versions[0].EffectiveLedger)
}

// EffectiveLedgerAfter returns the effective ledger of the version that
// immediately follows version (by EffectiveLedger order), and whether one
// exists at all — false if version is unknown to this catalog, or is the
// latest one in it.
//
// This exists for period closing (internal/meter's PeriodCloser): when a
// period opened under version needs to close because the catalog has
// since moved on, the correct closing boundary is the ledger just before
// the version that came right after version — not the ledger of
// whichever version happens to be current now, which could be several
// versions further along if nothing was observed in between. Using
// "current now" there would make the closed period's own range span
// multiple versions, exactly the invariant closing exists to prevent.
func (c *Catalog) EffectiveLedgerAfter(version uint32) (uint32, bool) {
	for i, v := range c.versions {
		if v.Version == version {
			if i+1 < len(c.versions) {
				return c.versions[i+1].EffectiveLedger, true
			}
			return 0, false
		}
	}
	return 0, false
}

// PricedRequest is the result of pricing one request: everything a
// requests row (§5) needs from the pricing step specifically.
type PricedRequest struct {
	ChargedAmount *big.Int
	PriceVersion  uint32
}

// Pricer prices individual requests against a Catalog.
type Pricer struct {
	catalog *Catalog
}

// NewPricer returns a Pricer backed by catalog.
func NewPricer(catalog *Catalog) *Pricer {
	return &Pricer{catalog: catalog}
}

// Price computes the charge for one request of units units against
// method+pathTemplate, using whichever price book version was effective at
// ledger. It returns ErrNoVersionAt if ledger predates every published
// version, or ErrUnknownEndpoint if the version effective at ledger has no
// rule for this endpoint.
func (p *Pricer) Price(ledger uint32, method, pathTemplate string, units uint64) (*PricedRequest, error) {
	version, err := p.catalog.VersionAt(ledger)
	if err != nil {
		return nil, fmt.Errorf("meter: price: %w", err)
	}

	unitPrice, ok := version.Schedule.Lookup(method, pathTemplate)
	if !ok {
		return nil, fmt.Errorf("meter: price: %w: %s %s (price book version %d)",
			ErrUnknownEndpoint, method, pathTemplate, version.Version)
	}

	amount := new(big.Int).Mul(unitPrice, new(big.Int).SetUint64(units))
	return &PricedRequest{ChargedAmount: amount, PriceVersion: version.Version}, nil
}
