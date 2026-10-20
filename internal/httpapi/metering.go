// Package httpapi is the collector's HTTP surface: the metering middleware
// that wraps a paid API, records every request, prices it against the
// operator's published schedule, and files it under the billing period it
// belongs to (§1). It authenticates and verifies payment for nothing —
// that is a facilitator's job, explicitly out of scope here (§1's
// non-goals) — it only meters requests that reach it, trusting whatever
// upstream middleware already decided the caller is allowed to be here.
package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"

	"github.com/Tallybook-Org/tallybook/internal/merkle"
	"github.com/Tallybook-Org/tallybook/internal/meter"
)

// Protocol names, exactly as the requests table's protocol column and its
// CHECK constraint expect (§5).
const (
	ProtocolX402       = "x402"
	ProtocolMPPCharge  = "mpp_charge"
	ProtocolMPPSession = "mpp_session"
)

// LedgerSource supplies the current ledger sequence the metering middleware
// stamps onto every recorded request (observed_ledger) and prices against
// (meter.Catalog.VersionAt mirrors price_book.version_at, §4). Implemented
// by internal/stellar.Client in production; a narrow interface here keeps
// this package from depending on the Soroban RPC client directly.
type LedgerSource interface {
	CurrentLedger(ctx context.Context) (uint32, error)
}

// CatalogSource resolves the price catalog an operator has published — the
// versioned schedule history the requests are priced against
// (internal/meter's Catalog). Implemented in production by a reader that
// fetches the operator's price_book versions from the chain and their
// schedule documents over HTTP; a narrow interface here keeps this package
// from depending on either.
//
// The middleware resolves exactly one catalog per request and uses it for
// both the price it charges and the billing period the request belongs
// to. That is deliberate: the two must agree on which price book version
// was effective at the request's ledger, or the amount charged and the
// version the period is recorded under would disagree — and a statement
// whose period and amounts disagree is one the chain rejects.
type CatalogSource interface {
	CatalogFor(ctx context.Context, operator string) (*meter.Catalog, error)
}

// PeriodOpener returns the billing period a request observed at ledger
// belongs to, opening one if none is open yet and closing the open one if
// the price book version in effect has changed (meter.PeriodCloser).
// Implemented by internal/meter's PeriodCloser in production; a narrow
// interface here keeps this package from depending on the database
// driver directly.
//
// Every recorded request belongs to a period: the periods are what the
// operator's statements are built from, so a request with no period would
// be revenue that can never be billed.
type PeriodOpener interface {
	EnsureOpenPeriod(ctx context.Context, scope meter.Scope, ledger uint32, catalog *meter.Catalog) (*meter.Period, error)
}

// Recorder persists one metered request. Implemented by internal/store's
// request-persistence type in production; a narrow interface here keeps
// this package from depending on the database driver directly.
type Recorder interface {
	RecordRequest(ctx context.Context, req RecordedRequest) error
}

// RecordedRequest is everything the metering middleware determines about
// one request, in the shape the requests table (§5) needs.
type RecordedRequest struct {
	RequestID      [32]byte
	Operator       string
	Consumer       string
	EndpointHash   [32]byte
	Method         string
	PathTemplate   string
	UnitCount      uint64
	PriceVersion   uint32
	ChargedAmount  *big.Int
	Protocol       string
	Channel        string // non-empty iff Protocol == ProtocolMPPSession
	ObservedLedger uint32
	// PeriodID is the billing period this request rolls up into. It is
	// set by the middleware, never by the caller: the middleware opens the
	// period (or finds the open one) before recording, so a request is
	// always recorded against the period its amount was priced under.
	PeriodID int64
}

// RequestInfo is what the metering middleware needs to know about the
// caller of one request. It is established upstream of metering — by
// whatever verified payment, a non-goal of this package (§1) — and only
// read here, via WithRequestInfo/RequestInfoFromContext.
type RequestInfo struct {
	Consumer string
	Protocol string // one of the Protocol* constants
	Channel  string // non-empty iff Protocol == ProtocolMPPSession
}

// Sentinel errors for RequestInfo.Validate, one per rejection class, so a
// caller can match the reason with errors.Is instead of parsing the
// message; the wrapping still names the offending value. They mirror the
// requests table's own CHECK constraint (§5) — see Validate.
var (
	ErrConsumerEmpty   = errors.New("httpapi: request info: consumer is empty")
	ErrUnknownProtocol = errors.New("httpapi: request info: unknown protocol")
	ErrChannelMismatch = errors.New("httpapi: request info: channel does not match protocol")
)

// Validate reports whether info is well-formed enough to record: Consumer
// and Protocol set, Protocol one of the three known values, and Channel set
// iff Protocol is ProtocolMPPSession. This mirrors the requests table's own
// CHECK constraint (§5) so a malformed RequestInfo fails loudly here,
// naming the problem, rather than surfacing later as an opaque database
// constraint violation.
//
// Each rejection is one of ErrConsumerEmpty, ErrUnknownProtocol, or
// ErrChannelMismatch, identified with errors.Is.
func (info RequestInfo) Validate() error {
	if info.Consumer == "" {
		return ErrConsumerEmpty
	}
	switch info.Protocol {
	case ProtocolX402, ProtocolMPPCharge, ProtocolMPPSession:
	default:
		return fmt.Errorf("%w: must be one of %q, %q, %q, got %q",
			ErrUnknownProtocol, ProtocolX402, ProtocolMPPCharge, ProtocolMPPSession, info.Protocol)
	}
	hasChannel := info.Channel != ""
	isSession := info.Protocol == ProtocolMPPSession
	if hasChannel != isSession {
		return fmt.Errorf("%w: channel must be set if and only if protocol is %q (channel=%q, protocol=%q)",
			ErrChannelMismatch, ProtocolMPPSession, info.Channel, info.Protocol)
	}
	return nil
}

type requestInfoKey struct{}

// WithRequestInfo attaches info to ctx for the metering middleware to read.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// RequestInfoFromContext retrieves the RequestInfo attached by
// WithRequestInfo, if any.
func RequestInfoFromContext(ctx context.Context) (RequestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info, ok
}

// Config configures metering for one route.
type Config struct {
	// Operator is the address whose published schedule prices this route.
	Operator string
	// Method and PathTemplate identify the endpoint. They are supplied
	// explicitly, not discovered from the request: net/http's ServeMux does
	// not expose the pattern that matched a request as of Go 1.25 (verified
	// against its docs — there is no Request.Pattern or equivalent
	// accessor, only Request.PathValue for a wildcard's matched value), and
	// the caller already knows this mapping at registration time. It must
	// exactly match the template used everywhere else this endpoint's
	// identity matters: the price schedule (meter.Schedule.Lookup) and the
	// merkle leaf's endpoint hash (merkle.EndpointHash) both key on this
	// same (method, path_template) pair.
	Method       string
	PathTemplate string
	// Catalogs resolves the operator's published price schedule, and
	// Periods opens (or finds) the billing period the request belongs to.
	// Both are required: a request that cannot be priced, or that has no
	// period to roll up into, is revenue the operator can never bill, so
	// the middleware fails the request closed rather than serving it
	// unmetered.
	Catalogs CatalogSource
	Ledger   LedgerSource
	Recorder Recorder
	Periods  PeriodOpener
	// UnitCount overrides the default of one unit per request. The product
	// is "settlement bookkeeping for services that charge per HTTP
	// request" (README, §1) — one unit per request is what an operator
	// gets without doing anything; an operator billing by a different unit
	// (tokens, bytes, whatever meter.Schedule's unit price is denominated
	// in for this route) supplies its own count here. Optional.
	UnitCount func(*http.Request) uint64
}

func (cfg Config) validate() error {
	if cfg.Operator == "" {
		return errors.New("httpapi: metering config: operator is empty")
	}
	if cfg.Method == "" {
		return errors.New("httpapi: metering config: method is empty")
	}
	if cfg.PathTemplate == "" {
		return errors.New("httpapi: metering config: path template is empty")
	}
	if cfg.Catalogs == nil {
		return errors.New("httpapi: metering config: catalog source is nil")
	}
	if cfg.Ledger == nil {
		return errors.New("httpapi: metering config: ledger source is nil")
	}
	if cfg.Recorder == nil {
		return errors.New("httpapi: metering config: recorder is nil")
	}
	if cfg.Periods == nil {
		return errors.New("httpapi: metering config: period opener is nil")
	}
	return nil
}

// Middleware returns a handler that meters every request that reaches next:
// determines the current ledger, prices the request against the operator's
// published schedule, opens (or finds) the billing period the request
// belongs to, durably records the charge, and only then calls next. It
// returns an error instead of panicking if cfg is incomplete — this is
// startup-time wiring, and a missing dependency here is exactly the kind
// of "fail fast, name the problem" startup validation §8 asks for, not a
// condition to discover from a stack trace during a request.
//
// Recording happens before next runs, not after. This package's job is to
// produce an accurate, complete billing record for every request that
// reaches the wrapped API; "served but never recorded" is a worse failure
// than "rejected before being served", so a LedgerSource, CatalogSource,
// PeriodOpener, or Recorder error fails the request closed (500, next is
// never called) rather than serving it unmetered. Because pricing does not
// depend on anything the wrapped handler produces — the default unit count
// is fixed at one request per unit, and a caller-supplied UnitCount reads
// only the incoming request, never the response — this ordering costs
// nothing: an unpriceable request is rejected before any work is done on
// its behalf, not after.
func Middleware(cfg Config) (func(http.Handler) http.Handler, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	unitCount := cfg.UnitCount
	if unitCount == nil {
		unitCount = func(*http.Request) uint64 { return 1 }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			info, ok := RequestInfoFromContext(ctx)
			if !ok {
				slog.ErrorContext(ctx, "httpapi: metering: no RequestInfo on request context",
					"method", cfg.Method, "path_template", cfg.PathTemplate)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if err := info.Validate(); err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: invalid RequestInfo on request context", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			ledger, err := cfg.Ledger.CurrentLedger(ctx)
			if err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: fetch current ledger", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			// One catalog for the whole request: the price the request is
			// charged and the period it rolls up into have to be decided
			// against the same price book version, or the amount charged
			// and the version the period is recorded under disagree — and
			// a statement whose amounts and period disagree is one the
			// chain rejects.
			catalog, err := cfg.Catalogs.CatalogFor(ctx, cfg.Operator)
			if err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: resolve price catalog",
					"operator", cfg.Operator, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			units := unitCount(r)
			priced, err := catalog.Price(ledger, cfg.Method, cfg.PathTemplate, units)
			if err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: price request",
					"method", cfg.Method, "path_template", cfg.PathTemplate, "ledger", ledger, "units", units, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			scope := meter.Scope{
				Operator: cfg.Operator,
				Consumer: info.Consumer,
				Protocol: info.Protocol,
				Channel:  info.Channel,
			}
			period, err := cfg.Periods.EnsureOpenPeriod(ctx, scope, ledger, catalog)
			if err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: open billing period",
					"method", cfg.Method, "path_template", cfg.PathTemplate, "ledger", ledger, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			var requestID [32]byte
			if _, err := rand.Read(requestID[:]); err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: generate request id", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			rec := RecordedRequest{
				RequestID:      requestID,
				Operator:       cfg.Operator,
				Consumer:       info.Consumer,
				EndpointHash:   merkle.EndpointHash(cfg.Method, cfg.PathTemplate),
				Method:         cfg.Method,
				PathTemplate:   cfg.PathTemplate,
				UnitCount:      units,
				PriceVersion:   priced.PriceVersion,
				ChargedAmount:  priced.ChargedAmount,
				Protocol:       info.Protocol,
				Channel:        info.Channel,
				ObservedLedger: ledger,
				PeriodID:       period.ID,
			}

			if err := cfg.Recorder.RecordRequest(ctx, rec); err != nil {
				slog.ErrorContext(ctx, "httpapi: metering: record request",
					"request_id", fmt.Sprintf("%x", requestID), "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, r)
		})
	}, nil
}
