package store

import (
	"context"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/httpapi"
)

// RequestStore persists metered requests to the requests table (§5). It
// implements httpapi.Recorder so the collector's metering middleware can
// depend on that narrow interface rather than on this package, or on
// pgx, directly.
type RequestStore struct {
	pool *pgxpool.Pool
}

// NewRequestStore returns a RequestStore backed by pool.
func NewRequestStore(pool *pgxpool.Pool) *RequestStore {
	return &RequestStore{pool: pool}
}

var _ httpapi.Recorder = (*RequestStore)(nil)

const insertRequestSQL = `
INSERT INTO requests (
    request_id, operator, consumer, endpoint_hash, method, path_template,
    unit_count, price_version, charged_amount, protocol, channel, observed_ledger, period_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

// RecordRequest inserts req as a new row. request_id is UNIQUE (§5); a
// caller that somehow retries the exact same RecordedRequest — the
// metering middleware never does, since it generates a fresh random
// RequestID per call — will get a wrapped unique-violation error back
// rather than a silent duplicate.
//
// The request's period_id links it to the billing period it rolls up
// into, which is what the operator's statements are built from
// (internal/meter's Anchorer reads a period's requests back through it).
// A request with no period is written with a NULL period_id rather than
// a zero: the column is a foreign key, and 0 is not a period.
func (s *RequestStore) RecordRequest(ctx context.Context, req httpapi.RecordedRequest) error {
	if req.ChargedAmount == nil {
		return fmt.Errorf("store: record request: charged amount is nil")
	}

	amount := pgtype.Numeric{Int: new(big.Int).Set(req.ChargedAmount), Exp: 0, Valid: true}

	// TEXT columns are nullable; an empty Go string must become a real SQL
	// NULL, not the literal empty string, or the requests_channel_iff_session
	// CHECK constraint (§5) — which tests IS NOT NULL, not != '' — would
	// reject a well-formed x402/mpp_charge row that correctly has no channel.
	var channel *string
	if req.Channel != "" {
		channel = &req.Channel
	}

	var periodID *int64
	if req.PeriodID > 0 {
		id := req.PeriodID
		periodID = &id
	}

	if _, err := s.pool.Exec(ctx, insertRequestSQL,
		req.RequestID[:],
		req.Operator,
		req.Consumer,
		req.EndpointHash[:],
		req.Method,
		req.PathTemplate,
		req.UnitCount,
		req.PriceVersion,
		amount,
		req.Protocol,
		channel,
		req.ObservedLedger,
		periodID,
	); err != nil {
		return fmt.Errorf("store: record request %x: %w", req.RequestID, err)
	}
	return nil
}
