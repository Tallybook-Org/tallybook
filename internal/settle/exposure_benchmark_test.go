package settle

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"
)

// BenchmarkCalculator_Calculate measures the per-channel cost of
// Calculator.Calculate — the two correlated subqueries over commitments
// (exposure.go) — at commitment counts spanning three orders of magnitude.
//
// It reuses testSettlePool's Postgres-availability guard, so it skips cleanly
// when the local docker-compose Postgres isn't running, matching the other
// integration tests in this package. CI (make test-ci) runs `go test ./...`
// without -bench, so this benchmark never executes there and cannot trip the
// --no-skips guard in scripts/test-ci.sh; it is meant to be run explicitly,
// e.g. `go test ./internal/settle -bench=BenchmarkCalculator_Calculate`.
//
// Per-channel cost is what sets the ceiling on how many open channels one
// settler can watch each tick (§6).
func BenchmarkCalculator_Calculate(b *testing.B) {
	pool := testSettlePool(b) // skips when Postgres is unavailable
	calc := NewCalculator(pool)
	ctx := context.Background()

	// Commitment counts spanning 10^2, 10^3, 10^4.
	sizes := []int{100, 1_000, 10_000}
	addrs := make([]string, len(sizes))

	// Setup: one channel per size, each seeded with `size` valid,
	// monotonically increasing commitments and last_settled_amount = 0 so
	// every commitment is unsettled — exercising both correlated subqueries
	// (MAX for highest_verified, MIN(received_at) for oldest_unsettled_since)
	// on every Calculate call.
	now := time.Now().UTC()
	for i, size := range sizes {
		addr := fmt.Sprintf("CBENCH%057d00", i)
		addrs[i] = addr
		insertTestChannel(b, pool, addr, "open", 0, nil, nil)
		for j := 1; j <= size; j++ {
			receivedAt := now.Add(-time.Duration(size-j) * time.Millisecond).Truncate(time.Microsecond)
			insertTestCommitment(b, pool, addr, int64(j), true, receivedAt)
		}
	}

	b.ResetTimer()

	for i, size := range sizes {
		addr := addrs[i]
		size := size
		b.Run(fmt.Sprintf("commitments/%d", size), func(b *testing.B) {
			// Validate the seed once; the timed loop only calls Calculate.
			exp, err := calc.Calculate(ctx, addr)
			if err != nil {
				b.Fatalf("Calculate(%d commitments): %v", size, err)
			}
			if exp.HighestVerified == nil || exp.HighestVerified.Cmp(big.NewInt(int64(size))) != 0 {
				b.Fatalf("HighestVerified = %s, want %d (%d commitments seeded)", exp.HighestVerified, size, size)
			}
			if exp.Unsettled == nil || exp.Unsettled.Cmp(big.NewInt(int64(size))) != 0 {
				b.Fatalf("Unsettled = %s, want %d (nothing settled yet)", exp.Unsettled, size)
			}

			b.ResetTimer()
			for j := 0; j < b.N; j++ {
				if _, err := calc.Calculate(ctx, addr); err != nil {
					b.Fatalf("Calculate(%d commitments): %v", size, err)
				}
			}
		})
	}
}
