package meter

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/merkle"
	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

// Sentinel errors for Anchorer.AnchorPeriod.
var (
	ErrPeriodNotClosed = errors.New("meter: period is not closed, cannot anchor it")
	ErrPeriodEmpty     = errors.New("meter: period has no requests to anchor")
)

// protocolFor maps periods.protocol (lowercase — matching httpapi's own
// Protocol* constants) to statement_registry's Protocol enum (mixed/upper
// case; the wire format is case-sensitive, §4).
func protocolFor(protocol string) (stellar.Protocol, error) {
	switch protocol {
	case "x402":
		return stellar.ProtocolX402, nil
	case "mpp_charge":
		return stellar.ProtocolMppCharge, nil
	case "mpp_session":
		return stellar.ProtocolMppSession, nil
	default:
		return "", fmt.Errorf("meter: anchor: unknown protocol %q", protocol)
	}
}

// AnchorSubmitter is the one piece of *stellar.StatementRegistry
// anchoring needs. A narrow interface — satisfied by
// *stellar.StatementRegistry — keeps this package testable without a
// live Soroban RPC endpoint, matching every other package's own pattern.
type AnchorSubmitter interface {
	Anchor(ctx context.Context, signer *keypair.Full, consumer string, periodStart, periodEnd uint32,
		usageRoot [32]byte, requestCount uint64, token string, amountBilled, amountSettled *big.Int,
		priceBookVersion uint32, protocol stellar.Protocol, channel *string) (uint64, string, error)
}

// Anchorer builds the merkle usage root for a closed period's metered
// requests and anchors it on chain via statement_registry.anchor (§4).
type Anchorer struct {
	pool *pgxpool.Pool
}

// NewAnchorer returns an Anchorer backed by pool.
func NewAnchorer(pool *pgxpool.Pool) *Anchorer {
	return &Anchorer{pool: pool}
}

type periodRow struct {
	id                        int64
	operator, consumer        string
	protocol                  string
	channel                   *string
	priceVersion, periodStart uint32
	periodEnd                 *uint32 // nil while status is 'open'; always set once 'closed' or 'anchored'
	status                    string
}

const fetchPeriodSQL = `
SELECT id, operator, consumer, protocol, channel, price_version, period_start, period_end, status
FROM periods WHERE id = $1`

// fetchPeriod does not require period_end to be set — an 'open' period
// never has one (periods' own CHECK constraint, 0001_periods.sql), and
// AnchorPeriod must still be able to fetch such a row far enough to
// report ErrPeriodNotClosed with a clear status, rather than an opaque
// "no period_end" error before ever checking status at all.
func (a *Anchorer) fetchPeriod(ctx context.Context, periodID int64) (*periodRow, error) {
	var p periodRow
	var priceVersion, periodStart int32
	var periodEnd *int32
	err := a.pool.QueryRow(ctx, fetchPeriodSQL, periodID).Scan(
		&p.id, &p.operator, &p.consumer, &p.protocol, &p.channel, &priceVersion, &periodStart, &periodEnd, &p.status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("meter: anchor: period %d not found", periodID)
	}
	if err != nil {
		return nil, fmt.Errorf("meter: anchor: fetch period %d: %w", periodID, err)
	}
	p.priceVersion = uint32(priceVersion)
	p.periodStart = uint32(periodStart)
	if periodEnd != nil {
		v := uint32(*periodEnd)
		p.periodEnd = &v
	}
	return &p, nil
}

const fetchPeriodRequestsSQL = `
SELECT request_id, endpoint_hash, unit_count, charged_amount, observed_ledger
FROM requests WHERE period_id = $1 ORDER BY id`

type requestRow struct {
	requestID, endpointHash [32]byte
	unitCount               uint64
	chargedAmount           *big.Int
	observedLedger          uint32
}

func (a *Anchorer) fetchRequests(ctx context.Context, periodID int64) ([]requestRow, error) {
	rows, err := a.pool.Query(ctx, fetchPeriodRequestsSQL, periodID)
	if err != nil {
		return nil, fmt.Errorf("meter: anchor: fetch requests for period %d: %w", periodID, err)
	}
	defer rows.Close()

	var out []requestRow
	for rows.Next() {
		var requestID, endpointHash []byte
		var chargedAmount pgtype.Numeric
		var unitCount int64
		var observedLedger int32
		if err := rows.Scan(&requestID, &endpointHash, &unitCount, &chargedAmount, &observedLedger); err != nil {
			return nil, fmt.Errorf("meter: anchor: scan request row: %w", err)
		}
		amount, err := numericToBigInt(chargedAmount)
		if err != nil {
			return nil, fmt.Errorf("meter: anchor: charged_amount: %w", err)
		}
		row := requestRow{unitCount: uint64(unitCount), chargedAmount: amount, observedLedger: uint32(observedLedger)}
		if len(requestID) != 32 || len(endpointHash) != 32 {
			return nil, fmt.Errorf("meter: anchor: request/endpoint id is not 32 bytes")
		}
		copy(row.requestID[:], requestID)
		copy(row.endpointHash[:], endpointHash)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meter: anchor: fetch requests for period %d: %w", periodID, err)
	}
	return out, nil
}

const markAnchoredSQL = `
UPDATE periods SET status = 'anchored', statement_seq = $2 WHERE id = $1 AND status = 'closed'`

// AnchorResult is what AnchorPeriod produces.
type AnchorResult struct {
	PeriodID     int64
	StatementSeq uint64
	TxHash       string
	UsageRoot    [32]byte
	RequestCount uint64
	AmountBilled *big.Int
}

// AnchorPeriod builds the merkle usage root for periodID's metered
// requests — byte-for-byte the same leaf format statement_registry's own
// verify_usage checks (§4; internal/merkle is the single source of truth
// for it) — anchors it on chain, then marks the period 'anchored' with
// the resulting statement sequence.
//
// token and amountSettled are supplied by the caller rather than derived
// here: neither is tracked anywhere this package can read on its own.
// requests has no token column (nothing in §5's schema associates one
// with a request), and how much of a period has actually settled is
// protocol-specific in a way this package doesn't have enough context to
// resolve safely on its own — for x402 and mpp_charge, payment is
// normally verified before a request is even served, so amountSettled is
// typically the same value as the returned AmountBilled; for
// mpp_session, it should be the channel's own last_settled_amount (§5),
// which internal/settle already tracks independently and can lag behind
// billed usage by design (that's the whole point of the settler's
// exposure-driven sweep policy, §6).
//
// This does not have internal/settle's "record intent before submitting"
// durability guarantee (§6 ordering rule 3): statement_registry.anchor
// has no idempotency key of its own (unlike settle, keyed by (channel,
// cumulative_amount)), so a crash between a successful on-chain anchor
// and this function's own final UPDATE would leave the period 'closed'
// locally with no local record of the on-chain statement, and a naive
// retry would anchor the same usage a second time, as a separate
// statement, on chain. Closing that gap fully would need the same
// submitting/confirmed/failed machinery internal/settle already has,
// applied to anchoring too — out of scope for this step; noted here
// rather than silently assumed away.
func (a *Anchorer) AnchorPeriod(
	ctx context.Context, periodID int64, signer *keypair.Full, registry AnchorSubmitter,
	token string, amountSettled *big.Int,
) (*AnchorResult, error) {
	period, err := a.fetchPeriod(ctx, periodID)
	if err != nil {
		return nil, err
	}
	if period.status != "closed" {
		return nil, fmt.Errorf("meter: anchor period %d: %w (status is %q)", periodID, ErrPeriodNotClosed, period.status)
	}
	if period.periodEnd == nil {
		// Unreachable given periods' own periods_closed_fields CHECK
		// constraint (status != 'open' requires period_end set) — guarded
		// anyway rather than dereferencing a nil pointer below.
		return nil, fmt.Errorf("meter: anchor period %d: status is closed but period_end is unset", periodID)
	}

	requests, err := a.fetchRequests(ctx, periodID)
	if err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("meter: anchor period %d: %w", periodID, ErrPeriodEmpty)
	}

	leaves := make([][32]byte, len(requests))
	amountBilled := big.NewInt(0)
	for i, r := range requests {
		leaf, err := merkle.Leaf(merkle.Record{
			Amount:       r.chargedAmount,
			Consumer:     period.consumer,
			EndpointHash: r.endpointHash,
			Ledger:       r.observedLedger,
			PriceVersion: period.priceVersion,
			RequestID:    r.requestID,
			Units:        r.unitCount,
		})
		if err != nil {
			return nil, fmt.Errorf("meter: anchor period %d: build leaf for request %x: %w", periodID, r.requestID, err)
		}
		leaves[i] = leaf
		amountBilled.Add(amountBilled, r.chargedAmount)
	}

	tree, err := merkle.BuildTree(leaves)
	if err != nil {
		return nil, fmt.Errorf("meter: anchor period %d: build tree: %w", periodID, err)
	}

	protocol, err := protocolFor(period.protocol)
	if err != nil {
		return nil, fmt.Errorf("meter: anchor period %d: %w", periodID, err)
	}

	seq, hash, err := registry.Anchor(ctx, signer, period.consumer, period.periodStart, *period.periodEnd,
		tree.Root, uint64(len(requests)), token, amountBilled, amountSettled, period.priceVersion, protocol, period.channel)
	if err != nil {
		return nil, fmt.Errorf("meter: anchor period %d: %w", periodID, err)
	}

	if _, err := a.pool.Exec(ctx, markAnchoredSQL, periodID, int64(seq)); err != nil {
		return nil, fmt.Errorf("meter: anchor period %d: anchored on chain (seq %d, tx %s) but failed to record locally: %w",
			periodID, seq, hash, err)
	}

	return &AnchorResult{
		PeriodID: periodID, StatementSeq: seq, TxHash: hash,
		UsageRoot: tree.Root, RequestCount: uint64(len(requests)), AmountBilled: amountBilled,
	}, nil
}
