package settle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Metrics serves §6's /metrics endpoint: "Structured slog throughout,
// plus a /metrics endpoint exposing at minimum: open channels, total
// unsettled exposure per token, ledgers remaining until the nearest
// refund deadline, settle attempts and failures, and time since the last
// successful chain read." §6 also calls out the deadline number
// specifically: "The nearest deadline is the single most important
// number this system produces. An operator should be able to alert on
// it" — it's exposed here as ledgers remaining, which can and does go
// negative once a deadline has already passed, rather than clamping at
// zero, since an operator alerting on "less than N ledgers remaining"
// needs to see how overdue it is, not have that information hidden.
//
// Hand-rolled Prometheus text exposition format rather than a client
// library: §3's dependency rule ("justify every third-party module")
// doesn't clear a bar this small a format doesn't need — a few
// `# TYPE`/`# HELP` lines and `name{labels} value` rows is the entire
// format, not worth a dependency for.
type Metrics struct {
	pool    *pgxpool.Pool
	ledgers LedgerSource
	daemon  *Daemon // optional; nil simply omits the last-chain-read gauge
}

// NewMetrics returns a Metrics handler. daemon may be nil if this
// process isn't also running the Daemon (the last-successful-chain-read
// gauge is simply omitted in that case).
func NewMetrics(pool *pgxpool.Pool, ledgers LedgerSource, daemon *Daemon) *Metrics {
	return &Metrics{pool: pool, ledgers: ledgers, daemon: daemon}
}

// ServeHTTP writes every metric it can successfully collect. One metric
// failing to collect (a query error, an RPC error reading the current
// ledger) is logged and that metric is simply omitted from the response —
// a monitoring endpoint returning partial data beats one returning
// nothing because a single sub-query had a bad moment.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var buf []byte
	buf = appendMetric(buf, "tallybook_settle_open_channels", "gauge",
		"Number of channels currently watched (status open or closing).",
		func() (string, error) { return m.openChannels(ctx) })

	buf = appendTokenMetric(buf, "tallybook_settle_unsettled_exposure", "gauge",
		"Unsettled exposure (highest verified commitment minus last settled amount), summed per token, in stroops.",
		func() (map[string]string, error) { return m.unsettledExposureByToken(ctx) })

	buf = appendMetric(buf, "tallybook_settle_nearest_refund_deadline_ledgers_remaining", "gauge",
		"Ledgers remaining until the nearest watched channel's refund deadline. "+
			"Negative means a deadline has already passed. The single most important number this system produces.",
		func() (string, error) { return m.nearestDeadlineLedgersRemaining(ctx) })

	buf = appendMetric(buf, "tallybook_settle_attempts_total", "counter",
		"Total settle attempts ever recorded (every settlements row, any status).",
		func() (string, error) { return m.settleCount(ctx, "") })

	buf = appendMetric(buf, "tallybook_settle_failures_total", "counter",
		"Total settle attempts that ended in status=failed.",
		func() (string, error) { return m.settleCount(ctx, "failed") })

	if m.daemon != nil {
		buf = appendMetric(buf, "tallybook_settle_last_chain_read_seconds", "gauge",
			"Seconds since the settler's last successful read of the current ledger.",
			func() (string, error) { return m.lastChainReadSeconds() })
	}

	if _, err := w.Write(buf); err != nil {
		slog.ErrorContext(ctx, "settle: metrics: write response", "error", err)
	}
}

func appendMetric(buf []byte, name, typ, help string, collect func() (string, error)) []byte {
	value, err := collect()
	if err != nil {
		slog.Error("settle: metrics: collect failed, omitting", "metric", name, "error", err)
		return buf
	}
	buf = append(buf, fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, typ, name, value)...)
	return buf
}

func appendTokenMetric(buf []byte, name, typ, help string, collect func() (map[string]string, error)) []byte {
	byToken, err := collect()
	if err != nil {
		slog.Error("settle: metrics: collect failed, omitting", "metric", name, "error", err)
		return buf
	}
	buf = append(buf, fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)...)
	for token, value := range byToken {
		buf = append(buf, fmt.Sprintf("%s{token=%q} %s\n", name, token, value)...)
	}
	return buf
}

func (m *Metrics) openChannels(ctx context.Context) (string, error) {
	var count int
	err := m.pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE status IN ('open', 'closing')`).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("count open channels: %w", err)
	}
	return fmt.Sprintf("%d", count), nil
}

const unsettledExposureByTokenSQL = `
SELECT c.token,
       COALESCE(SUM(GREATEST(
           (SELECT MAX(cm.cumulative_amount) FROM commitments cm WHERE cm.channel = c.address AND cm.valid = true) - c.last_settled_amount,
           0
       )), 0) AS unsettled
FROM channels c
WHERE c.status IN ('open', 'closing')
GROUP BY c.token`

func (m *Metrics) unsettledExposureByToken(ctx context.Context) (map[string]string, error) {
	rows, err := m.pool.Query(ctx, unsettledExposureByTokenSQL)
	if err != nil {
		return nil, fmt.Errorf("unsettled exposure by token: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var token string
		var unsettled pgtype.Numeric
		if err := rows.Scan(&token, &unsettled); err != nil {
			return nil, fmt.Errorf("unsettled exposure by token: scan: %w", err)
		}
		amount, err := numericToBigInt(unsettled)
		if err != nil {
			return nil, fmt.Errorf("unsettled exposure by token: %w", err)
		}
		result[token] = amount.String()
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unsettled exposure by token: %w", err)
	}
	return result, nil
}

func (m *Metrics) nearestDeadlineLedgersRemaining(ctx context.Context) (string, error) {
	var nearest *int32
	err := m.pool.QueryRow(ctx,
		`SELECT MIN(refund_deadline_ledger) FROM channels WHERE status IN ('open', 'closing') AND refund_deadline_ledger IS NOT NULL`,
	).Scan(&nearest)
	if err != nil {
		return "", fmt.Errorf("nearest refund deadline: %w", err)
	}
	if nearest == nil {
		// No watched channel has a deadline at all (none are closing yet).
		// "no data" is the honest answer here, distinct from "0 remaining",
		// so this metric is simply omitted rather than reporting a
		// misleading number — matches appendMetric's own omit-on-error
		// convention via a sentinel error.
		return "", errNoDeadlineWatched
	}

	current, err := m.ledgers.CurrentLedger(ctx)
	if err != nil {
		return "", fmt.Errorf("nearest refund deadline: current ledger: %w", err)
	}
	remaining := int64(*nearest) - int64(current)
	return fmt.Sprintf("%d", remaining), nil
}

var errNoDeadlineWatched = fmt.Errorf("no watched channel has a refund deadline yet")

func (m *Metrics) settleCount(ctx context.Context, status string) (string, error) {
	var count int
	var err error
	if status == "" {
		err = m.pool.QueryRow(ctx, `SELECT count(*) FROM settlements`).Scan(&count)
	} else {
		err = m.pool.QueryRow(ctx, `SELECT count(*) FROM settlements WHERE status = $1`, status).Scan(&count)
	}
	if err != nil {
		return "", fmt.Errorf("count settlements (status=%q): %w", status, err)
	}
	return fmt.Sprintf("%d", count), nil
}

func (m *Metrics) lastChainReadSeconds() (string, error) {
	last, ok := m.daemon.LastSuccessfulChainRead()
	if !ok {
		return "", fmt.Errorf("no successful chain read recorded yet")
	}
	return fmt.Sprintf("%.0f", time.Since(last).Seconds()), nil
}

// chainReadTracker is embedded (by value) in Daemon to record when its
// LedgerSource was last read successfully, for lastChainReadSeconds
// above. A mutex, not an atomic.Value, since time.Time isn't safe to
// store in one directly without boxing it — simpler to just lock.
type chainReadTracker struct {
	mu   sync.Mutex
	last time.Time
	set  bool
}

func (c *chainReadTracker) recordSuccess(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = t
	c.set = true
}

func (c *chainReadTracker) get() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.set
}
