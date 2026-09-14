package settle

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestCalculate_ChannelNotFound(t *testing.T) {
	pool := testSettlePool(t)
	calc := NewCalculator(pool)

	_, err := calc.Calculate(context.Background(), "CNONEXISTENT")
	if !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("error = %v, want ErrChannelNotFound", err)
	}
}

func TestCalculate_NoCommitmentsAtAll(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	calc := NewCalculator(pool)

	exp, err := calc.Calculate(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("Calculate returned unexpected error: %v", err)
	}
	if exp.HighestVerified != nil {
		t.Errorf("HighestVerified = %v, want nil", exp.HighestVerified)
	}
	if exp.Unsettled.Sign() != 0 {
		t.Errorf("Unsettled = %s, want 0", exp.Unsettled)
	}
	if exp.OldestUnsettledSince != nil {
		t.Errorf("OldestUnsettledSince = %v, want nil", exp.OldestUnsettledSince)
	}
}

func TestCalculate_OneValidCommitmentNoSettlementYet(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	receivedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	insertTestCommitment(t, pool, testChannel, 5000, true, receivedAt)
	calc := NewCalculator(pool)

	exp, err := calc.Calculate(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("Calculate returned unexpected error: %v", err)
	}
	if exp.HighestVerified.Cmp(big.NewInt(5000)) != 0 {
		t.Errorf("HighestVerified = %s, want 5000", exp.HighestVerified)
	}
	if exp.Unsettled.Cmp(big.NewInt(5000)) != 0 {
		t.Errorf("Unsettled = %s, want 5000", exp.Unsettled)
	}
	if exp.OldestUnsettledSince == nil {
		t.Fatal("OldestUnsettledSince is nil, want the commitment's received_at")
	}
	if !exp.OldestUnsettledSince.Equal(receivedAt) {
		t.Errorf("OldestUnsettledSince = %s, want %s", exp.OldestUnsettledSince, receivedAt)
	}
}

// TestCalculate_InvalidCommitmentsNeverCount is the correctness-critical
// case: an invalid (valid=false) commitment — even one with a higher
// cumulative_amount than any valid one, and even the most recent one —
// must never contribute to HighestVerified or OldestUnsettledSince. §6
// ordering rule 2: never settle against an unverified commitment.
func TestCalculate_InvalidCommitmentsNeverCount(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	now := time.Now().UTC()
	insertTestCommitment(t, pool, testChannel, 1000, true, now.Add(-3*time.Hour).Truncate(time.Microsecond))
	insertTestCommitment(t, pool, testChannel, 9000, false, now.Add(-1*time.Hour).Truncate(time.Microsecond)) // higher, newer, but invalid
	calc := NewCalculator(pool)

	exp, err := calc.Calculate(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("Calculate returned unexpected error: %v", err)
	}
	if exp.HighestVerified.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("HighestVerified = %s, want 1000 (the invalid 9000 commitment must not count)", exp.HighestVerified)
	}
	if exp.Unsettled.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("Unsettled = %s, want 1000", exp.Unsettled)
	}
}

func TestCalculate_FullySettledHasZeroExposure(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 5000, nil, nil)
	insertTestCommitment(t, pool, testChannel, 5000, true, time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond))
	calc := NewCalculator(pool)

	exp, err := calc.Calculate(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("Calculate returned unexpected error: %v", err)
	}
	if exp.Unsettled.Sign() != 0 {
		t.Errorf("Unsettled = %s, want 0 (fully settled)", exp.Unsettled)
	}
	if exp.OldestUnsettledSince != nil {
		t.Errorf("OldestUnsettledSince = %v, want nil (nothing left unsettled)", exp.OldestUnsettledSince)
	}
}

func TestCalculate_PartialSettlementPicksOldestUnsettled(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 3000, nil, nil)
	now := time.Now().UTC()
	// Settled up to 3000. 1000 and 2000 are already covered; 4000 and 6000
	// (cumulative) are still unsettled, 4000 being the older of the two.
	insertTestCommitment(t, pool, testChannel, 1000, true, now.Add(-5*time.Hour).Truncate(time.Microsecond))
	insertTestCommitment(t, pool, testChannel, 2000, true, now.Add(-4*time.Hour).Truncate(time.Microsecond))
	fourK := now.Add(-3 * time.Hour).Truncate(time.Microsecond)
	insertTestCommitment(t, pool, testChannel, 4000, true, fourK)
	insertTestCommitment(t, pool, testChannel, 6000, true, now.Add(-1*time.Hour).Truncate(time.Microsecond))
	calc := NewCalculator(pool)

	exp, err := calc.Calculate(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("Calculate returned unexpected error: %v", err)
	}
	if exp.HighestVerified.Cmp(big.NewInt(6000)) != 0 {
		t.Errorf("HighestVerified = %s, want 6000", exp.HighestVerified)
	}
	if exp.Unsettled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("Unsettled = %s, want 3000 (6000 - 3000)", exp.Unsettled)
	}
	if exp.OldestUnsettledSince == nil || !exp.OldestUnsettledSince.Equal(fourK) {
		t.Errorf("OldestUnsettledSince = %v, want %s (the 4000 commitment, the first one > last_settled_amount)",
			exp.OldestUnsettledSince, fourK)
	}
}

func TestExposure_Age(t *testing.T) {
	now := time.Now()
	since := now.Add(-90 * time.Minute)
	exp := &Exposure{OldestUnsettledSince: &since}
	if got := exp.Age(now); got != 90*time.Minute {
		t.Errorf("Age = %s, want 90m", got)
	}

	empty := &Exposure{}
	if got := empty.Age(now); got != 0 {
		t.Errorf("Age with no OldestUnsettledSince = %s, want 0", got)
	}
}
