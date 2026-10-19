package indexer

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Metrics exposes ingestion health and cursor staleness using Prometheus text format.
type Metrics struct {
	ingestor *Ingestor
}

// NewMetrics returns the handler for the indexer's ingestion metrics.
func NewMetrics(ingestor *Ingestor) *Metrics {
	return &Metrics{ingestor: ingestor}
}

// ServeHTTP reports recent tick success separately from cursor movement so an
// empty but successful RPC poll is distinguishable from a wedged ingestion loop.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	snapshot := m.ingestor.status.snapshot()
	_, err := fmt.Fprintf(w,
		"# HELP tallybook_indexer_tick_failures_total Failed ingestion polls since process start.\n"+
			"# TYPE tallybook_indexer_tick_failures_total counter\n"+
			"tallybook_indexer_tick_failures_total %d\n"+
			"# HELP tallybook_indexer_consecutive_tick_failures Failed polls since the most recent successful poll.\n"+
			"# TYPE tallybook_indexer_consecutive_tick_failures gauge\n"+
			"tallybook_indexer_consecutive_tick_failures %d\n",
		snapshot.totalFailures, snapshot.consecutiveFailures)
	if err == nil && snapshot.hasSuccessfulTick {
		_, err = fmt.Fprintf(w,
			"# HELP tallybook_indexer_seconds_since_last_successful_tick Seconds since the latest successful RPC and persistence poll.\n"+
				"# TYPE tallybook_indexer_seconds_since_last_successful_tick gauge\n"+
				"tallybook_indexer_seconds_since_last_successful_tick %.3f\n",
			time.Since(snapshot.lastSuccessfulTick).Seconds())
	}
	if err == nil && snapshot.hasCursorAdvance {
		_, err = fmt.Fprintf(w,
			"# HELP tallybook_indexer_seconds_since_cursor_advanced Seconds since the last durable cursor advance.\n"+
				"# TYPE tallybook_indexer_seconds_since_cursor_advanced gauge\n"+
				"tallybook_indexer_seconds_since_cursor_advanced %.3f\n"+
				"# HELP tallybook_indexer_cursor_ledger Ledger of the last durable cursor advance.\n"+
				"# TYPE tallybook_indexer_cursor_ledger gauge\n"+
				"tallybook_indexer_cursor_ledger %d\n",
			time.Since(snapshot.lastCursorAdvance).Seconds(), snapshot.cursorLedger)
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "indexer: metrics: write response", "error", err)
	}
}
