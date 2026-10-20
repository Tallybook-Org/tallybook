package main

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tallybook-Org/tallybook/internal/httpapi"
	"github.com/Tallybook-Org/tallybook/internal/meter"
)

const (
	testOperator = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
	testConsumer = "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
)

// fakeCatalogs is a CatalogSource returning one catalog, or one error.
type fakeCatalogs struct {
	catalog *meter.Catalog
	err     error
}

func (f fakeCatalogs) CatalogFor(context.Context, string) (*meter.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.catalog, nil
}

// fakeLedger is a LedgerSource returning one ledger number.
type fakeLedger struct {
	ledger uint32
}

func (f fakeLedger) CurrentLedger(context.Context) (uint32, error) {
	return f.ledger, nil
}

// fakeRecorder records what the middleware asked it to record.
type fakeRecorder struct {
	recorded []httpapi.RecordedRequest
}

func (f *fakeRecorder) RecordRequest(_ context.Context, req httpapi.RecordedRequest) error {
	f.recorded = append(f.recorded, req)
	return nil
}

// fakePeriods is a PeriodOpener returning one period.
type fakePeriods struct {
	period *meter.Period
}

func (f fakePeriods) EnsureOpenPeriod(context.Context, meter.Scope, uint32, *meter.Catalog) (*meter.Period, error) {
	return f.period, nil
}

// testCatalog builds a catalog that prices GET /v1/example.
func testCatalog(t *testing.T) *meter.Catalog {
	t.Helper()
	schedule, err := meter.ParseSchedule([]byte(`{"rules":[{"method":"GET","path_template":"/v1/example","unit_price":"1000"}]}`))
	if err != nil {
		t.Fatalf("ParseSchedule returned unexpected error: %v", err)
	}
	catalog, err := meter.NewCatalog(meter.CatalogVersion{Version: 1, EffectiveLedger: 1000, Schedule: schedule})
	if err != nil {
		t.Fatalf("NewCatalog returned unexpected error: %v", err)
	}
	return catalog
}

// testMux wires the example routes against the given dependencies.
func testMux(t *testing.T, deps routeDeps) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := registerRoutes(mux, deps, exampleRoutes()); err != nil {
		t.Fatalf("registerRoutes returned unexpected error: %v", err)
	}
	return mux
}

// TestExampleRoute_MetersTheRequest covers the whole path a metered
// request takes through the collector: the header shim establishes the
// caller, the middleware prices the request against the operator's
// published schedule, files it under a period, and records it — and only
// then does the route's own handler run.
func TestExampleRoute_MetersTheRequest(t *testing.T) {
	recorder := &fakeRecorder{}
	mux := testMux(t, routeDeps{
		Operator: testOperator,
		Catalogs: fakeCatalogs{catalog: testCatalog(t)},
		Ledger:   fakeLedger{ledger: 1500},
		Recorder: recorder,
		Periods:  fakePeriods{period: &meter.Period{ID: 7}},
	})

	req := httptest.NewRequest("GET", "/v1/example", nil)
	req.Header.Set(headerConsumer, testConsumer)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(recorder.recorded) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(recorder.recorded))
	}
	got := recorder.recorded[0]
	if got.Consumer != testConsumer {
		t.Errorf("consumer = %q, want %q", got.Consumer, testConsumer)
	}
	if got.Protocol != httpapi.ProtocolX402 {
		t.Errorf("protocol = %q, want %q", got.Protocol, httpapi.ProtocolX402)
	}
	if got.ChargedAmount.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("charged amount = %s, want 1000", got.ChargedAmount)
	}
	if got.PeriodID != 7 {
		t.Errorf("period = %d, want 7", got.PeriodID)
	}
}

// TestExampleRoute_RequiresAConsumer covers the header shim's one hard
// requirement: without a consumer there is nothing to bill, so the
// request is refused before it reaches the API.
func TestExampleRoute_RequiresAConsumer(t *testing.T) {
	recorder := &fakeRecorder{}
	mux := testMux(t, routeDeps{
		Operator: testOperator,
		Catalogs: fakeCatalogs{catalog: testCatalog(t)},
		Ledger:   fakeLedger{ledger: 1500},
		Recorder: recorder,
		Periods:  fakePeriods{period: &meter.Period{ID: 7}},
	})

	req := httptest.NewRequest("GET", "/v1/example", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(recorder.recorded) != 0 {
		t.Errorf("recorded %d requests, want 0", len(recorder.recorded))
	}
}

// TestExampleRoute_FailsClosedWithoutAPriceBook covers the metering
// path's failure posture: with the operator's price book unreadable, the
// request is refused rather than served unmetered.
func TestExampleRoute_FailsClosedWithoutAPriceBook(t *testing.T) {
	recorder := &fakeRecorder{}
	mux := testMux(t, routeDeps{
		Operator: testOperator,
		Catalogs: fakeCatalogs{err: errors.New("price book unavailable")},
		Ledger:   fakeLedger{ledger: 1500},
		Recorder: recorder,
		Periods:  fakePeriods{period: &meter.Period{ID: 7}},
	})

	req := httptest.NewRequest("GET", "/v1/example", nil)
	req.Header.Set(headerConsumer, testConsumer)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if len(recorder.recorded) != 0 {
		t.Errorf("recorded %d requests, want 0 — an unpriced request must never be served", len(recorder.recorded))
	}
}

// TestRequestInfoFromHeaders covers the shim's own contract: the
// protocol defaults to x402, an mpp_session request must name its
// channel, and an unknown protocol is refused rather than guessed at.
func TestRequestInfoFromHeaders(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantInfo   httpapi.RequestInfo
	}{
		{
			name:       "defaults to x402",
			headers:    map[string]string{headerConsumer: testConsumer},
			wantStatus: http.StatusOK,
			wantInfo:   httpapi.RequestInfo{Consumer: testConsumer, Protocol: httpapi.ProtocolX402},
		},
		{
			name: "mpp_charge with no channel",
			headers: map[string]string{
				headerConsumer: testConsumer,
				headerProtocol: httpapi.ProtocolMPPCharge,
			},
			wantStatus: http.StatusOK,
			wantInfo:   httpapi.RequestInfo{Consumer: testConsumer, Protocol: httpapi.ProtocolMPPCharge},
		},
		{
			name: "mpp_session with a channel",
			headers: map[string]string{
				headerConsumer: testConsumer,
				headerProtocol: httpapi.ProtocolMPPSession,
				headerChannel:  "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW",
			},
			wantStatus: http.StatusOK,
			wantInfo: httpapi.RequestInfo{
				Consumer: testConsumer,
				Protocol: httpapi.ProtocolMPPSession,
				Channel:  "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW",
			},
		},
		{
			name: "mpp_session without a channel",
			headers: map[string]string{
				headerConsumer: testConsumer,
				headerProtocol: httpapi.ProtocolMPPSession,
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown protocol",
			headers: map[string]string{
				headerConsumer: testConsumer,
				headerProtocol: "carrier_pigeon",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no consumer",
			headers:    map[string]string{},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got httpapi.RequestInfo
			handler := requestInfoFromHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ = httpapi.RequestInfoFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/v1/example", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK && got != tt.wantInfo {
				t.Errorf("request info = %+v, want %+v", got, tt.wantInfo)
			}
		})
	}
}
