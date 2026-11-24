package stellar

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// realOperator is a real operator address with real published price book
// versions on the deployed testnet price_book contract (§4:
// CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW), discovered via
// a live getEvents call against that contract. Used only as a fixed value
// the recorded fixtures below were captured against — these tests never
// touch the network themselves.
const realOperator = "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM"

// unpublishedOperator has never published a price book on the contract
// above — used to capture the real NotFound error shape.
const unpublishedOperator = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"

const realPriceBookContractID = "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"

// readFixtureRaw loads a recorded RPC response by fixture name. It takes
// no *testing.T — unlike loadFixtureRaw — so a doc Example, which cannot
// receive one, can replay the same recorded responses this file's tests do.
func readFixtureRaw(name string) ([]byte, error) {
	path := filepath.Join("..", "..", "testdata", "stellar", name+".json")
	return os.ReadFile(path)
}

func loadFixtureRaw(t *testing.T, name string) []byte {
	t.Helper()
	data, err := readFixtureRaw(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// singleFixtureServer replays one recorded response for every request —
// enough for any PriceBook read, which issues exactly one simulateTransaction
// call.
func singleFixtureServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	data := loadFixtureRaw(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// methodRoutedServer dispatches each request to the fixture named by
// byMethod[<rpc method>], for replaying a multi-call flow (the write path:
// getLedgerEntries, simulateTransaction, sendTransaction, getTransaction)
// deterministically. getTransaction is called repeatedly while polling;
// the same fixture (the final, settled one) is returned every time, which
// is fine since these tests only exercise the "already settled" case.
func methodRoutedServer(t *testing.T, byMethod map[string]string) *httptest.Server {
	t.Helper()
	fixtures := make(map[string][]byte, len(byMethod))
	for method, name := range byMethod {
		fixtures[method] = loadFixtureRaw(t, name)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, ok := fixtures[req.Method]
		if !ok {
			http.Error(w, "no fixture routed for method "+req.Method, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPriceBook_Latest(t *testing.T) {
	srv := singleFixtureServer(t, "pricebook_latest")
	pb := NewPriceBook(NewClient(srv.URL, nil), realPriceBookContractID, "")

	got, err := pb.Latest(context.Background(), realOperator)
	if err != nil {
		t.Fatalf("Latest returned unexpected error: %v", err)
	}
	if got != 11 {
		t.Errorf("Latest = %d, want 11 (the real on-chain value at capture time)", got)
	}
}

func TestPriceBook_Latest_NotFound(t *testing.T) {
	srv := singleFixtureServer(t, "pricebook_latest_not_found")
	pb := NewPriceBook(NewClient(srv.URL, nil), realPriceBookContractID, "")

	_, err := pb.Latest(context.Background(), unpublishedOperator)
	if err == nil {
		t.Fatal("Latest returned nil error for an operator with no published price book")
	}
	var simErr *ErrSimulationFailed
	if !errors.As(err, &simErr) {
		t.Fatalf("error %v does not wrap *ErrSimulationFailed", err)
	}
	// The real contract's NotFound is error discriminant 2 (§4); the
	// simulation's diagnostic text carries it as "Error(Contract, #2)".
	if !strings.Contains(simErr.Message, "#2") {
		t.Errorf("error message %q does not mention the NotFound discriminant", simErr.Message)
	}
}

func TestPriceBook_VersionAt(t *testing.T) {
	srv := singleFixtureServer(t, "pricebook_version_at")
	pb := NewPriceBook(NewClient(srv.URL, nil), realPriceBookContractID, "")

	got, err := pb.VersionAt(context.Background(), realOperator, 4600000)
	if err != nil {
		t.Fatalf("VersionAt returned unexpected error: %v", err)
	}
	if got != 2 {
		t.Errorf("VersionAt = %d, want 2", got)
	}
}

func TestPriceBook_GetVersion(t *testing.T) {
	srv := singleFixtureServer(t, "pricebook_get_version")
	pb := NewPriceBook(NewClient(srv.URL, nil), realPriceBookContractID, "")

	got, err := pb.GetVersion(context.Background(), realOperator, 2)
	if err != nil {
		t.Fatalf("GetVersion returned unexpected error: %v", err)
	}
	if got.Operator != realOperator {
		t.Errorf("Operator = %q, want %q", got.Operator, realOperator)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if got.URI != "https://example.com/tallybook-testnet-schedule-v2.json" {
		t.Errorf("URI = %q", got.URI)
	}
	if got.EffectiveLedger != 4600000 {
		t.Errorf("EffectiveLedger = %d, want 4600000", got.EffectiveLedger)
	}
	wantHash := "e759c25665bbca00d8f9efc4cfeb5a642613205085fca0bf75f805fdfc7525b7"
	if hex.EncodeToString(got.ScheduleHash[:]) != wantHash {
		t.Errorf("ScheduleHash = %s, want %s", hex.EncodeToString(got.ScheduleHash[:]), wantHash)
	}
}

// TestPriceBook_Publish replays a genuine successful publish flow captured
// live against the testnet contract (a disposable, friendbot-funded test
// account — see testdata/stellar/README.md), covering the full write path:
// fetching the signer's sequence number, simulating to obtain the resource
// footprint and recorded authorization, signing, submitting, and polling
// to a SUCCESS result whose real on-chain return value (not simulation's
// predicted one) is decoded and returned.
func TestPriceBook_Publish(t *testing.T) {
	srv := methodRoutedServer(t, map[string]string{
		"getLedgerEntries":    "pricebook_publish_01_getledgerentries_account",
		"simulateTransaction": "pricebook_publish_02_simulate",
		"sendTransaction":     "pricebook_publish_03_send",
		"getTransaction":      "pricebook_publish_04_gettransaction_success",
	})
	pb := NewPriceBook(NewClient(srv.URL, nil), realPriceBookContractID, "Test SDF Network ; September 2015")

	// The disposable signer whose real, successful publish this fixture
	// set was captured from. Its seed is testnet-only throwaway material
	// with no funds worth protecting; the contract call it authorizes here
	// against a mock server is exactly the one captured live.
	signer, err := keypair.ParseFull("SBE7WEPVPEWTUGU2BV5UKXMATWS5P47HZQQ3VPCVCHMXK37UMS63DUFU")
	if err != nil {
		t.Fatalf("parse signer: %v", err)
	}

	var scheduleHash [32]byte
	copy(scheduleHash[:], []byte("test-schedule-v-probe-capture--")) // placeholder; exact bytes don't matter, the mock server ignores args

	version, hash, err := pb.Publish(context.Background(), signer, scheduleHash, "https://example.com/probe-schedule-capture.json", 4750000)
	if err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}
	if version != 2 {
		t.Errorf("version = %d, want 2 (the real on-chain return value — this signer had already published version 1 in an earlier live capture)", version)
	}
	if hash != "9bf5edffad2c4dc70fa82e26b042dfc8b7d704a0054749154bfac064192fe68c" {
		t.Errorf("hash = %q", hash)
	}
}

// ExampleNewPriceBook shows one accepted and one rejected contract ID.
// NewPriceBook itself never fails — its doc comment promises only a
// binding, and contractID is stored verbatim — so a malformed ID surfaces
// its error on the first call that has to decode it, here Latest, before
// any RPC request is built. The chain back to the caller wraps with %w
// throughout, so errors.Is identifies the sentinel rather than any message
// text.
func ExampleNewPriceBook() {
	// Never contacted: both paths below finish before a request is built.
	client := NewClient("https://soroban-testnet.stellar.org", nil)

	// Accepted: the deployed testnet price_book contract's own C... strkey,
	// stored verbatim alongside the network passphrase.
	pb := NewPriceBook(client, realPriceBookContractID, "Test SDF Network ; September 2015")
	fmt.Println(pb.ContractID)
	fmt.Println(pb.NetworkPassphrase)

	// Rejected: the same strkey with a corrupted version byte — first
	// character changed C -> D, decoding to version byte 24 where the
	// contract's is 16, and strkey defines no version byte 24. Construction
	// still succeeds silently; the first call that needs the contract
	// address fails with strkey's sentinel.
	bad := NewPriceBook(client, "D"+realPriceBookContractID[1:], "")
	_, err := bad.Latest(context.Background(), realOperator)
	fmt.Println(errors.Is(err, strkey.ErrInvalidVersionByte))

	// Output:
	// CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW
	// Test SDF Network ; September 2015
	// true
}

func ExamplePriceBook() {
	// Construct a PriceBook binding via NewPriceBook — all three fields
	// are provided by the caller; the package stores them verbatim.
	client := NewClient("https://soroban-testnet.stellar.org", nil)
	pb := NewPriceBook(client, realPriceBookContractID, "Test SDF Network ; September 2015")

	fmt.Println("ContractID:", pb.ContractID)
	fmt.Println("NetworkPassphrase:", pb.NetworkPassphrase)
	fmt.Println("Client is nil:", pb.Client == nil)

	// Demonstrate usage: Latest only needs the operator argument; the
	// binding stores the rest.
	_, err := pb.Latest(context.Background(), realOperator)
	if err != nil {
		fmt.Println("Latest error (expected on first capture):", err)
	}

	// Output:
	// ContractID: CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW
	// NetworkPassphrase: Test SDF Network ; September 2015
	// Client is nil: false
}

func ExamplePriceBookVersion() {
	// PriceBookVersion mirrors the on-chain struct returned by
	// PriceBook.GetVersion. All fields are decoded from the contract's XDR
	// response — the package fills every field; callers read them.
	//
	// Operator, Version, URI, EffectiveLedger, and ScheduleHash come from
	// the publish call; PublishedLedger is the ledger at which the version
	// was recorded (set by the contract, not the publisher).
	v := PriceBookVersion{
		Operator:        "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM",
		Version:         1,
		ScheduleHash:    [32]byte{0xab, 0xcd}, // first two bytes non-zero; rest zero-padded
		URI:             "https://pay.example.com/schedule-v1.json",
		EffectiveLedger: 5_000_000,
		PublishedLedger: 4_999_900,
	}
	fmt.Println("version:", v.Version)
	fmt.Println("uri:", v.URI)
	fmt.Println("effective_ledger:", v.EffectiveLedger)
	// Output:
	// version: 1
	// uri: https://pay.example.com/schedule-v1.json
	// effective_ledger: 5000000
}
