// Package indexer ingests Soroban events into Postgres so metered usage can
// be reconciled against what actually settled (§1). It owns its own
// Postgres access directly, the same way internal/custody owns commitment
// storage — §2 describes both the same way ("event ingestion +
// reconciliation" / "commitment storage + verification"), not as thin
// wrappers around internal/store.
//
// A known, deliberate limitation worth stating up front: price_book and
// statement_registry are fixed, configured contracts (TB_PRICE_BOOK_ID,
// TB_STATEMENT_REGISTRY_ID, §7), so their events are filtered precisely by
// contract ID. one-way-channel is not — §4's factory deploys a new
// contract instance per channel, so a new channel's address is never known
// in advance, and nothing in §7's env vars or §4's contract surface
// identifies the factory itself for the indexer to watch instead. Channel
// events (Open, Close, Withdraw, Refund) are therefore filtered by topic
// only, with no contract ID restriction — the only way this package can
// discover a channel it has never seen before. This accepts a real,
// documented risk: any contract anywhere on the network that happens to
// emit a bare Symbol("open"/"close"/"withdraw"/"refund") topic — the exact
// wire shape one-way-channel's own events use, per events.go's own
// extrapolation from soroban_sdk's #[contractevent] default — would be
// picked up too, and decoded as if it were a channel event (classify, and
// then the specific Decode*Event call, would simply fail and the event
// would be skipped if the shapes don't match closely enough, but a
// same-shaped collision can't be ruled out). Tightening this to a real
// factory-address filter is future work once one exists in config.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

// Kind identifies which of the eight contract events (events.go) a
// chain_events row is.
type Kind string

const (
	KindPriceBookPublish Kind = "price_book.publish"
	KindStatementAnchor  Kind = "statement.anchor"
	KindStatementDispute Kind = "statement.dispute"
	KindStatementResolve Kind = "statement.resolve"
	KindChannelOpen      Kind = "channel.open"
	KindChannelClose     Kind = "channel.close"
	KindChannelWithdraw  Kind = "channel.withdraw"
	KindChannelRefund    Kind = "channel.refund"
)

// ErrUnknownEventKind means an event's topic matched none of the known
// kinds. In production this should be unreachable — every filter this
// package sends already restricts getEvents to exactly these topics — so
// it surfacing at all means either the RPC node returned something
// inconsistent with the request, or (for the topic-only channel filter,
// see the package doc comment) an unrelated contract's same-shaped event
// slipped through classify but not a specific Decode*Event call.
var ErrUnknownEventKind = errors.New("indexer: event topic matched none of the known kinds")

// EventsSource is the one piece of *stellar.Client ingestion needs. A
// narrow interface — satisfied by *stellar.Client — keeps this package
// testable without a live Soroban RPC endpoint.
type EventsSource interface {
	GetEvents(ctx context.Context, params stellar.GetEventsParams) (*stellar.GetEventsResult, error)
}

// cursorName is the indexer_cursors row this loop reads and writes. A
// name rather than a singleton table costs nothing today and leaves room
// for more than one ingestion loop later without another migration.
const cursorName = "chain_events"

// Config configures a new Ingestor.
type Config struct {
	Client              EventsSource
	Pool                *pgxpool.Pool
	PriceBookID         string
	StatementRegistryID string
	// StartLedger is where ingestion begins if no cursor has been
	// persisted yet (TB_INDEXER_START_LEDGER, §7).
	StartLedger uint32
	// PageLimit caps how many events GetEvents returns per call. Zero
	// leaves it to the RPC node's own default.
	PageLimit uint32
}

func (cfg Config) validate() error {
	if cfg.Client == nil {
		return errors.New("indexer: config: client is nil")
	}
	if cfg.Pool == nil {
		return errors.New("indexer: config: pool is nil")
	}
	if cfg.PriceBookID == "" {
		return errors.New("indexer: config: price book id is empty")
	}
	if cfg.StatementRegistryID == "" {
		return errors.New("indexer: config: statement registry id is empty")
	}
	return nil
}

// Ingestor pulls contract events from Soroban RPC and persists them to
// chain_events, resuming from a durably persisted cursor.
type Ingestor struct {
	client              EventsSource
	pool                *pgxpool.Pool
	priceBookID         string
	statementRegistryID string
	startLedger         uint32
	pageLimit           uint32
}

// New validates cfg and returns an Ingestor. Returning an error rather
// than panicking on a missing dependency is startup-time validation (§8):
// this runs once, when cmd/indexer wires its dependencies together, not
// mid-ingestion.
func New(cfg Config) (*Ingestor, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Ingestor{
		client: cfg.Client, pool: cfg.Pool,
		priceBookID: cfg.PriceBookID, statementRegistryID: cfg.StatementRegistryID,
		startLedger: cfg.StartLedger, pageLimit: cfg.PageLimit,
	}, nil
}

// topicPatterns lists every known event kind's exact topic sequence, in
// the same order classify checks them. Order doesn't affect correctness —
// the sequences are mutually exclusive by length and content — but scans
// top to bottom for the price_book/statement kinds first since a real
// deployment sees far more of those than channel lifecycle events.
var topicPatterns = []struct {
	kind Kind
	want []string
}{
	{KindPriceBookPublish, []string{stellar.TopicPriceBook, stellar.TopicPublish}},
	{KindStatementAnchor, []string{stellar.TopicStatement, stellar.TopicAnchor}},
	{KindStatementDispute, []string{stellar.TopicStatement, stellar.TopicDispute}},
	{KindStatementResolve, []string{stellar.TopicStatement, stellar.TopicResolve}},
	{KindChannelOpen, []string{stellar.TopicOpen}},
	{KindChannelClose, []string{stellar.TopicClose}},
	{KindChannelWithdraw, []string{stellar.TopicWithdraw}},
	{KindChannelRefund, []string{stellar.TopicRefund}},
}

// classify decodes topic (as returned by EventInfo.Topic) and reports
// which known Kind it matches. A decode error (malformed base64 ScVal, not
// simply "didn't match this candidate") is returned rather than swallowed
// — MatchesTopic itself only attempts to decode a candidate whose length
// already matches, so this is a genuine problem with the event's topic,
// not a length mismatch against one of several candidates.
func classify(topic []string) (Kind, error) {
	for _, p := range topicPatterns {
		ok, err := stellar.MatchesTopic(topic, p.want...)
		if err != nil {
			return "", fmt.Errorf("indexer: classify event: %w", err)
		}
		if ok {
			return p.kind, nil
		}
	}
	return "", ErrUnknownEventKind
}

// topicsB64 XDR-encodes each Symbol in each sequence, in order, for use as
// an stellar.EventFilter's Topics — which getEvents requires as base64
// ScVal per segment, not plain strings (verified against EventInfo.Topic's
// own documented shape in rpc.go).
func topicsB64(sequences [][]string) ([][]string, error) {
	out := make([][]string, len(sequences))
	for i, seq := range sequences {
		encoded := make([]string, len(seq))
		for j, sym := range seq {
			b64, err := stellar.MarshalScValBase64(stellar.ScvSymbol(sym))
			if err != nil {
				return nil, fmt.Errorf("indexer: encode topic symbol %q: %w", sym, err)
			}
			encoded[j] = b64
		}
		out[i] = encoded
	}
	return out, nil
}

// filters builds the three EventFilters this package sends to getEvents:
// price_book (by ID), statement_registry (by ID, three topic sequences
// OR'd together), and one-way-channel (by topic only — see the package
// doc comment for why no contract ID restriction is possible here).
func (ix *Ingestor) filters() ([]stellar.EventFilter, error) {
	priceBookTopics, err := topicsB64([][]string{{stellar.TopicPriceBook, stellar.TopicPublish}})
	if err != nil {
		return nil, err
	}
	statementTopics, err := topicsB64([][]string{
		{stellar.TopicStatement, stellar.TopicAnchor},
		{stellar.TopicStatement, stellar.TopicDispute},
		{stellar.TopicStatement, stellar.TopicResolve},
	})
	if err != nil {
		return nil, err
	}
	channelTopics, err := topicsB64([][]string{
		{stellar.TopicOpen}, {stellar.TopicClose}, {stellar.TopicWithdraw}, {stellar.TopicRefund},
	})
	if err != nil {
		return nil, err
	}

	return []stellar.EventFilter{
		{Type: "contract", ContractIDs: []string{ix.priceBookID}, Topics: priceBookTopics},
		{Type: "contract", ContractIDs: []string{ix.statementRegistryID}, Topics: statementTopics},
		{Type: "contract", Topics: channelTopics},
	}, nil
}

// loadCursor returns the persisted cursor for cursorName, or ("", 0, nil)
// if ingestion has never run before.
func (ix *Ingestor) loadCursor(ctx context.Context) (string, uint32, error) {
	var cursor string
	var ledger uint32
	err := ix.pool.QueryRow(ctx, `SELECT cursor, ledger FROM indexer_cursors WHERE name = $1`, cursorName).
		Scan(&cursor, &ledger)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("indexer: load cursor: %w", err)
	}
	return cursor, ledger, nil
}

const upsertCursorSQL = `
INSERT INTO indexer_cursors (name, cursor, ledger, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (name) DO UPDATE SET cursor = EXCLUDED.cursor, ledger = EXCLUDED.ledger, updated_at = now()`

const insertChainEventSQL = `
INSERT INTO chain_events (
    event_id, ledger, ledger_closed_at, contract_id, tx_hash, topic, kind, value_xdr,
    operator, consumer, seq, amount_billed, amount_settled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (event_id) DO NOTHING`

// persistEvent inserts one classified event. For KindStatementAnchor it
// also decodes the event's value to populate chain_events' denormalized
// operator/consumer/seq/amount_billed/amount_settled columns (see
// 0005_chain_events.sql) — every other kind leaves them NULL.
func persistEvent(ctx context.Context, tx pgx.Tx, ev stellar.EventInfo, kind Kind) error {
	closedAt, err := time.Parse(time.RFC3339, ev.LedgerClosedAt)
	if err != nil {
		return fmt.Errorf("indexer: parse ledgerClosedAt %q: %w", ev.LedgerClosedAt, err)
	}

	var operator, consumer *string
	var seq *int64
	amountBilled := pgtype.Numeric{Valid: false}
	amountSettled := pgtype.Numeric{Valid: false}

	if kind == KindStatementAnchor {
		decoded, err := stellar.DecodeStatementAnchorEvent(ev.Value)
		if err != nil {
			return fmt.Errorf("indexer: decode statement anchor event: %w", err)
		}
		operator = &decoded.Operator
		consumer = &decoded.Consumer
		s := int64(decoded.Seq)
		seq = &s
		amountBilled = pgtype.Numeric{Int: decoded.AmountBilled, Exp: 0, Valid: true}
		amountSettled = pgtype.Numeric{Int: decoded.AmountSettled, Exp: 0, Valid: true}
	}

	if _, err := tx.Exec(ctx, insertChainEventSQL,
		ev.ID, ev.Ledger, closedAt, ev.ContractID, ev.TxHash, ev.Topic, string(kind), ev.Value,
		operator, consumer, seq, amountBilled, amountSettled,
	); err != nil {
		return fmt.Errorf("indexer: insert chain event: %w", err)
	}
	return nil
}

// Tick runs one ingestion cycle: fetch events since the last persisted
// cursor (or StartLedger if none exists yet), classify and persist each
// one, and advance the cursor — all in a single transaction, so a partial
// failure never advances the cursor past events that were never actually
// written. It returns the number of events persisted.
//
// A duplicate event from a cursor overlapping the previous tick (or a
// resume after a crash between commit and... there is no such window,
// since the cursor update is in the same transaction as the event
// inserts) is a harmless no-op via chain_events' own
// ON CONFLICT (event_id) DO NOTHING.
func (ix *Ingestor) Tick(ctx context.Context) (int, error) {
	cursor, _, err := ix.loadCursor(ctx)
	if err != nil {
		return 0, err
	}

	filters, err := ix.filters()
	if err != nil {
		return 0, err
	}

	params := stellar.GetEventsParams{Filters: filters, XDRFormat: "base64"}
	if cursor != "" {
		params.Pagination = &stellar.EventsPagination{Cursor: cursor, Limit: ix.pageLimit}
	} else {
		params.StartLedger = ix.startLedger
		if ix.pageLimit > 0 {
			params.Pagination = &stellar.EventsPagination{Limit: ix.pageLimit}
		}
	}

	result, err := ix.client.GetEvents(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("indexer: get events: %w", err)
	}
	if len(result.Events) == 0 {
		return 0, nil
	}

	tx, err := ix.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("indexer: begin tick transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed below

	persisted := 0
	for _, ev := range result.Events {
		kind, err := classify(ev.Topic)
		if err != nil {
			slog.ErrorContext(ctx, "indexer: could not classify event, skipping",
				"event_id", ev.ID, "contract_id", ev.ContractID, "ledger", ev.Ledger, "error", err)
			continue
		}
		if err := persistEvent(ctx, tx, ev, kind); err != nil {
			return persisted, fmt.Errorf("indexer: event %s: %w", ev.ID, err)
		}
		persisted++
	}

	newCursor := result.Cursor
	if newCursor == "" {
		// Some RPC nodes omit Cursor on a response that isn't a full page;
		// the last event's own ID is the same opaque format getEvents
		// produces for "resume strictly after this event".
		newCursor = result.Events[len(result.Events)-1].ID
	}
	lastLedger := result.Events[len(result.Events)-1].Ledger
	if _, err := tx.Exec(ctx, upsertCursorSQL, cursorName, newCursor, lastLedger); err != nil {
		return persisted, fmt.Errorf("indexer: save cursor: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return persisted, fmt.Errorf("indexer: commit tick: %w", err)
	}
	return persisted, nil
}

// Run calls Tick every interval until ctx is cancelled. A failed tick is
// logged and retried on the next interval, never fatal to the loop —
// matching the settler's own "keep retrying" philosophy (§6): a paused
// indexer means reconciliation silently falls behind, not that anything
// else visibly breaks, so there is no reason to give up.
func (ix *Ingestor) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := ix.Tick(ctx)
			if err != nil {
				slog.ErrorContext(ctx, "indexer: tick failed", "error", err)
				continue
			}
			if n > 0 {
				slog.InfoContext(ctx, "indexer: ingested events", "count", n)
			}
		}
	}
}
