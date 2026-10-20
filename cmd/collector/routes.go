package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Tallybook-Org/tallybook/internal/httpapi"
)

// Header names the example authentication shim reads. They are this
// binary's own invention, not a protocol: see requestInfoFromHeaders.
const (
	headerConsumer = "X-Tallybook-Consumer"
	headerProtocol = "X-Tallybook-Protocol"
	headerChannel  = "X-Tallybook-Channel"
)

// route is one metered endpoint the collector serves.
//
// Method and PathTemplate are the endpoint's identity everywhere else
// too: the price schedule keys its rules on the same pair, and so does
// the merkle leaf's endpoint hash. A route whose path template is not in
// the operator's published schedule prices nothing, and every request to
// it fails closed with a 500 rather than being served unmetered.
type route struct {
	// Method is the HTTP method, uppercase.
	Method string
	// PathTemplate is the path template, exactly as the operator's
	// published schedule spells it.
	PathTemplate string
	// Handler serves the request once it has been metered.
	Handler http.Handler
	// Identify establishes the request's identity — who the caller is and
	// which protocol they paid under — and wraps the metering middleware
	// rather than sitting inside it: the middleware reads the identity
	// this step established, so it has to run first.
	//
	// Optional. A route without one expects the identity to have been
	// established already, by whatever middleware wraps the whole
	// server.
	Identify func(http.Handler) http.Handler
}

// routeDeps are the dependencies every metered route needs — the same
// set for every route, since they all price against the same operator's
// schedule and roll up into the same periods.
type routeDeps struct {
	Operator string
	Catalogs httpapi.CatalogSource
	Ledger   httpapi.LedgerSource
	Recorder httpapi.Recorder
	Periods  httpapi.PeriodOpener
}

// registerRoutes mounts each route behind the metering middleware.
//
// The middleware is what makes a route a paid route: it prices the
// request against the operator's published schedule, files it under the
// billing period it belongs to, and records it — all before the route's
// own handler runs, and failing the request closed if any of that fails.
//
// A route that cannot be wired is a startup error, not a warning: a paid
// API that silently serves unmetered requests is the failure mode this
// whole codebase exists to prevent.
func registerRoutes(mux *http.ServeMux, deps routeDeps, routes []route) error {
	for _, r := range routes {
		middleware, err := httpapi.Middleware(httpapi.Config{
			Operator:     deps.Operator,
			Method:       r.Method,
			PathTemplate: r.PathTemplate,
			Catalogs:     deps.Catalogs,
			Ledger:       deps.Ledger,
			Recorder:     deps.Recorder,
			Periods:      deps.Periods,
		})
		if err != nil {
			return fmt.Errorf("wire metered route %s %s: %w", r.Method, r.PathTemplate, err)
		}
		handler := middleware(r.Handler)
		if r.Identify != nil {
			handler = r.Identify(handler)
		}
		mux.Handle(r.Method+" "+r.PathTemplate, handler)
	}
	return nil
}

// exampleRoutes returns the collector's example metered route.
//
// It exists so the collector is a working metering layer out of the box
// rather than a server with nothing on it: without a route, no request
// is ever metered, no period is ever opened, and there is nothing for
// the period closer or the anchor driver to act on.
//
// It is an example, and it says so: the route serves a fixed "ok"
// response, it is only priced if the operator's published schedule has a
// rule for GET /v1/example, and the identity of its caller comes from a
// request header (see requestInfoFromHeaders) — which is not
// authentication. An operator serving a real API replaces this route
// with their own handler and their own authentication, keeping the
// middleware around it.
func exampleRoutes() []route {
	return []route{
		{
			Method:       "GET",
			PathTemplate: "/v1/example",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok\n"))
			}),
			Identify: requestInfoFromHeaders,
		},
	}
}

// requestInfoFromHeaders attaches the request's RequestInfo to its
// context, built from request headers.
//
// The metering middleware needs to know who the caller is and which
// protocol they paid under before it can meter them. Deciding that is
// the job of whatever verified the payment — explicitly out of scope
// here (§1's non-goals) — and this shim is the example of how that
// decision reaches the middleware.
//
// IT IS NOT AUTHENTICATION. It trusts the headers it is given, so
// anyone who can reach the collector can claim to be any consumer, and
// the operator's usage would be billed to a consumer who never paid for
// it. That is why the route this shim fronts is an example, why it is
// logged at startup as what it is, and why the README says plainly that
// it must be replaced before the collector is exposed.
//
// The headers it reads:
//
//	X-Tallybook-Consumer  the caller's address — required
//	X-Tallybook-Protocol  x402 (the default), mpp_charge or mpp_session
//	X-Tallybook-Channel   the payment channel, required for mpp_session
func requestInfoFromHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		consumer := strings.TrimSpace(r.Header.Get(headerConsumer))
		if consumer == "" {
			http.Error(w, "missing "+headerConsumer, http.StatusUnauthorized)
			return
		}

		protocol := strings.TrimSpace(r.Header.Get(headerProtocol))
		if protocol == "" {
			protocol = httpapi.ProtocolX402
		}
		channel := strings.TrimSpace(r.Header.Get(headerChannel))

		info := httpapi.RequestInfo{
			Consumer: consumer,
			Protocol: protocol,
			Channel:  channel,
		}
		if err := info.Validate(); err != nil {
			slog.ErrorContext(r.Context(), "collector: invalid request info", "error", err)
			http.Error(w, "invalid request info", http.StatusBadRequest)
			return
		}

		next.ServeHTTP(w, r.WithContext(httpapi.WithRequestInfo(r.Context(), info)))
	})
}
