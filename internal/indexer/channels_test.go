package indexer

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

// The channels' own bindings (internal/stellar/channel.go) note that no
// live one-way-channel deployment's events were ever captured — see that
// file's doc comment and channel_test.go's. These builders produce
// synthetic-but-schema-accurate event values, the same posture
// channel_test.go already takes for its own RPC-calling tests, using this
// package's own already-verified ScVal helpers rather than hand-rolled XDR.

func mustVecB64(t *testing.T, items ...xdr.ScVal) string {
	t.Helper()
	b64, err := stellar.MarshalScValBase64(stellar.ScvVec(items))
	if err != nil {
		t.Fatalf("MarshalScValBase64: %v", err)
	}
	return b64
}

func mustAddress(t *testing.T, addr string) xdr.ScVal {
	t.Helper()
	v, err := stellar.ScvAddress(addr)
	if err != nil {
		t.Fatalf("ScvAddress(%q): %v", addr, err)
	}
	return v
}

func mustI128(t *testing.T, v int64) xdr.ScVal {
	t.Helper()
	val, err := stellar.ScvI128(big.NewInt(v))
	if err != nil {
		t.Fatalf("ScvI128(%d): %v", v, err)
	}
	return val
}

func buildChannelOpenValue(t *testing.T, from, to, token string, amount int64, refundWaitingPeriod uint32) string {
	t.Helper()
	var commitmentKey [32]byte
	copy(commitmentKey[:], []byte("test-commitment-key-32-bytes!!!"))
	return mustVecB64(t,
		mustAddress(t, from),
		stellar.ScvBytes(commitmentKey[:]),
		mustAddress(t, to),
		mustAddress(t, token),
		mustI128(t, amount),
		stellar.ScvU32(refundWaitingPeriod),
	)
}

func buildChannelCloseValue(t *testing.T, effectiveAtLedger uint32) string {
	t.Helper()
	return mustVecB64(t, stellar.ScvU32(effectiveAtLedger))
}

func buildChannelWithdrawValue(t *testing.T, to string, amount int64) string {
	t.Helper()
	return mustVecB64(t, mustAddress(t, to), mustI128(t, amount))
}

func buildChannelRefundValue(t *testing.T, from string, amount int64) string {
	t.Helper()
	return mustVecB64(t, mustAddress(t, from), mustI128(t, amount))
}

func topicB64(t *testing.T, symbol string) string {
	t.Helper()
	b64, err := stellar.MarshalScValBase64(stellar.ScvSymbol(symbol))
	if err != nil {
		t.Fatalf("MarshalScValBase64(Symbol(%q)): %v", symbol, err)
	}
	return b64
}

const (
	testChannelAddress = "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"
	testFunder         = "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY"
	testOperator       = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"
	testToken          = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
)

func openEventInfo(t *testing.T, ledger uint32, deposited int64, refundWaitingPeriod uint32) stellar.EventInfo {
	t.Helper()
	return stellar.EventInfo{
		Type: "contract", Ledger: ledger, LedgerClosedAt: "2026-09-11T00:00:00Z",
		ContractID: testChannelAddress, ID: fmt.Sprintf("%020d-0000000000", ledger),
		TxHash: "aa00000000000000000000000000000000000000000000000000000000000000",
		Topic:  []string{topicB64(t, stellar.TopicOpen)},
		Value:  buildChannelOpenValue(t, testFunder, testOperator, testToken, deposited, refundWaitingPeriod),
	}
}

func closeEventInfo(t *testing.T, ledger, effectiveAtLedger uint32) stellar.EventInfo {
	t.Helper()
	return stellar.EventInfo{
		Type: "contract", Ledger: ledger, LedgerClosedAt: "2026-09-11T00:00:00Z",
		ContractID: testChannelAddress, ID: fmt.Sprintf("%020d-0000000001", ledger),
		TxHash: "bb00000000000000000000000000000000000000000000000000000000000000",
		Topic:  []string{topicB64(t, stellar.TopicClose)},
		Value:  buildChannelCloseValue(t, effectiveAtLedger),
	}
}

func withdrawEventInfo(t *testing.T, ledger uint32, amount int64) stellar.EventInfo {
	t.Helper()
	return stellar.EventInfo{
		Type: "contract", Ledger: ledger, LedgerClosedAt: "2026-09-11T00:00:00Z",
		ContractID: testChannelAddress, ID: fmt.Sprintf("%020d-0000000002", ledger),
		TxHash: "cc00000000000000000000000000000000000000000000000000000000000000",
		Topic:  []string{topicB64(t, stellar.TopicWithdraw)},
		Value:  buildChannelWithdrawValue(t, testOperator, amount),
	}
}

func refundEventInfo(t *testing.T, ledger uint32, amount int64) stellar.EventInfo {
	t.Helper()
	return stellar.EventInfo{
		Type: "contract", Ledger: ledger, LedgerClosedAt: "2026-09-11T00:00:00Z",
		ContractID: testChannelAddress, ID: fmt.Sprintf("%020d-0000000003", ledger),
		TxHash: "dd00000000000000000000000000000000000000000000000000000000000000",
		Topic:  []string{topicB64(t, stellar.TopicRefund)},
		Value:  buildChannelRefundValue(t, testFunder, amount),
	}
}

func TestApplyChannelOpen_CreatesRowWithOperatorFunderMapping(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{openEventInfo(t, 1000, 5_000_000, 1440)}, Cursor: "C1"},
	}}
	ix := newTestIngestor(t, pool, client)

	if _, err := ix.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}

	var operator, funder, token, status string
	var deposited, withdrawn string
	var refundWaitingPeriod uint32
	err := pool.QueryRow(context.Background(),
		`SELECT operator, funder, token, status, deposited::text, withdrawn::text, refund_waiting_period
		 FROM channels WHERE address = $1`, testChannelAddress,
	).Scan(&operator, &funder, &token, &status, &deposited, &withdrawn, &refundWaitingPeriod)
	if err != nil {
		t.Fatalf("query channel row: %v", err)
	}

	// The mapping this package deliberately makes: the Open event's `to`
	// (recipient) becomes Tallybook's operator, and `from` (funder)
	// becomes the channel's funder — see channels.go's doc comment.
	if operator != testOperator {
		t.Errorf("operator = %q, want %q (the event's `to`)", operator, testOperator)
	}
	if funder != testFunder {
		t.Errorf("funder = %q, want %q (the event's `from`)", funder, testFunder)
	}
	if token != testToken {
		t.Errorf("token = %q, want %q", token, testToken)
	}
	if status != "open" {
		t.Errorf("status = %q, want %q", status, "open")
	}
	if deposited != "5000000" {
		t.Errorf("deposited = %q, want %q", deposited, "5000000")
	}
	if withdrawn != "0" {
		t.Errorf("withdrawn = %q, want %q", withdrawn, "0")
	}
	if refundWaitingPeriod != 1440 {
		t.Errorf("refund_waiting_period = %d, want 1440", refundWaitingPeriod)
	}
}

func TestApplyChannelOpen_DuplicateEventIsIdempotent(t *testing.T) {
	pool := testIndexerPool(t)
	ev := openEventInfo(t, 1000, 5_000_000, 1440)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{ev}, Cursor: "C1"},
		{Events: []stellar.EventInfo{ev}, Cursor: "C2"},
	}}
	ix := newTestIngestor(t, pool, client)

	if _, err := ix.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick returned unexpected error: %v", err)
	}
	if _, err := ix.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick returned unexpected error: %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if count != 1 {
		t.Errorf("channels has %d rows after a duplicate Open event, want 1", count)
	}
}

// TestApplyChannelClose_ComputesDeadline is the literal "deadline
// computation" coverage step 37 asks for: refund_deadline_ledger must be
// exactly effective_at_ledger + refund_waiting_period, using the channel's
// own refund_waiting_period captured at Open — checked across several
// values, including a zero waiting period (deadline == start).
func TestApplyChannelClose_ComputesDeadline(t *testing.T) {
	tests := []struct {
		name                string
		refundWaitingPeriod uint32
		effectiveAtLedger   uint32
	}{
		{"typical waiting period", 1440, 100000},
		{"zero waiting period", 0, 50000},
		{"large waiting period", 999999, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testIndexerPool(t)
			client := &fakeEventsSource{results: []*stellar.GetEventsResult{
				{Events: []stellar.EventInfo{openEventInfo(t, 1000, 5_000_000, tt.refundWaitingPeriod)}, Cursor: "C1"},
				{Events: []stellar.EventInfo{closeEventInfo(t, 2000, tt.effectiveAtLedger)}, Cursor: "C2"},
			}}
			ix := newTestIngestor(t, pool, client)

			if _, err := ix.Tick(context.Background()); err != nil {
				t.Fatalf("open Tick returned unexpected error: %v", err)
			}
			if _, err := ix.Tick(context.Background()); err != nil {
				t.Fatalf("close Tick returned unexpected error: %v", err)
			}

			var status string
			var closeStarted, deadline int32
			err := pool.QueryRow(context.Background(),
				`SELECT status, close_started_ledger, refund_deadline_ledger FROM channels WHERE address = $1`,
				testChannelAddress,
			).Scan(&status, &closeStarted, &deadline)
			if err != nil {
				t.Fatalf("query channel row: %v", err)
			}

			if status != "closing" {
				t.Errorf("status = %q, want %q", status, "closing")
			}
			if closeStarted != int32(tt.effectiveAtLedger) {
				t.Errorf("close_started_ledger = %d, want %d", closeStarted, tt.effectiveAtLedger)
			}
			wantDeadline := int32(tt.effectiveAtLedger + tt.refundWaitingPeriod)
			if deadline != wantDeadline {
				t.Errorf("refund_deadline_ledger = %d, want %d (effective_at_ledger %d + refund_waiting_period %d)",
					deadline, wantDeadline, tt.effectiveAtLedger, tt.refundWaitingPeriod)
			}
		})
	}
}

func TestApplyChannelClose_UnknownChannelWarnsButDoesNotFail(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{closeEventInfo(t, 1000, 5000)}, Cursor: "C1"},
	}}
	ix := newTestIngestor(t, pool, client)

	n, err := ix.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error for a Close event on an unknown channel: %v", err)
	}
	if n != 1 {
		t.Errorf("Tick persisted %d events, want 1 (the chain_events row is still written)", n)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if count != 0 {
		t.Errorf("channels has %d rows, want 0 (nothing to update)", count)
	}
}

// TestApplyChannelWithdraw_AccumulatesAcrossMultipleEvents confirms
// withdrawn is a running cumulative total built from each event's own
// incremental delta, not overwritten by the last event's amount alone —
// and that last_settled_amount tracks the same cumulative total.
func TestApplyChannelWithdraw_AccumulatesAcrossMultipleEvents(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{openEventInfo(t, 1000, 5_000_000, 1440)}, Cursor: "C1"},
		{Events: []stellar.EventInfo{withdrawEventInfo(t, 2000, 300)}, Cursor: "C2"},
		{Events: []stellar.EventInfo{withdrawEventInfo(t, 3000, 200)}, Cursor: "C3"},
	}}
	ix := newTestIngestor(t, pool, client)

	for i := 0; i < 3; i++ {
		if _, err := ix.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d returned unexpected error: %v", i, err)
		}
	}

	var withdrawn, lastSettledAmount string
	var lastSettledLedger int32
	err := pool.QueryRow(context.Background(),
		`SELECT withdrawn::text, last_settled_amount::text, last_settled_ledger FROM channels WHERE address = $1`,
		testChannelAddress,
	).Scan(&withdrawn, &lastSettledAmount, &lastSettledLedger)
	if err != nil {
		t.Fatalf("query channel row: %v", err)
	}

	if withdrawn != "500" {
		t.Errorf("withdrawn = %q, want %q (300 + 200, cumulative)", withdrawn, "500")
	}
	if lastSettledAmount != "500" {
		t.Errorf("last_settled_amount = %q, want %q (same cumulative total)", lastSettledAmount, "500")
	}
	if lastSettledLedger != 3000 {
		t.Errorf("last_settled_ledger = %d, want 3000 (the second withdraw's ledger)", lastSettledLedger)
	}
}

func TestApplyChannelWithdraw_UnknownChannelWarnsButDoesNotFail(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{withdrawEventInfo(t, 1000, 300)}, Cursor: "C1"},
	}}
	ix := newTestIngestor(t, pool, client)

	if _, err := ix.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned unexpected error for a Withdraw event on an unknown channel: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if count != 0 {
		t.Errorf("channels has %d rows, want 0", count)
	}
}

func TestApplyChannelRefund_MarksRefundedWithoutTouchingWithdrawn(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{openEventInfo(t, 1000, 5_000_000, 1440)}, Cursor: "C1"},
		{Events: []stellar.EventInfo{withdrawEventInfo(t, 2000, 300)}, Cursor: "C2"},
		{Events: []stellar.EventInfo{refundEventInfo(t, 3000, 4_700_000)}, Cursor: "C3"},
	}}
	ix := newTestIngestor(t, pool, client)

	for i := 0; i < 3; i++ {
		if _, err := ix.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d returned unexpected error: %v", i, err)
		}
	}

	var status, withdrawn string
	err := pool.QueryRow(context.Background(),
		`SELECT status, withdrawn::text FROM channels WHERE address = $1`, testChannelAddress,
	).Scan(&status, &withdrawn)
	if err != nil {
		t.Fatalf("query channel row: %v", err)
	}
	if status != "refunded" {
		t.Errorf("status = %q, want %q", status, "refunded")
	}
	if withdrawn != "300" {
		t.Errorf("withdrawn = %q, want %q (a refund must not change the recipient's cumulative take)", withdrawn, "300")
	}
}

func TestApplyChannelRefund_UnknownChannelWarnsButDoesNotFail(t *testing.T) {
	pool := testIndexerPool(t)
	client := &fakeEventsSource{results: []*stellar.GetEventsResult{
		{Events: []stellar.EventInfo{refundEventInfo(t, 1000, 100)}, Cursor: "C1"},
	}}
	ix := newTestIngestor(t, pool, client)

	if _, err := ix.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned unexpected error for a Refund event on an unknown channel: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if count != 0 {
		t.Errorf("channels has %d rows, want 0", count)
	}
}
