package settle

import (
	"fmt"
	"math/big"
	"testing"
	"time"
)

func u32(v uint32) *uint32 { return &v }

func exposureOf(highest, unsettled int64, oldestSince *time.Time) *Exposure {
	return &Exposure{
		HighestVerified:      big.NewInt(highest),
		Unsettled:            big.NewInt(unsettled),
		OldestUnsettledSince: oldestSince,
	}
}

func defaultPolicy() Policy {
	return Policy{
		SafetyMarginLedgers: 1440,
		MaxExposure:         big.NewInt(10_000_000),
		MaxExposureAge:      24 * time.Hour,
	}
}

// TestDecide_WaitCase covers the baseline: nothing has crossed any
// threshold, the channel is open, and the deadline (if any) is far off —
// Decide must wait.
func TestDecide_WaitCase(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Hour)
	state := ChannelState{
		Address:              testChannel,
		Status:               "open",
		RefundDeadlineLedger: u32(1_000_000),
		Exposure:             exposureOf(5000, 5000, &recentCommitment),
	}

	d := Decide(defaultPolicy(), state, 100, now)
	if d.ShouldSettle {
		t.Errorf("ShouldSettle = true, want false (trigger %q)", d.Trigger)
	}
	if d.Trigger != TriggerNone {
		t.Errorf("Trigger = %q, want empty", d.Trigger)
	}
	if d.Amount != nil {
		t.Errorf("Amount = %v, want nil", d.Amount)
	}
}

// TestDecide_NoUnsettledExposureAlwaysWaits confirms §6's "never settle an
// amount at or below last_settled_amount" holds even when every other
// condition (an imminent deadline, in particular) would otherwise demand
// settling right now.
func TestDecide_NoUnsettledExposureAlwaysWaits(t *testing.T) {
	now := time.Now()
	state := ChannelState{
		Address:              testChannel,
		Status:               "closing",
		RefundDeadlineLedger: u32(100), // already effectively past
		Exposure:             exposureOf(5000, 0, nil),
	}
	d := Decide(defaultPolicy(), state, 1_000_000, now)
	if d.ShouldSettle {
		t.Errorf("ShouldSettle = true, want false — there is nothing unsettled to settle")
	}
}

func TestDecide_NilExposureWaits(t *testing.T) {
	d := Decide(defaultPolicy(), ChannelState{Address: testChannel, Status: "open"}, 100, time.Now())
	if d.ShouldSettle {
		t.Error("ShouldSettle = true for a channel with no Exposure at all")
	}
}

// TestDecide_DeadlinePressureTrigger covers trigger 1.
func TestDecide_DeadlinePressureTrigger(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	state := ChannelState{
		Address:              testChannel,
		Status:               "closing",
		RefundDeadlineLedger: u32(101440), // currentLedger(100000) + margin(1440) == deadline
		Exposure:             exposureOf(5000, 5000, &recentCommitment),
	}
	d := Decide(defaultPolicy(), state, 100000, now)
	if !d.ShouldSettle || d.Trigger != TriggerDeadlinePressure {
		t.Fatalf("Decide = {ShouldSettle:%v Trigger:%q}, want deadline pressure to fire", d.ShouldSettle, d.Trigger)
	}
	if d.Amount.Cmp(big.NewInt(5000)) != 0 {
		t.Errorf("Amount = %s, want 5000", d.Amount)
	}
}

// TestDecide_ExposureThresholdTrigger covers trigger 2.
func TestDecide_ExposureThresholdTrigger(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	policy := defaultPolicy() // MaxExposure = 10,000,000
	state := ChannelState{
		Address:              testChannel,
		Status:               "open",
		RefundDeadlineLedger: nil, // no deadline at all — this trigger must not depend on one
		Exposure:             exposureOf(10_000_001, 10_000_001, &recentCommitment),
	}
	d := Decide(policy, state, 100, now)
	if !d.ShouldSettle || d.Trigger != TriggerExposureThreshold {
		t.Fatalf("Decide = {ShouldSettle:%v Trigger:%q}, want exposure threshold to fire", d.ShouldSettle, d.Trigger)
	}
}

func TestDecide_ExposureExactlyAtThresholdDoesNotFire(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	policy := defaultPolicy()
	state := ChannelState{
		Address:  testChannel,
		Status:   "open",
		Exposure: exposureOf(10_000_000, 10_000_000, &recentCommitment), // exactly MaxExposure, not over it
	}
	d := Decide(policy, state, 100, now)
	if d.ShouldSettle {
		t.Errorf("Decide fired at exactly MaxExposure, want strictly greater required (got trigger %q)", d.Trigger)
	}
}

// TestDecide_AgeThresholdTrigger covers trigger 3.
func TestDecide_AgeThresholdTrigger(t *testing.T) {
	now := time.Now()
	oldCommitment := now.Add(-25 * time.Hour) // older than MaxExposureAge (24h)
	policy := defaultPolicy()
	state := ChannelState{
		Address:  testChannel,
		Status:   "open",
		Exposure: exposureOf(500, 500, &oldCommitment), // well under MaxExposure
	}
	d := Decide(policy, state, 100, now)
	if !d.ShouldSettle || d.Trigger != TriggerAgeThreshold {
		t.Fatalf("Decide = {ShouldSettle:%v Trigger:%q}, want age threshold to fire", d.ShouldSettle, d.Trigger)
	}
}

func TestDecide_AgeExactlyAtThresholdDoesNotFire(t *testing.T) {
	now := time.Now()
	exactly24h := now.Add(-24 * time.Hour)
	policy := defaultPolicy()
	state := ChannelState{
		Address:  testChannel,
		Status:   "open",
		Exposure: exposureOf(500, 500, &exactly24h),
	}
	d := Decide(policy, state, 100, now)
	if d.ShouldSettle {
		t.Errorf("Decide fired at exactly MaxExposureAge, want strictly greater required (got trigger %q)", d.Trigger)
	}
}

// TestDecide_ChannelClosingTrigger covers trigger 4: it must fire even
// when neither threshold has been crossed.
func TestDecide_ChannelClosingTrigger(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	policy := defaultPolicy()
	state := ChannelState{
		Address:              testChannel,
		Status:               "closing",
		RefundDeadlineLedger: u32(1_000_000), // far off — deadline pressure must not be why this fires
		Exposure:             exposureOf(1, 1, &recentCommitment),
	}
	d := Decide(policy, state, 100, now)
	if !d.ShouldSettle || d.Trigger != TriggerChannelClosing {
		t.Fatalf("Decide = {ShouldSettle:%v Trigger:%q}, want channel closing to fire", d.ShouldSettle, d.Trigger)
	}
}

// TestDecide_DeadlinePressureOverridesEveryOtherTrigger is the literal
// "overrides everything" claim in §6, checked directly: construct a
// channel where the exposure threshold, the age threshold, AND channel
// closing would all independently fire on their own — then also put the
// deadline under pressure, and confirm deadline pressure is what actually
// gets reported, not whichever of the other three the switch might
// otherwise have reached first.
func TestDecide_DeadlinePressureOverridesEveryOtherTrigger(t *testing.T) {
	now := time.Now()
	veryOldCommitment := now.Add(-100 * time.Hour) // far past MaxExposureAge
	policy := defaultPolicy()                      // MaxExposure = 10,000,000; MaxExposureAge = 24h

	state := ChannelState{
		Address:              testChannel,
		Status:               "closing",                                              // trigger 4 would fire alone
		RefundDeadlineLedger: u32(100440),                                            // currentLedger(99000) + margin(1440) == deadline: pressure is on
		Exposure:             exposureOf(20_000_000, 20_000_000, &veryOldCommitment), // over MaxExposure AND older than MaxExposureAge
	}

	d := Decide(policy, state, 99000, now)
	if !d.ShouldSettle {
		t.Fatal("ShouldSettle = false, want true")
	}
	if d.Trigger != TriggerDeadlinePressure {
		t.Errorf("Trigger = %q, want %q — deadline pressure must be reported even though the exposure threshold, "+
			"the age threshold, and channel closing all independently apply here too", d.Trigger, TriggerDeadlinePressure)
	}
}

// TestDecide_DeadlinePressureFiresIndependentlyOfEveryOtherCondition is the
// converse check: deadline pressure alone, with every other condition
// deliberately NOT met (exposure well under MaxExposure, commitment
// recent, channel still 'open' rather than 'closing'), must still fire.
// This confirms deadline pressure is a pure ledger-arithmetic check that
// does not implicitly depend on the others being true too.
func TestDecide_DeadlinePressureFiresIndependentlyOfEveryOtherCondition(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute) // well under MaxExposureAge
	policy := defaultPolicy()                 // MaxExposure = 10,000,000

	state := ChannelState{
		Address:              testChannel,
		Status:               "open", // not closing
		RefundDeadlineLedger: u32(50440),
		Exposure:             exposureOf(1, 1, &recentCommitment), // far under MaxExposure
	}

	d := Decide(policy, state, 49000, now) // 49000 + 1440 == 50440: pressure is on
	if !d.ShouldSettle || d.Trigger != TriggerDeadlinePressure {
		t.Fatalf("Decide = {ShouldSettle:%v Trigger:%q}, want deadline pressure to fire on its own", d.ShouldSettle, d.Trigger)
	}
}

// TestDecide_DeadlinePressureBoundary pins the exact boundary the
// override depends on: currentLedger + SafetyMarginLedgers >=
// refund_deadline_ledger. One ledger below the boundary must not fire;
// exactly at it, and one past it, must.
func TestDecide_DeadlinePressureBoundary(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	policy := defaultPolicy() // SafetyMarginLedgers = 1440
	deadline := uint32(101440)

	tests := []struct {
		name          string
		currentLedger uint32
		wantFire      bool
	}{
		{"one ledger before the boundary", 99999, false}, // 99999+1440 = 101439 < 101440
		{"exactly at the boundary", 100000, true},        // 100000+1440 = 101440 >= 101440
		{"one ledger past the boundary", 100001, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := ChannelState{
				Address:              testChannel,
				Status:               "open",
				RefundDeadlineLedger: &deadline,
				Exposure:             exposureOf(1, 1, &recentCommitment),
			}
			d := Decide(policy, state, tt.currentLedger, now)
			if d.ShouldSettle != tt.wantFire {
				t.Errorf("at ledger %d: ShouldSettle = %v, want %v (trigger %q)",
					tt.currentLedger, d.ShouldSettle, tt.wantFire, d.Trigger)
			}
			if tt.wantFire && d.Trigger != TriggerDeadlinePressure {
				t.Errorf("Trigger = %q, want %q", d.Trigger, TriggerDeadlinePressure)
			}
		})
	}
}

func TestDecide_ClosedOrRefundedChannelDoesNotTriggerOnStatusAlone(t *testing.T) {
	now := time.Now()
	recentCommitment := now.Add(-time.Minute)
	for _, status := range []string{"closed", "refunded", "open"} {
		state := ChannelState{
			Address:  testChannel,
			Status:   status,
			Exposure: exposureOf(1, 1, &recentCommitment),
		}
		d := Decide(defaultPolicy(), state, 100, now)
		if d.ShouldSettle {
			t.Errorf("status %q: ShouldSettle = true, want false (only \"closing\" triggers on status alone)", status)
		}
	}
}

func ExampleDecide() {
	policy := Policy{
		SafetyMarginLedgers: 1440,
		MaxExposure:         big.NewInt(10_000_000),
		MaxExposureAge:      24 * time.Hour,
	}

	// Deadline is far away, unsettled exposure is below MaxExposure, but
	// the oldest commitment is 25 h old — age threshold fires.
	deadline := uint32(1_000_000)
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-25 * time.Hour)

	state := ChannelState{
		Address:              "GABC1234",
		Status:               "open",
		RefundDeadlineLedger: &deadline,
		Exposure: &Exposure{
			HighestVerified:      big.NewInt(500_000),
			LastSettledAmount:    big.NewInt(0),
			Unsettled:            big.NewInt(500_000),
			OldestUnsettledSince: &oldest,
		},
	}

	d := Decide(policy, state, 50_000, now)
	fmt.Println(d.ShouldSettle)
	fmt.Println(d.Trigger)
	fmt.Println(d.Amount)
	// Output:
	// true
	// age_threshold
	// 500000
}

// ExampleChannelState shows how a caller constructs a ChannelState value
// with all fields set. Every field of ChannelState is the caller's
// responsibility — the package fills in none. This example demonstrates
// the difference between the zero value and a properly-constructed one,
// so callers can see which fields they must populate.
func ExampleChannelState() {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	deadline := uint32(1_000_000)
	oldest := now.Add(-25 * time.Hour)

	state := ChannelState{
		Address:              testChannel,
		Status:               "open",
		RefundDeadlineLedger: &deadline,
		Exposure: &Exposure{
			HighestVerified:      big.NewInt(500_000),
			LastSettledAmount:    big.NewInt(0),
			Unsettled:            big.NewInt(500_000),
			OldestUnsettledSince: &oldest,
		},
	}

	d := Decide(defaultPolicy(), state, 50_000, now)
	fmt.Printf("settle=%t trigger=%q amount=%s\n", d.ShouldSettle, d.Trigger, d.Amount)
	// Output:
	// settle=true trigger="age_threshold" amount=500000
}

// ExamplePolicy shows how the settler binary builds a Policy and what the
// zero value means. Policy has no constructor and nothing in this package
// fills any of its fields in: every field is the caller's to set, from
// TB_SAFETY_MARGIN_LEDGERS, TB_MAX_EXPOSURE and TB_MAX_EXPOSURE_AGE
// (cmd/settler/main.go copies them straight out of internal/config,
// which refuses to start with any of them missing).
//
// The zero value is legal to pass to Decide but almost never what an
// operator wants: a nil MaxExposure and a zero MaxExposureAge each
// disable their threshold entirely, and a zero SafetyMarginLedgers only
// fires deadline pressure once the refund deadline has actually arrived —
// too late to land a settle before the payer can refund. Only the
// close_start trigger still works under the zero value.
func ExamplePolicy() {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-48 * time.Hour)
	deadline := uint32(4_591_000)

	// 20 XLM unsettled for two days, 1000 ledgers before its refund
	// deadline: every configured threshold has been crossed.
	state := ChannelState{
		Address:              testChannel,
		Status:               string(ChannelStatusOpen),
		RefundDeadlineLedger: &deadline,
		Exposure: &Exposure{
			Channel:              testChannel,
			HighestVerified:      big.NewInt(200_000_000),
			LastSettledAmount:    big.NewInt(0),
			Unsettled:            big.NewInt(200_000_000),
			OldestUnsettledSince: &oldest,
		},
	}
	const currentLedger = 4_590_000

	var zero Policy
	d := Decide(zero, state, currentLedger, now)
	fmt.Printf("zero value: settle=%t trigger=%q\n", d.ShouldSettle, d.Trigger)

	configured := Policy{
		SafetyMarginLedgers: 1440,                   // TB_SAFETY_MARGIN_LEDGERS
		MaxExposure:         big.NewInt(10_000_000), // TB_MAX_EXPOSURE, stroops
		MaxExposureAge:      24 * time.Hour,         // TB_MAX_EXPOSURE_AGE
	}
	d = Decide(configured, state, currentLedger, now)
	fmt.Printf("configured: settle=%t trigger=%q amount=%s\n", d.ShouldSettle, d.Trigger, d.Amount)
	// Output:
	// zero value: settle=false trigger=""
	// configured: settle=true trigger="deadline_pressure" amount=200000000
}

// ExampleDecision constructs a Decision the way a caller would, demonstrating
// the fields a caller sets versus what Decide fills in. Every field of
// Decision is caller-supplied; Decide only sets Amount when
// ShouldSettle is true.
func ExampleDecision() {
	var zero Decision
	fmt.Printf("zero value: channel=%#v shouldSettle=%t trigger=%v amount=%v\n",
		zero.Channel, zero.ShouldSettle, zero.Trigger, zero.Amount)

	d := Decision{
		Channel:      testChannel,
		ShouldSettle: true,
		Trigger:      TriggerDeadlinePressure,
		Amount:       big.NewInt(500_000),
	}
	fmt.Printf("constructed: channel=%#v shouldSettle=%t trigger=%v amount=%v\n",
		d.Channel, d.ShouldSettle, d.Trigger, d.Amount)
	// Output:
	// zero value: channel="" shouldSettle=false trigger= amount=<nil>
	// constructed: channel="CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW" shouldSettle=true trigger=deadline_pressure amount=500000
}
