package stellar

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
)

const realRegistryContractID = "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M"

// registryOperator and registryConsumer are the two disposable,
// friendbot-funded testnet keypairs the registry_* fixtures were captured
// against — see testdata/stellar/README.md.
const (
	registryOperatorSeed = "SAQJHZ3ZUNSHQB2CWYRGD5JKTJW6YDSCGIFOKHAPESG3DCURKQ4ZTZYM"
	registryConsumerSeed = "SB4A2UA44VMYI2Z6XZPMLRF4XNYZRFMP2IVUVI6KQP5OEV2RGRXHPLT7"
	registryOperator     = "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A"
	registryConsumer     = "GCWV2YQTXWP72QTU5YX5FM237RCHY6REV6BL6TKBEFCQ5R2QCTX67VUG"
)

// sequencedFixtureHandler builds the method-routed replay handler:
// fixtures[method] is replayed in order for each successive request to
// that method, repeating the last one once exhausted (the shape polling
// needs: several NOT_FOUND-equivalent calls then a final settled one —
// though these fixture sets only include the final response, since that's
// all these tests assert on). It takes no *testing.T — unlike
// sequencedFixtureServer — so the Example functions below, which cannot
// receive one, can drive the same recorded responses these tests do.
func sequencedFixtureHandler(fixtures map[string][]string) (http.Handler, error) {
	data := make(map[string][][]byte, len(fixtures))
	for method, names := range fixtures {
		for _, name := range names {
			raw, err := readFixtureRaw(name)
			if err != nil {
				return nil, fmt.Errorf("read fixture %s: %w", name, err)
			}
			data[method] = append(data[method], raw)
		}
	}
	pos := make(map[string]int, len(fixtures))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		responses, ok := data[req.Method]
		if !ok || len(responses) == 0 {
			http.Error(w, "no fixture routed for method "+req.Method, http.StatusInternalServerError)
			return
		}
		i := pos[req.Method]
		if i >= len(responses) {
			i = len(responses) - 1
		}
		pos[req.Method] = i + 1
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responses[i])
	}), nil
}

// sequencedFixtureServer wraps sequencedFixtureHandler in an httptest
// server scoped to the test's lifetime.
func sequencedFixtureServer(t *testing.T, fixtures map[string][]string) *httptest.Server {
	t.Helper()
	handler, err := sequencedFixtureHandler(fixtures)
	if err != nil {
		t.Fatalf("build fixture handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatementRegistry_Anchor(t *testing.T) {
	srv := sequencedFixtureServer(t, map[string][]string{
		"getLedgerEntries":    {"registry_anchor_getledgerentries"},
		"simulateTransaction": {"registry_anchor_simulate"},
		"sendTransaction":     {"registry_anchor_send"},
		"getTransaction":      {"registry_anchor_gettransaction"},
	})
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "Test SDF Network ; September 2015")
	signer, err := keypair.ParseFull(registryOperatorSeed)
	if err != nil {
		t.Fatalf("parse signer: %v", err)
	}

	usageRoot := sha256.Sum256([]byte("registry-capture-usage-root"))
	seq, hash, err := registry.Anchor(context.Background(), signer, registryConsumer,
		4612165, 4612167, usageRoot, 9, registryOperator,
		big.NewInt(3000), big.NewInt(3000), 1, ProtocolX402, nil)
	if err != nil {
		t.Fatalf("Anchor returned unexpected error: %v", err)
	}
	if seq != 3 {
		t.Errorf("seq = %d, want 3 (the real on-chain value)", seq)
	}
	if hash == "" {
		t.Error("hash should not be empty")
	}
}

// ExampleStatementRegistry_Anchor anchors one statement against an RPC
// endpoint that replays the responses a real testnet anchor captured (see
// testdata/stellar/README.md), so seq and tx are the values that call
// actually produced on chain. A real caller points NewClient at the
// network's RPC URL instead of an httptest server; nothing else changes.
func ExampleStatementRegistry_Anchor() {
	handler, err := sequencedFixtureHandler(map[string][]string{
		"getLedgerEntries":    {"registry_anchor_getledgerentries"},
		"simulateTransaction": {"registry_anchor_simulate"},
		"sendTransaction":     {"registry_anchor_send"},
		"getTransaction":      {"registry_anchor_gettransaction"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	signer, err := keypair.ParseFull("SAQJHZ3ZUNSHQB2CWYRGD5JKTJW6YDSCGIFOKHAPESG3DCURKQ4ZTZYM")
	if err != nil {
		log.Fatal(err)
	}
	registry := NewStatementRegistry(
		NewClient(srv.URL, nil),
		"CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M",
		"Test SDF Network ; September 2015",
	)

	usageRoot := sha256.Sum256([]byte("example usage root"))
	seq, tx, err := registry.Anchor(
		context.Background(),
		signer,
		"GCWV2YQTXWP72QTU5YX5FM237RCHY6REV6BL6TKBEFCQ5R2QCTX67VUG",
		4612165,
		4612167,
		usageRoot,
		9,
		"GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM",
		big.NewInt(3000),
		big.NewInt(3000),
		1,
		ProtocolX402,
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("seq=%d\n", seq)
	fmt.Printf("tx=%s\n", tx)
	// Output:
	// seq=3
	// tx=83806937259d185c694da8e4efd7d8e7e859c8df235f8f77895f244c999068d3
}

func TestStatementRegistry_GetStatement(t *testing.T) {
	srv := singleFixtureServer(t, "registry_get_statement")
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "")

	stmt, err := registry.GetStatement(context.Background(), registryOperator, 3)
	if err != nil {
		t.Fatalf("GetStatement returned unexpected error: %v", err)
	}
	if stmt.Operator != registryOperator {
		t.Errorf("Operator = %q", stmt.Operator)
	}
	if stmt.Consumer != registryConsumer {
		t.Errorf("Consumer = %q", stmt.Consumer)
	}
	if stmt.PeriodStart != 4612165 || stmt.PeriodEnd != 4612167 {
		t.Errorf("period = [%d, %d], want [4612165, 4612167]", stmt.PeriodStart, stmt.PeriodEnd)
	}
	if stmt.RequestCount != 9 {
		t.Errorf("RequestCount = %d, want 9", stmt.RequestCount)
	}
	if stmt.AmountBilled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("AmountBilled = %s, want 3000", stmt.AmountBilled)
	}
	if stmt.AmountSettled.Cmp(big.NewInt(3000)) != 0 {
		t.Errorf("AmountSettled = %s, want 3000", stmt.AmountSettled)
	}
	if stmt.PriceBookVersion != 1 {
		t.Errorf("PriceBookVersion = %d, want 1", stmt.PriceBookVersion)
	}
	if stmt.Protocol != ProtocolX402 {
		t.Errorf("Protocol = %q, want %q", stmt.Protocol, ProtocolX402)
	}
	if stmt.Channel != nil {
		t.Errorf("Channel = %v, want nil (protocol is X402)", stmt.Channel)
	}
	if stmt.Status != StatusAnchored {
		t.Errorf("Status = %q, want %q", stmt.Status, StatusAnchored)
	}
	wantRoot := sha256.Sum256([]byte("registry-capture-usage-root"))
	if stmt.UsageRoot != wantRoot {
		t.Errorf("UsageRoot = %x, want %x", stmt.UsageRoot, wantRoot)
	}
}

func TestStatementRegistry_GetStatement_Resolved(t *testing.T) {
	srv := singleFixtureServer(t, "registry_get_statement_resolved")
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "")

	stmt, err := registry.GetStatement(context.Background(), registryOperator, 3)
	if err != nil {
		t.Fatalf("GetStatement returned unexpected error: %v", err)
	}
	if stmt.Status != StatusResolved {
		t.Errorf("Status = %q, want %q", stmt.Status, StatusResolved)
	}
}

func TestStatementRegistry_ListStatements(t *testing.T) {
	srv := singleFixtureServer(t, "registry_list_statements")
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "")

	seqs, err := registry.ListStatements(context.Background(), registryOperator, registryConsumer)
	if err != nil {
		t.Fatalf("ListStatements returned unexpected error: %v", err)
	}
	want := []uint64{1, 2, 3}
	if len(seqs) != len(want) {
		t.Fatalf("got %v, want %v", seqs, want)
	}
	for i := range want {
		if seqs[i] != want[i] {
			t.Errorf("seqs[%d] = %d, want %d", i, seqs[i], want[i])
		}
	}
}

func TestStatementRegistry_VerifyUsage(t *testing.T) {
	srv := singleFixtureServer(t, "registry_verify_usage")
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "")

	usageRoot := sha256.Sum256([]byte("registry-capture-usage-root"))
	ok, err := registry.VerifyUsage(context.Background(), registryOperator, 3, usageRoot, nil)
	if err != nil {
		t.Fatalf("VerifyUsage returned unexpected error: %v", err)
	}
	if !ok {
		t.Error("VerifyUsage = false, want true (leaf equals the usage_root, empty proof)")
	}
}

func TestStatementRegistry_OpenDispute(t *testing.T) {
	srv := sequencedFixtureServer(t, map[string][]string{
		"getLedgerEntries":    {"registry_open_dispute_getledgerentries"},
		"simulateTransaction": {"registry_open_dispute_simulate"},
		"sendTransaction":     {"registry_open_dispute_send"},
		"getTransaction":      {"registry_open_dispute_gettransaction"},
	})
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "Test SDF Network ; September 2015")
	consumerSigner, err := keypair.ParseFull(registryConsumerSeed)
	if err != nil {
		t.Fatalf("parse signer: %v", err)
	}

	reasonHash := sha256.Sum256([]byte("registry-capture-reason"))
	hash, err := registry.OpenDispute(context.Background(), consumerSigner, registryOperator, 3, reasonHash)
	if err != nil {
		t.Fatalf("OpenDispute returned unexpected error: %v", err)
	}
	if hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestStatementRegistry_GetDispute(t *testing.T) {
	srv := singleFixtureServer(t, "registry_get_dispute")
	registry := NewStatementRegistry(NewClient(srv.URL, nil), realRegistryContractID, "")

	dispute, err := registry.GetDispute(context.Background(), registryOperator, 3)
	if err != nil {
		t.Fatalf("GetDispute returned unexpected error: %v", err)
	}
	if dispute.Consumer != registryConsumer {
		t.Errorf("Consumer = %q", dispute.Consumer)
	}
	wantReason := sha256.Sum256([]byte("registry-capture-reason"))
	if dispute.ReasonHash != wantReason {
		t.Errorf("ReasonHash = %x, want %x", dispute.ReasonHash, wantReason)
	}
	if dispute.ResolutionHash != nil {
		t.Errorf("ResolutionHash = %v, want nil (not yet resolved)", dispute.ResolutionHash)
	}
	if dispute.ResolvedLedger != nil {
		t.Errorf("ResolvedLedger = %v, want nil (not yet resolved)", dispute.ResolvedLedger)
	}
	if dispute.AmountCredited.Sign() != 0 {
		t.Errorf("AmountCredited = %s, want 0", dispute.AmountCredited)
	}
}

// TestStatementRegistry_ResolveDispute is the dual-authorization case §4
// calls out explicitly. It replays the exact live-captured flow: two
// simulateTransaction passes (the second re-simulates with the consumer's
// authorization already attached, so the resulting resource footprint
// accounts for the consumer's nonce access — the fix this package needed
// after a first live attempt failed with "trying to access nonce outside
// of the footprint" when that second pass was missing), then send and
// poll to a real SUCCESS.
func TestStatementRegistry_ResolveDispute(t *testing.T) {
	srv := sequencedFixtureServer(t, map[string][]string{
		"getLatestLedger":     {"registry_resolve_dispute_getlatestledger"},
		"getLedgerEntries":    {"registry_resolve_dispute_getledgerentries"},
		"simulateTransaction": {"registry_resolve_dispute_simulate_pass1", "registry_resolve_dispute_simulate_pass2"},
		"sendTransaction":     {"registry_resolve_dispute_send"},
		"getTransaction":      {"registry_resolve_dispute_gettransaction"},
	})
	client := NewClient(srv.URL, nil)
	registry := NewStatementRegistry(client, realRegistryContractID, "Test SDF Network ; September 2015")

	opSigner, err := keypair.ParseFull(registryOperatorSeed)
	if err != nil {
		t.Fatalf("parse operator signer: %v", err)
	}
	csSigner, err := keypair.ParseFull(registryConsumerSeed)
	if err != nil {
		t.Fatalf("parse consumer signer: %v", err)
	}

	latest, err := client.GetLatestLedger(context.Background())
	if err != nil {
		t.Fatalf("GetLatestLedger returned unexpected error: %v", err)
	}

	resolutionHash := sha256.Sum256([]byte("registry-capture-resolution"))
	amountCredited := big.NewInt(300)
	args, err := ResolveDisputeArgs(registryOperator, 3, resolutionHash, amountCredited)
	if err != nil {
		t.Fatalf("ResolveDisputeArgs returned unexpected error: %v", err)
	}
	consumerAuth, err := AuthorizeInvocation("Test SDF Network ; September 2015", csSigner, realRegistryContractID, "resolve_dispute", args, latest.Sequence)
	if err != nil {
		t.Fatalf("AuthorizeInvocation returned unexpected error: %v", err)
	}

	hash, err := registry.ResolveDispute(context.Background(), opSigner, 3, resolutionHash, amountCredited, consumerAuth)
	if err != nil {
		t.Fatalf("ResolveDispute returned unexpected error: %v", err)
	}
	if hash == "" {
		t.Error("hash should not be empty")
	}
}

// ExampleResolveDisputeArgs builds the exact argument vector that both
// parties must authorize for resolve_dispute.
func ExampleResolveDisputeArgs() {
	resolutionHash := sha256.Sum256([]byte("example resolution"))
	amountCredited := big.NewInt(300)
	args, err := ResolveDisputeArgs(
		"GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A",
		3,
		resolutionHash,
		amountCredited,
	)
	if err != nil {
		log.Fatal(err)
	}
	credited, err := DecodeI128(args[3])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("arguments=%d sequence=%d amount_credited=%s\n", len(args), *args[1].U64, credited)
	// Output:
	// arguments=4 sequence=3 amount_credited=300
}

// ExampleStatementRegistry_ResolveDispute resolves a dispute with both the
// operator's and the consumer's authorization attached — the dual-auth case
// CLAUDE.md §4 calls out. consumerAuth is built here with
// AuthorizeInvocation because the consumer's key lives in the consumer's
// process, not the operator's; in production it arrives out of band. The
// RPC endpoint replays the two simulateTransaction passes a real testnet
// resolve_dispute made — the second, with consumerAuth already attached, is
// what puts the consumer's nonce inside the transaction's resource
// footprint (see invoke.go).
func ExampleStatementRegistry_ResolveDispute() {
	handler, err := sequencedFixtureHandler(map[string][]string{
		"getLatestLedger":     {"registry_resolve_dispute_getlatestledger"},
		"getLedgerEntries":    {"registry_resolve_dispute_getledgerentries"},
		"simulateTransaction": {"registry_resolve_dispute_simulate_pass1", "registry_resolve_dispute_simulate_pass2"},
		"sendTransaction":     {"registry_resolve_dispute_send"},
		"getTransaction":      {"registry_resolve_dispute_gettransaction"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	const (
		contractID = "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M"
		passphrase = "Test SDF Network ; September 2015"
	)
	client := NewClient(srv.URL, nil)
	registry := NewStatementRegistry(client, contractID, passphrase)

	operator, err := keypair.ParseFull("SAQJHZ3ZUNSHQB2CWYRGD5JKTJW6YDSCGIFOKHAPESG3DCURKQ4ZTZYM")
	if err != nil {
		log.Fatal(err)
	}
	consumer, err := keypair.ParseFull("SB4A2UA44VMYI2Z6XZPMLRF4XNYZRFMP2IVUVI6KQP5OEV2RGRXHPLT7")
	if err != nil {
		log.Fatal(err)
	}

	latest, err := client.GetLatestLedger(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	resolutionHash := sha256.Sum256([]byte("example resolution hash"))
	amountCredited := big.NewInt(300)
	args, err := ResolveDisputeArgs(operator.Address(), 3, resolutionHash, amountCredited)
	if err != nil {
		log.Fatal(err)
	}
	consumerAuth, err := AuthorizeInvocation(passphrase, consumer, contractID, "resolve_dispute", args, latest.Sequence)
	if err != nil {
		log.Fatal(err)
	}

	tx, err := registry.ResolveDispute(context.Background(), operator, 3, resolutionHash, amountCredited, consumerAuth)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("tx=%s\n", tx)
	// Output:
	// tx=27b25cf1393f2d5904f606d4b55a71406f47fb3b90a67d067cdb9581eb3f231d
}

// ExampleStatement shows which Statement fields come from the caller and
// which come from the contract. A caller never builds a Statement to send:
// it passes the same values as Anchor arguments, and gets a Statement back
// from GetStatement. So the literal below is what a caller writes to check
// an anchored statement against what it meant to anchor. AnchoredLedger and
// Status are left unset because the contract sets them (Status is never an
// argument; see its doc comment). Channel stays nil because Protocol is not
// ProtocolMppSession.
func ExampleStatement() {
	usageRoot := sha256.Sum256([]byte("registry-capture-usage-root"))
	want := Statement{
		Operator:         "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A",
		Consumer:         "GCWV2YQTXWP72QTU5YX5FM237RCHY6REV6BL6TKBEFCQ5R2QCTX67VUG",
		PeriodStart:      4612165,
		PeriodEnd:        4612167,
		UsageRoot:        usageRoot,
		RequestCount:     9,
		Token:            "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A",
		AmountBilled:     big.NewInt(3000),
		AmountSettled:    big.NewInt(3000),
		PriceBookVersion: 1,
		Protocol:         ProtocolX402,
	}
	fmt.Printf("before anchoring: anchoredLedger=%d status=%q channel=%t\n", want.AnchoredLedger, want.Status, want.Channel != nil)

	// Replays the recorded testnet get_statement response for seq 3, the
	// statement ExampleStatementRegistry_Anchor anchored
	// (testdata/stellar/registry_get_statement.json).
	handler, err := sequencedFixtureHandler(map[string][]string{
		"simulateTransaction": {"registry_get_statement"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	registry := NewStatementRegistry(NewClient(srv.URL, nil), "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M", "Test SDF Network ; September 2015")

	got, err := registry.GetStatement(context.Background(), want.Operator, 3)
	if err != nil {
		log.Fatal(err)
	}
	matches := got.Operator == want.Operator &&
		got.Consumer == want.Consumer &&
		got.PeriodStart == want.PeriodStart &&
		got.PeriodEnd == want.PeriodEnd &&
		got.UsageRoot == want.UsageRoot &&
		got.RequestCount == want.RequestCount &&
		got.Token == want.Token &&
		got.AmountBilled.Cmp(want.AmountBilled) == 0 &&
		got.AmountSettled.Cmp(want.AmountSettled) == 0 &&
		got.PriceBookVersion == want.PriceBookVersion &&
		got.Protocol == want.Protocol &&
		got.Channel == nil
	fmt.Println("caller fields match:", matches)
	fmt.Printf("after anchoring: anchoredLedger=%d status=%q\n", got.AnchoredLedger, got.Status)

	// Output:
	// before anchoring: anchoredLedger=0 status="" channel=false
	// caller fields match: true
	// after anchoring: anchoredLedger=4612538 status="Anchored"
}

// ExampleStatementRegistry builds a binding the way a caller does — with
// NewStatementRegistry, setting all three fields — and reads one statement
// through it. The zero value is not usable: it has no Client to send
// requests with, no ContractID to address, and no NetworkPassphrase to sign
// transactions for. The RPC endpoint here replays the recorded testnet
// get_statement response for seq 3 (testdata/stellar/registry_get_statement.json);
// a real caller points NewClient at the network's RPC URL instead.
func ExampleStatementRegistry() {
	var zero StatementRegistry
	fmt.Printf("zero value: client=%t contract=%q passphrase=%q\n", zero.Client != nil, zero.ContractID, zero.NetworkPassphrase)

	handler, err := sequencedFixtureHandler(map[string][]string{
		"simulateTransaction": {"registry_get_statement"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	registry := NewStatementRegistry(
		NewClient(srv.URL, nil),
		"CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M",
		"Test SDF Network ; September 2015",
	)

	stmt, err := registry.GetStatement(context.Background(), "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A", 3)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("statement 3: consumer=%s billed=%s status=%s\n", stmt.Consumer, stmt.AmountBilled, stmt.Status)

	// Output:
	// zero value: client=false contract="" passphrase=""
	// statement 3: consumer=GCWV2YQTXWP72QTU5YX5FM237RCHY6REV6BL6TKBEFCQ5R2QCTX67VUG billed=3000 status=Anchored
}

// ExampleStatus reads a statement's Status the way a caller does: from a
// Statement that GetStatement decoded. As Status's doc comment says, the
// contract sets it and it is never passed as an argument, so a caller only
// ever compares it against the StatusAnchored, StatusDisputed and
// StatusResolved constants — never constructs one to send. The zero value
// "" matches none of them: it means the Statement was not decoded from the
// contract, not that the statement is in some default state. The RPC
// endpoint replays two recorded testnet get_statement responses for seq 3,
// before and after its dispute was resolved.
func ExampleStatus() {
	var zero Status
	fmt.Printf("zero value: %q anchored=%t\n", zero, zero == StatusAnchored)

	for _, fixture := range []string{"registry_get_statement", "registry_get_statement_resolved"} {
		handler, err := sequencedFixtureHandler(map[string][]string{
			"simulateTransaction": {fixture},
		})
		if err != nil {
			log.Fatal(err)
		}
		srv := httptest.NewServer(handler)
		registry := NewStatementRegistry(NewClient(srv.URL, nil), "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M", "Test SDF Network ; September 2015")

		stmt, err := registry.GetStatement(context.Background(), "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A", 3)
		srv.Close()
		if err != nil {
			log.Fatal(err)
		}
		switch stmt.Status {
		case StatusAnchored:
			fmt.Println("Anchored: no dispute opened")
		case StatusDisputed:
			fmt.Println("Disputed: awaiting resolve_dispute")
		case StatusResolved:
			fmt.Println("Resolved: dispute closed")
		default:
			fmt.Printf("unknown status %q\n", stmt.Status)
		}
	}

	// Output:
	// zero value: "" anchored=false
	// Anchored: no dispute opened
	// Resolved: dispute closed
}

func ExampleProtocol() {
	// Protocol is the billing protocol used by a statement. It is set by
	// the caller when anchoring a statement via StatementRegistry.Anchor
	// (passed as the protocol argument) and returned in Statement.Protocol
	// when reading one back. The wire encoding is case-sensitive; always
	// use the named constants, never bare string literals.
	for _, p := range []Protocol{ProtocolX402, ProtocolMppCharge, ProtocolMppSession} {
		fmt.Println(string(p))
	}
	// Output:
	// X402
	// MppCharge
	// MppSession
}
