package stellar

import (
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Event topic names. price_book and statement_registry's topics are
// documented in §4 verbatim and confirmed against real emitted events
// (events_test.go). one-way-channel's events are not documented in §4
// beyond function names. The Withdraw topic and payload are confirmed by
// a live event fixture; Open, Close, and Refund remain unobserved. Their
// topic follows soroban_sdk's documented default for a #[contractevent]
// struct with no explicit topics attribute: a single Symbol matching the
// struct's name in snake_case, as declared in event.rs.
//
// CLAUDE.md's build step names "all five contract events", but the real
// contract set is eight: price_book.publish (1) + statement_registry's
// anchor/dispute/resolve (3) + one-way-channel's Open/Close/Withdraw/Refund
// (4) — the one-way-channel side was undercounted, most likely because
// it's external and wasn't enumerated in detail the way the other two
// contracts were. All eight are decoded here since the indexer's channel
// state tracking (a later build step) needs Open, Close, Withdraw, and
// Refund, not just one of them.
const (
	TopicPriceBook = "price_book"
	TopicPublish   = "publish"
	TopicStatement = "statement"
	TopicAnchor    = "anchor"
	TopicDispute   = "dispute"
	TopicResolve   = "resolve"
	TopicOpen      = "open"
	TopicClose     = "close"
	TopicWithdraw  = "withdraw"
	TopicRefund    = "refund"
)

// MatchesTopic decodes each base64 ScVal in topicB64 (as returned by
// EventInfo.Topic) as a Symbol and reports whether the sequence exactly
// equals want. Use it to identify which of a contract's event kinds a
// given EventInfo is before decoding its Value.
func MatchesTopic(topicB64 []string, want ...string) (bool, error) {
	if len(topicB64) != len(want) {
		return false, nil
	}
	for i, b64 := range topicB64 {
		v, err := UnmarshalScValBase64(b64)
		if err != nil {
			return false, fmt.Errorf("stellar: decode topic[%d]: %w", i, err)
		}
		sym, err := DecodeSymbol(v)
		if err != nil {
			return false, fmt.Errorf("stellar: topic[%d]: %w", i, err)
		}
		if sym != want[i] {
			return false, nil
		}
	}
	return true, nil
}

// decodeEventFields decodes valueB64 as an event's positional-Vec data
// payload — the wire form soroban_sdk's #[contractevent] macro uses for a
// struct's fields, confirmed live against real anchor/dispute/resolve
// events (events_test.go) — and checks it has exactly want elements.
func decodeEventFields(valueB64 string, want int) ([]xdr.ScVal, error) {
	items, err := decodeEventVector(valueB64)
	if err != nil {
		return nil, err
	}
	if len(items) != want {
		return nil, fmt.Errorf("event has %d fields, want %d", len(items), want)
	}
	return items, nil
}

func decodeEventVector(valueB64 string) ([]xdr.ScVal, error) {
	v, err := UnmarshalScValBase64(valueB64)
	if err != nil {
		return nil, fmt.Errorf("event value: expected base64-encoded XDR ScVal Vec: %w", err)
	}
	items, err := DecodeVec(v)
	if err != nil {
		return nil, fmt.Errorf("decode event value as vec: %w", err)
	}
	return items, nil
}

// PriceBookPublishEvent mirrors price_book's ("price_book", "publish")
// event data: (operator, version, schedule_hash, effective_ledger).
type PriceBookPublishEvent struct {
	Operator        string
	Version         uint32
	ScheduleHash    [32]byte
	EffectiveLedger uint32
}

// DecodePriceBookPublishEvent decodes an EventInfo.Value carrying a
// price_book publish event.
func DecodePriceBookPublishEvent(valueB64 string) (*PriceBookPublishEvent, error) {
	items, err := decodeEventFields(valueB64, 4)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode price_book publish event: %w", err)
	}
	operator, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: price_book publish event: field 0 (operator): %w", err)
	}
	version, err := DecodeU32(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: price_book publish event: field 1 (version): %w", err)
	}
	scheduleHash, err := decodeBytes32(items[2])
	if err != nil {
		return nil, fmt.Errorf("stellar: price_book publish event: field 2 (schedule_hash): %w", err)
	}
	effectiveLedger, err := DecodeU32(items[3])
	if err != nil {
		return nil, fmt.Errorf("stellar: price_book publish event: field 3 (effective_ledger): %w", err)
	}
	return &PriceBookPublishEvent{
		Operator:        operator,
		Version:         version,
		ScheduleHash:    scheduleHash,
		EffectiveLedger: effectiveLedger,
	}, nil
}

// StatementAnchorEvent mirrors statement_registry's ("statement", "anchor")
// event data: (operator, consumer, seq, usage_root, amount_billed,
// amount_settled, protocol).
type StatementAnchorEvent struct {
	Operator      string
	Consumer      string
	Seq           uint64
	UsageRoot     [32]byte
	AmountBilled  *big.Int
	AmountSettled *big.Int
	Protocol      Protocol
}

// DecodeStatementAnchorEvent decodes an EventInfo.Value carrying a
// statement_registry anchor event.
func DecodeStatementAnchorEvent(valueB64 string) (*StatementAnchorEvent, error) {
	items, err := decodeEventFields(valueB64, 7)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode statement anchor event: %w", err)
	}
	operator, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 0 (operator): %w", err)
	}
	consumer, err := DecodeAddress(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 1 (consumer): %w", err)
	}
	seq, err := DecodeU64(items[2])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 2 (seq): %w", err)
	}
	usageRoot, err := decodeBytes32(items[3])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 3 (usage_root): %w", err)
	}
	amountBilled, err := DecodeI128(items[4])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 4 (amount_billed): %w", err)
	}
	amountSettled, err := DecodeI128(items[5])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 5 (amount_settled): %w", err)
	}
	protocol, err := decodeProtocol(items[6])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement anchor event: field 6 (protocol): %w", err)
	}
	return &StatementAnchorEvent{
		Operator:      operator,
		Consumer:      consumer,
		Seq:           seq,
		UsageRoot:     usageRoot,
		AmountBilled:  amountBilled,
		AmountSettled: amountSettled,
		Protocol:      protocol,
	}, nil
}

// StatementDisputeEvent mirrors statement_registry's ("statement",
// "dispute") event data: (operator, consumer, seq, reason_hash).
type StatementDisputeEvent struct {
	Operator   string
	Consumer   string
	Seq        uint64
	ReasonHash [32]byte
}

// DecodeStatementDisputeEvent decodes an EventInfo.Value carrying a
// statement_registry dispute event.
func DecodeStatementDisputeEvent(valueB64 string) (*StatementDisputeEvent, error) {
	items, err := decodeEventFields(valueB64, 4)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode statement dispute event: %w", err)
	}
	operator, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement dispute event: field 0 (operator): %w", err)
	}
	consumer, err := DecodeAddress(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement dispute event: field 1 (consumer): %w", err)
	}
	seq, err := DecodeU64(items[2])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement dispute event: field 2 (seq): %w", err)
	}
	reasonHash, err := decodeBytes32(items[3])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement dispute event: field 3 (reason_hash): %w", err)
	}
	return &StatementDisputeEvent{
		Operator:   operator,
		Consumer:   consumer,
		Seq:        seq,
		ReasonHash: reasonHash,
	}, nil
}

// StatementResolveEvent mirrors statement_registry's ("statement",
// "resolve") event data: (operator, consumer, seq, resolution_hash,
// amount_credited).
type StatementResolveEvent struct {
	Operator       string
	Consumer       string
	Seq            uint64
	ResolutionHash [32]byte
	AmountCredited *big.Int
}

// DecodeStatementResolveEvent decodes an EventInfo.Value carrying a
// statement_registry resolve event.
func DecodeStatementResolveEvent(valueB64 string) (*StatementResolveEvent, error) {
	items, err := decodeEventFields(valueB64, 5)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode statement resolve event: %w", err)
	}
	operator, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement resolve event: field 0 (operator): %w", err)
	}
	consumer, err := DecodeAddress(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement resolve event: field 1 (consumer): %w", err)
	}
	seq, err := DecodeU64(items[2])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement resolve event: field 2 (seq): %w", err)
	}
	resolutionHash, err := decodeBytes32(items[3])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement resolve event: field 3 (resolution_hash): %w", err)
	}
	amountCredited, err := DecodeI128(items[4])
	if err != nil {
		return nil, fmt.Errorf("stellar: statement resolve event: field 4 (amount_credited): %w", err)
	}
	return &StatementResolveEvent{
		Operator:       operator,
		Consumer:       consumer,
		Seq:            seq,
		ResolutionHash: resolutionHash,
		AmountCredited: amountCredited,
	}, nil
}

// ChannelOpenEvent mirrors one-way-channel's Open event: (from,
// commitment_key, to, token, amount, refund_waiting_period), in the
// struct's declared field order.
type ChannelOpenEvent struct {
	From                string
	CommitmentKey       [32]byte
	To                  string
	Token               string
	Amount              *big.Int
	RefundWaitingPeriod uint32
}

// DecodeChannelOpenEvent decodes an EventInfo.Value carrying a
// one-way-channel Open event.
func DecodeChannelOpenEvent(valueB64 string) (*ChannelOpenEvent, error) {
	items, err := decodeEventFields(valueB64, 6)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode channel Open event: %w", err)
	}
	from, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 0 (from): %w", err)
	}
	commitmentKey, err := decodeBytes32(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 1 (commitment_key): %w", err)
	}
	to, err := DecodeAddress(items[2])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 2 (to): %w", err)
	}
	token, err := DecodeAddress(items[3])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 3 (token): %w", err)
	}
	amount, err := DecodeI128(items[4])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 4 (amount): %w", err)
	}
	refundWaitingPeriod, err := DecodeU32(items[5])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Open event: field 5 (refund_waiting_period): %w", err)
	}
	return &ChannelOpenEvent{
		From:                from,
		CommitmentKey:       commitmentKey,
		To:                  to,
		Token:               token,
		Amount:              amount,
		RefundWaitingPeriod: refundWaitingPeriod,
	}, nil
}

// ChannelCloseEvent mirrors one-way-channel's Close event: a single field,
// effective_at_ledger — the ledger the settler must treat as the
// deadline's starting point (§6).
type ChannelCloseEvent struct {
	EffectiveAtLedger uint32
}

// DecodeChannelCloseEvent decodes an EventInfo.Value carrying a
// one-way-channel Close event.
func DecodeChannelCloseEvent(valueB64 string) (*ChannelCloseEvent, error) {
	items, err := decodeEventFields(valueB64, 1)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode channel Close event: %w", err)
	}
	effectiveAtLedger, err := DecodeU32(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Close event: field 0 (effective_at_ledger): %w", err)
	}
	return &ChannelCloseEvent{EffectiveAtLedger: effectiveAtLedger}, nil
}

// ChannelWithdrawEvent mirrors one-way-channel's Withdraw event: (to,
// amount) in the checked-in contract source. Some deployed events also
// include a leading 32-byte value whose meaning is not documented; it is
// preserved as OpaquePrefix rather than assigned guessed semantics.
type ChannelWithdrawEvent struct {
	OpaquePrefix *[32]byte
	To           string
	Amount       *big.Int
}

// DecodeChannelWithdrawEvent decodes an EventInfo.Value carrying a
// one-way-channel Withdraw event.
func DecodeChannelWithdrawEvent(valueB64 string) (*ChannelWithdrawEvent, error) {
	items, err := decodeEventVector(valueB64)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode channel Withdraw event: %w", err)
	}
	if len(items) != 2 && len(items) != 3 {
		return nil, fmt.Errorf("stellar: decode channel Withdraw event: event has %d fields, want 2 or 3", len(items))
	}
	toIndex, amountIndex := 0, 1
	var opaquePrefix *[32]byte
	if len(items) == 3 {
		prefix, err := decodeBytes32(items[0])
		if err != nil {
			return nil, fmt.Errorf("stellar: channel Withdraw event: field 0 (opaque_prefix): %w", err)
		}
		opaquePrefix = &prefix
		toIndex, amountIndex = 1, 2
	}
	to, err := DecodeAddress(items[toIndex])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Withdraw event: field %d (to): %w", toIndex, err)
	}
	amount, err := DecodeI128(items[amountIndex])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Withdraw event: field %d (amount): %w", amountIndex, err)
	}
	return &ChannelWithdrawEvent{OpaquePrefix: opaquePrefix, To: to, Amount: amount}, nil
}

// ChannelRefundEvent mirrors one-way-channel's Refund event: (from,
// amount) — the entire remaining balance, per §1.
type ChannelRefundEvent struct {
	From   string
	Amount *big.Int
}

// DecodeChannelRefundEvent decodes an EventInfo.Value carrying a
// one-way-channel Refund event.
func DecodeChannelRefundEvent(valueB64 string) (*ChannelRefundEvent, error) {
	items, err := decodeEventFields(valueB64, 2)
	if err != nil {
		return nil, fmt.Errorf("stellar: decode channel Refund event: %w", err)
	}
	from, err := DecodeAddress(items[0])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Refund event: field 0 (from): %w", err)
	}
	amount, err := DecodeI128(items[1])
	if err != nil {
		return nil, fmt.Errorf("stellar: channel Refund event: field 1 (amount): %w", err)
	}
	return &ChannelRefundEvent{From: from, Amount: amount}, nil
}
