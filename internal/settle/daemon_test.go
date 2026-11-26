package settle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/Tallybook-Org/tallybook/internal/stellar"
)

type fakeLedgerSource struct {
	ledger uint32
	err    error
}

func (f fakeLedgerSource) CurrentLedger(context.Context) (uint32, error) {
	return f.ledger, f.err
}

func fixedFactory(live LiveChannel) ChannelFactory {
	return func(string) LiveChannel { return live }
}

// fakeLiveChannel implements LiveChannel for daemon tests: Settle
// succeeds immediately (no retry needed within the tick), Withdrawn is
// unused unless a test specifically exercises reconciliation.
type fakeLiveChannel struct {
	*fakeChannelSettler
	withdrawn *fakeWithdrawnReader
}

func (f *fakeLiveChannel) Withdrawn(ctx context.Context) (*big.Int, error) {
	return f.withdrawn.Withdrawn(ctx)
}

func newFakeLiveChannel(hash string, err error) *fakeLiveChannel {
	return &fakeLiveChannel{
		fakeChannelSettler: &fakeChannelSettler{hash: hash, err: err},
		withdrawn:          &fakeWithdrawnReader{withdrawn: big.NewInt(0)},
	}
}

func TestDaemon_Tick_SettlesChannelsOverThreshold(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, testChannel, 20_000_000) // over MaxExposure

	live := newFakeLiveChannel("hash1", nil)
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), fixedFactory(live), nil,
		fakeLedgerSource{ledger: 100}, defaultPolicy())

	attempted, underPressure, err := daemon.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}
	if attempted != 1 {
		t.Errorf("attempted = %d, want 1", attempted)
	}
	if underPressure {
		t.Error("underPressure = true, want false (no deadline set)")
	}
	if len(live.fakeChannelSettler.calls) != 1 {
		t.Fatalf("Settle called %d times, want 1", len(live.fakeChannelSettler.calls))
	}
	if live.fakeChannelSettler.calls[0].amount.Cmp(big.NewInt(20_000_000)) != 0 {
		t.Errorf("Settle called with amount %s, want 20000000", live.fakeChannelSettler.calls[0].amount)
	}

	status, exists := settlementStatus(t, pool, testChannel, 20_000_000)
	if !exists || status != "confirmed" {
		t.Errorf("settlements row status = %q (exists=%v), want confirmed", status, exists)
	}
}

func TestDaemon_Tick_DoesNotSettleChannelsUnderThreshold(t *testing.T) {
	pool := testSettlePool(t)
	insertTestChannel(t, pool, testChannel, "open", 0, nil, nil)
	insertTestCommitment(t, pool, testChannel, 100, true, time.Now().UTC().Truncate(time.Microsecond)) // well under every threshold

	live := newFakeLiveChannel("should-not-be-used", nil)
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), fixedFactory(live), nil,
		fakeLedgerSource{ledger: 100}, defaultPolicy())

	attempted, _, err := daemon.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}
	if attempted != 0 {
		t.Errorf("attempted = %d, want 0", attempted)
	}
	if len(live.fakeChannelSettler.calls) != 0 {
		t.Error("Settle was called for a channel under every threshold")
	}
}

// TestDaemon_Tick_IgnoresClosedAndRefundedChannels confirms the watch
// scope itself — status IN ('open', 'closing') per §6 — excludes
// terminal-state channels regardless of how much exposure they show.
func TestDaemon_Tick_IgnoresClosedAndRefundedChannels(t *testing.T) {
	pool := testSettlePool(t)
	for i, status := range []string{"closed", "refunded"} {
		addr := testChannel[:len(testChannel)-1] + string(rune('A'+i))
		insertTestChannel(t, pool, addr, status, 0, nil, nil)
		insertTestCommitment(t, pool, addr, 50_000_000, true, time.Now().UTC().Truncate(time.Microsecond))
	}

	live := newFakeLiveChannel("should-not-be-used", nil)
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), fixedFactory(live), nil,
		fakeLedgerSource{ledger: 100}, defaultPolicy())

	attempted, _, err := daemon.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}
	if attempted != 0 {
		t.Errorf("attempted = %d, want 0 (closed/refunded channels must never be watched)", attempted)
	}
	if len(live.fakeChannelSettler.calls) != 0 {
		t.Error("Settle was called for a closed or refunded channel")
	}
}

// TestDaemon_Tick_OneChannelFailureDoesNotStopOthers is the resilience
// case: two channels both need settling; the first's chain call fails
// permanently (a real contract-level rejection, not a transient error —
// otherwise Daemon.Tick's real, un-faked retry/backoff would burn through
// its whole tickRetryBudget for real wall-clock time before this test
// could even get to the second channel), the second must still be
// attempted and succeed.
func TestDaemon_Tick_OneChannelFailureDoesNotStopOthers(t *testing.T) {
	pool := testSettlePool(t)
	channelA := testChannel
	channelB := testChannel[:len(testChannel)-1] + "Z"
	insertTestChannel(t, pool, channelA, "open", 0, nil, nil)
	insertTestChannel(t, pool, channelB, "open", 0, nil, nil)
	insertValidCommitmentForSettle(t, pool, channelA, 20_000_000)
	insertValidCommitmentForSettle(t, pool, channelB, 20_000_000)

	failing := newFakeLiveChannel("", &stellar.ErrTransactionFailed{Hash: "deadbeef"})
	succeeding := newFakeLiveChannel("hash-b", nil)

	factory := func(address string) LiveChannel {
		if address == channelA {
			return failing
		}
		return succeeding
	}
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), factory, nil,
		fakeLedgerSource{ledger: 100}, defaultPolicy())

	attempted, _, err := daemon.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}
	if attempted != 2 {
		t.Errorf("attempted = %d, want 2 (both channels needed settling)", attempted)
	}

	statusA, existsA := settlementStatus(t, pool, channelA, 20_000_000)
	if !existsA || statusA != "failed" {
		t.Errorf("channel A settlement status = %q (exists=%v), want failed", statusA, existsA)
	}
	statusB, existsB := settlementStatus(t, pool, channelB, 20_000_000)
	if !existsB || statusB != "confirmed" {
		t.Errorf("channel B settlement status = %q (exists=%v), want confirmed despite channel A failing", statusB, existsB)
	}
}

// TestDaemon_Tick_ReportsUnderDeadlinePressure confirms Run's tightening
// signal is actually derived from a real deadline-pressure decision, not
// just from whether anything got attempted.
func TestDaemon_Tick_ReportsUnderDeadlinePressure(t *testing.T) {
	pool := testSettlePool(t)
	deadline := int32(101440) // currentLedger(100000) + margin(1440) == deadline
	insertTestChannel(t, pool, testChannel, "closing", 0, ptrInt32(101440-1440), &deadline)
	insertValidCommitmentForSettle(t, pool, testChannel, 100)

	live := newFakeLiveChannel("hash1", nil)
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), fixedFactory(live), nil,
		fakeLedgerSource{ledger: 100000}, defaultPolicy())

	_, underPressure, err := daemon.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick returned unexpected error: %v", err)
	}
	if !underPressure {
		t.Error("underPressure = false, want true")
	}
}

func TestDaemon_Tick_LedgerSourceErrorPropagates(t *testing.T) {
	pool := testSettlePool(t)
	boom := errors.New("rpc down")
	daemon := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool), fixedFactory(newFakeLiveChannel("", nil)), nil,
		fakeLedgerSource{err: boom}, defaultPolicy())

	_, _, err := daemon.Tick(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap %v", err, boom)
	}
}

func ptrInt32(v int32) *int32 { return &v }

func ExampleDaemon_LastSuccessfulChainRead() {
	d := NewDaemon(nil, nil, nil, nil, nil, nil, Policy{})

	// Before any successful Tick the chain has never been read.
	_, ok := d.LastSuccessfulChainRead()
	fmt.Println(ok)

	// Once a Tick records a successful ledger read, it returns that time and true.
	t0 := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	d.chainRead.recordSuccess(t0)
	got, ok := d.LastSuccessfulChainRead()
	fmt.Println(ok)
	fmt.Println(got.UTC().Format(time.RFC3339))
	// Output:
	// false
	// true
	// 2024-01-15T10:00:00Z
}

// ExampleDaemon_Tick shows one pass over every channel whose status is
// 'open' or 'closing'. Tick reads the current ledger first — a failure
// there fails the whole tick, since no deadline can be judged without it
// — then calculates each channel's exposure, asks Decide whether to
// settle, and settles through SubmitWithRetry with a bounded in-line
// retry budget. One channel's error is logged and skipped, so a nil error
// does not mean every channel settled: attempted counts the channels
// Decide chose to settle, not the ones that succeeded. A channel that
// did not finish is picked up again on the next tick. underDeadlinePressure
// reports whether any channel is inside SafetyMarginLedgers of its refund
// deadline, which is what Run uses to tighten its interval.
//
// The pool, ledger source and channel factory are the package's own test
// stubs: fakeLedgerSource stands in for Soroban RPC's getLatestLedger and
// fakeLiveChannel for one-way-channel. There is no // Output: line on
// purpose: Tick lists channels and reads commitments and settlements from
// Postgres, so its result depends on database state an example cannot set
// up without a live database. Without an Output line `go test` compiles
// the example but does not run it, so the zero-value pool is never
// dialed. It is here to pin the call shape: a signature change breaks the
// build. The behaviour is covered by the TestDaemon_Tick_* tests above.
func ExampleDaemon_Tick() {
	pool := &pgxpool.Pool{}
	d := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool),
		fixedFactory(newFakeLiveChannel("abc123", nil)), nil,
		fakeLedgerSource{ledger: 4_590_000}, defaultPolicy())

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	attempted, underPressure, err := d.Tick(ctx)
	if err != nil {
		// Only a failure to read the current ledger or list the watched
		// channels fails the tick; per-channel errors are logged instead.
		fmt.Println("tick:", err)
		return
	}
	fmt.Println("attempted:", attempted, "under deadline pressure:", underPressure)
}

// ExampleDaemon_Run shows how the settler binary drives the daemon: Run
// ticks every interval (TB_SETTLER_TICK) until ctx is cancelled, tightens
// to a short fixed interval while any channel is under deadline pressure,
// and widens back once none is. A failed tick is logged and retried on
// the next interval, never fatal — a stopped settler lets refund
// deadlines approach with nothing being swept. Run has no return value;
// it returns only when ctx is done.
//
// The pool, ledger source and channel factory are the same test stubs
// ExampleDaemon_Tick uses. There is no // Output: line on purpose: Run's
// only observable effects are settlements written to Postgres and slog
// lines, neither of which an example can assert deterministically without
// a live database. Without an Output line `go test` compiles the example
// but does not run it, so the zero-value pool is never dialed. It is here
// to pin the call shape: a signature change breaks the build. The
// behaviour of each tick is covered by the TestDaemon_Tick_* tests above.
func ExampleDaemon_Run() {
	pool := &pgxpool.Pool{}
	d := NewDaemon(pool, NewCalculator(pool), NewSubmitter(pool),
		fixedFactory(newFakeLiveChannel("abc123", nil)), nil,
		fakeLedgerSource{ledger: 4_590_000}, defaultPolicy())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx, 30*time.Second)
	}()

	// Run returns once ctx is done: here when the timeout elapses, in
	// production on SIGINT/SIGTERM.
	<-done
}

// ExampleDaemon constructs a Daemon via NewDaemon, demonstrating that all
// fields are caller-supplied dependencies. In production each field is wired
// from config (database pool, calculator, submitter, channel factory, signer,
// ledger source, policy). The zero value of Daemon has all nil fields; a
// caller must always use NewDaemon to obtain a usable instance.
func ExampleDaemon() {
	var zero Daemon
	fmt.Printf("zero value: pool=%v calc=%v submitter=%v channelFactory=%v signer=%v ledgers=%v policy=%+v\n",
		zero.pool, zero.calc, zero.submitter, zero.channelFactory, zero.signer, zero.ledgers, zero.policy)

	d := NewDaemon(nil, nil, nil, nil, nil, nil, Policy{
		SafetyMarginLedgers: 1440,
		MaxExposure:         big.NewInt(10_000_000),
		MaxExposureAge:      24 * time.Hour,
	})
	fmt.Printf("constructed: pool=%v calc=%v submitter=%v channelFactory=%v signer=%v ledgers=%v policy=%+v\n",
		d.pool, d.calc, d.submitter, d.channelFactory, d.signer, d.ledgers, d.policy)
	// Output:
	// zero value: pool=<nil> calc=<nil> submitter=<nil> channelFactory=<nil> signer=<nil> ledgers=<nil> policy={SafetyMarginLedgers:0 MaxExposure:<nil> MaxExposureAge:0s}
	// constructed: pool=<nil> calc=<nil> submitter=<nil> channelFactory=<nil> signer=<nil> ledgers=<nil> policy={SafetyMarginLedgers:1440 MaxExposure:+10000000 MaxExposureAge:24h0m0s}
}

// exampleChannel is a stand-in for one deployed one-way-channel, used by
// ExampleLiveChannel. It records what was settled so Withdrawn reflects
// it, the way the real contract's withdrawn getter does.
type exampleChannel struct {
	address   string
	withdrawn *big.Int
}

func (c *exampleChannel) Settle(_ context.Context, _ *keypair.Full, amount *big.Int, _ [64]byte) (string, error) {
	c.withdrawn = new(big.Int).Set(amount)
	return "tx-" + c.address[:4], nil
}

func (c *exampleChannel) Withdrawn(context.Context) (*big.Int, error) {
	return new(big.Int).Set(c.withdrawn), nil
}

// ExampleLiveChannel shows how a caller supplies LiveChannel: never
// directly, but through a ChannelFactory that returns a fresh binding per
// channel address, because one-way-channel is deployed once per channel.
// In cmd/settler the factory is a closure around stellar.NewChannel; here
// it builds exampleChannel so the example runs without Soroban RPC.
//
// A caller implements both halves, since the daemon needs both for every
// channel it watches: Settle to sweep against a commitment, and Withdrawn
// to reconcile a settlement left in 'submitting' by a crash. The zero
// value of the interface, nil, is not a usable channel — a factory must
// always return a binding, never nil, or the daemon's first call on it
// panics.
func ExampleLiveChannel() {
	var factory ChannelFactory = func(address string) LiveChannel {
		return &exampleChannel{address: address, withdrawn: big.NewInt(0)}
	}

	ctx := context.Background()
	live := factory(testChannel)

	before, err := live.Withdrawn(ctx)
	if err != nil {
		fmt.Println("withdrawn:", err)
		return
	}
	fmt.Println("withdrawn before:", before)

	// Settle is cumulative: it sweeps up to the commitment's total, not
	// an increment. The signer is passed through untouched; the real
	// binding uses it to sign the transaction.
	var sig [64]byte
	hash, err := live.Settle(ctx, nil, big.NewInt(25_000_000), sig)
	if err != nil {
		fmt.Println("settle:", err)
		return
	}
	fmt.Println("tx:", hash)

	after, err := live.Withdrawn(ctx)
	if err != nil {
		fmt.Println("withdrawn:", err)
		return
	}
	fmt.Println("withdrawn after:", after)
	// Output:
	// withdrawn before: 0
	// tx: tx-CB2I
	// withdrawn after: 25000000
}
