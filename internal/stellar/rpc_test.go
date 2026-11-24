package stellar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// fixtureServer returns an httptest.Server that replays the recorded
// response in testdata/stellar/<name>.json verbatim, and a func returning
// the last request body it received (for asserting what the client sent).
func fixtureServer(t *testing.T, name string) (*httptest.Server, func() []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "stellar", name+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}

	var lastRequest []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastRequest, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []byte { return lastRequest }
}

// ExampleNewClient shows one accepted input and one rejected input. NewClient
// itself never fails — it has no error return and validates nothing at
// construction — so the rejection half of its contract is enforced on the
// first call, where the error wraps a sentinel a caller matches with
// errors.Is rather than a message to compare as a string.
func ExampleNewClient() {
	// Accepted input: a well-formed endpoint URL and a nil *http.Client.
	// NewClient substitutes one bounded by defaultTimeout, observable on
	// the returned Client from this in-package example. Nothing is dialed
	// here — constructing is the whole call.
	c := NewClient("https://soroban-testnet.stellar.org", nil)
	fmt.Println("accepted: default timeout =", c.httpClient.Timeout)

	// Rejected input: a context that is already cancelled. The first call
	// refuses it before any request is sent, and the error identifies
	// itself by wrapping the context.Canceled sentinel — matched with
	// errors.Is, as CONTRIBUTING requires, never by its string.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetLatestLedger(ctx)
	fmt.Println("rejected: errors.Is(err, context.Canceled) =", errors.Is(err, context.Canceled))

	// Output:
	// accepted: default timeout = 30s
	// rejected: errors.Is(err, context.Canceled) = true
}

// ExampleRPCError_Error demonstrates the stable protocol error message
// callers see when Soroban RPC returns a JSON-RPC error.
func ExampleRPCError_Error() {
	err := &RPCError{Code: -32602, Message: "invalid_xdr"}
	fmt.Println(err.Error())
	// Output:
	// soroban rpc: code -32602: invalid_xdr
}

// exampleFixtureClient returns a Client pointed at a loopback httptest.Server
// that replays the recorded response in testdata/stellar/<name>.json, so an
// example runs offline against a real testnet response rather than a
// hand-invented one. The returned func stops the server.
func exampleFixtureClient(name string) (*Client, func()) {
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "stellar", name+".json"))
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	return NewClient(srv.URL, nil), srv.Close
}

// ExampleGetLatestLedgerResult shows that a caller never fills in a
// GetLatestLedgerResult itself: it has no request-side fields, and every
// field is set by GetLatestLedger from the RPC node's response. The zero
// value's Sequence of 0 is not a ledger, so a caller only trusts a result
// that came back with a nil error. Sequence is what the collector and
// settler read as "the current ledger"; CloseTime is a unix timestamp in
// seconds, carried as a string.
func ExampleGetLatestLedgerResult() {
	var zero GetLatestLedgerResult
	fmt.Printf("zero value: sequence=%d closeTime=%q\n", zero.Sequence, zero.CloseTime)

	// Replays the recorded testnet getLatestLedger response
	// (testdata/stellar/get_latest_ledger.json).
	client, stop := exampleFixtureClient("get_latest_ledger")
	defer stop()

	latest, err := client.GetLatestLedger(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("sequence:", latest.Sequence)
	fmt.Println("protocol:", latest.ProtocolVersion)

	closeUnix, err := strconv.ParseInt(latest.CloseTime, 10, 64)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("closed at:", time.Unix(closeUnix, 0).UTC().Format(time.RFC3339))

	// Output:
	// zero value: sequence=0 closeTime=""
	// sequence: 4611229
	// protocol: 28
	// closed at: 2026-09-10T22:35:32Z
}

// ExampleGetLedgerEntriesResult shows a GetLedgerEntriesResult as
// GetLedgerEntries fills it in: the caller supplies only the keys, and the
// RPC node supplies every field of the result. A key with no live entry is
// simply absent from Entries rather than an error, so the zero value's empty
// Entries reads the same as "not found" (fetchSequenceNumber in invoke.go
// relies on exactly that); a caller matches entries by Key rather than by
// position. LatestLedger is the ledger the node answered at, which is what a
// LiveUntilLedgerSeq is measured against.
func ExampleGetLedgerEntriesResult() {
	var zero GetLedgerEntriesResult
	fmt.Println("zero value: entries =", len(zero.Entries), "latestLedger =", zero.LatestLedger)

	// Replays the recorded testnet getLedgerEntries response for contract
	// CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC's instance
	// key (testdata/stellar/get_ledger_entries.json).
	client, stop := exampleFixtureClient("get_ledger_entries")
	defer stop()

	const instanceKey = "AAAABgAAAAHXkotywnA8z+r365/0701QSlWouXn8m0UOoshCtNHOYQAAABQAAAAB"
	result, err := client.GetLedgerEntries(context.Background(), []string{instanceKey})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("entries:", len(result.Entries))
	fmt.Println("latestLedger:", result.LatestLedger)

	for _, entry := range result.Entries {
		if entry.Key != instanceKey {
			continue
		}
		fmt.Println("ledgers until archival:", entry.LiveUntilLedgerSeq-result.LatestLedger)
	}

	// Output:
	// zero value: entries = 0 latestLedger = 0
	// entries: 1
	// latestLedger: 4611230
	// ledgers until archival: 2950337
}

// ExampleLedgerEntryChange reads the StateChanges of a real simulation:
// the recorded testnet simulateTransaction response for a price_book
// publish call (testdata/stellar/pricebook_publish_02_simulate.json),
// decoded with the same JSON tags SimulateTransaction uses. A caller never
// builds a LedgerEntryChange; the RPC node reports one per ledger entry the
// invocation would write. Before is nil for an entry the call creates and
// After is nil for one it deletes, which is why both are pointers: the zero
// value, with an empty Type and two nil pointers, is not a change at all.
// Key is a base64 LedgerKey; Before and After are base64 LedgerEntry XDR.
func ExampleLedgerEntryChange() {
	var zero LedgerEntryChange
	fmt.Printf("zero value: type=%q before=%t after=%t\n", zero.Type, zero.Before != nil, zero.After != nil)

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "stellar", "pricebook_publish_02_simulate.json"))
	if err != nil {
		log.Fatal(err)
	}
	var resp struct {
		Result SimulateTransactionResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		log.Fatal(err)
	}

	for _, change := range resp.Result.StateChanges {
		var key xdr.LedgerKey
		if err := xdr.SafeUnmarshalBase64(change.Key, &key); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s %s: before=%t after=%t\n", change.Type, key.Type, change.Before != nil, change.After != nil)
	}

	// Output:
	// zero value: type="" before=false after=false
	// updated LedgerEntryTypeContractData: before=true after=true
	// updated LedgerEntryTypeContractData: before=true after=true
	// created LedgerEntryTypeContractData: before=false after=true
}

// ExampleLedgerEntryResult reads two LedgerEntryResults as GetLedgerEntries
// fills them in from recorded testnet responses. Every field is set by the
// RPC node, never by the caller. XDR decodes as LedgerEntryData, the same
// way fetchSequenceNumber in invoke.go reads an account's sequence number.
// LiveUntilLedgerSeq only applies to Soroban entries, which carry a TTL: a
// classic account entry has none, the node omits the field, and it decodes
// to 0, the same as the zero value. So 0 here means "no TTL", not "expired".
func ExampleLedgerEntryResult() {
	var zero LedgerEntryResult
	fmt.Println("zero value: lastModified =", zero.LastModifiedLedgerSeq, "liveUntil =", zero.LiveUntilLedgerSeq)

	// A contract instance entry (testdata/stellar/get_ledger_entries.json)
	// and a classic account entry fetched for a sequence number
	// (testdata/stellar/pricebook_publish_01_getledgerentries_account.json).
	fixtures := []struct{ name, key string }{
		{"get_ledger_entries", "AAAABgAAAAHXkotywnA8z+r365/0701QSlWouXn8m0UOoshCtNHOYQAAABQAAAAB"},
		{"pricebook_publish_01_getledgerentries_account", "AAAAAAAAAACxo2Y62NJF7xUghukoiQ0XteQyuJfdetk9gOOSFFdt4A=="},
	}
	for _, f := range fixtures {
		client, stop := exampleFixtureClient(f.name)
		result, err := client.GetLedgerEntries(context.Background(), []string{f.key})
		stop()
		if err != nil {
			log.Fatal(err)
		}
		entry := result.Entries[0]

		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.XDR, &data); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: lastModified = %d liveUntil = %d\n", data.Type, entry.LastModifiedLedgerSeq, entry.LiveUntilLedgerSeq)
	}

	// Output:
	// zero value: lastModified = 0 liveUntil = 0
	// LedgerEntryTypeContractData: lastModified = 4609662 liveUntil = 7561567
	// LedgerEntryTypeAccount: lastModified = 4611396 liveUntil = 0
}

// ExampleTransactionEvents constructs a TransactionEvents the way a caller would,
// demonstrating that both XDR fields are set by the caller and may be empty
// (the zero value) when no events are present. The struct has no fields the
// package fills in — all fields are caller-specified.
func ExampleTransactionEvents() {
	var events TransactionEvents

	// Both XDR slices are omitempty; a caller sets them when events are present
	// and leaves them nil/empty when there are none. This example demonstrates
	// the zero-value construction, which is the most common pattern since callers
	// typically check Status before accessing Events.
	_ = events

	fmt.Printf("zero value: transactionEventsXdr=%v contractEventsXdr=%v\n",
		events.TransactionEventsXDR, events.ContractEventsXDR)

	// No // Output: is asserted because TransactionEvents' contents depend on
	// the transaction's actual events on chain, which vary per invocation.
}

func TestClient_GetLatestLedger(t *testing.T) {
	srv, lastRequest := fixtureServer(t, "get_latest_ledger")
	c := NewClient(srv.URL, nil)

	result, err := c.GetLatestLedger(context.Background())
	if err != nil {
		t.Fatalf("GetLatestLedger returned unexpected error: %v", err)
	}
	if result.ID != "ebf0cf5947dd21719178d9f34bef992e6e5e197db9debc54c5761d1937772321" {
		t.Errorf("ID = %q", result.ID)
	}
	if result.ProtocolVersion != 28 {
		t.Errorf("ProtocolVersion = %d, want 28", result.ProtocolVersion)
	}
	if result.Sequence != 4611229 {
		t.Errorf("Sequence = %d, want 4611229", result.Sequence)
	}
	if result.CloseTime != "1789079732" {
		t.Errorf("CloseTime = %q, want 1789079732", result.CloseTime)
	}
	if result.HeaderXDR == "" || result.MetadataXDR == "" {
		t.Error("HeaderXDR/MetadataXDR should not be empty")
	}

	req := string(lastRequest())
	if !containsAll(req, `"method":"getLatestLedger"`, `"jsonrpc":"2.0"`) {
		t.Errorf("request body %s does not look like a getLatestLedger call", req)
	}
	if containsAll(req, `"params"`) {
		t.Errorf("request body %s should omit params for a no-parameter method", req)
	}
}

func TestClient_GetEvents(t *testing.T) {
	srv, _ := fixtureServer(t, "get_events")
	c := NewClient(srv.URL, nil)

	result, err := c.GetEvents(context.Background(), GetEventsParams{
		StartLedger: 4611150,
		Pagination:  &EventsPagination{Limit: 3},
	})
	if err != nil {
		t.Fatalf("GetEvents returned unexpected error: %v", err)
	}
	if len(result.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(result.Events))
	}
	first := result.Events[0]
	if first.Type != "contract" {
		t.Errorf("Events[0].Type = %q, want contract", first.Type)
	}
	if first.ContractID != "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC" {
		t.Errorf("Events[0].ContractID = %q", first.ContractID)
	}
	if first.ID != "0019804738446950400-0000000000" {
		t.Errorf("Events[0].ID = %q", first.ID)
	}
	if len(first.Topic) != 2 {
		t.Errorf("Events[0].Topic has %d entries, want 2", len(first.Topic))
	}
	if result.Cursor == "" {
		t.Error("Cursor should not be empty")
	}
	if result.LatestLedgerCloseTime == "" {
		t.Error("LatestLedgerCloseTime should not be empty")
	}
}

func TestClient_GetLedgerEntries(t *testing.T) {
	srv, lastRequest := fixtureServer(t, "get_ledger_entries")
	c := NewClient(srv.URL, nil)

	key := "AAAABgAAAAHXkotywnA8z+r365/0701QSlWouXn8m0UOoshCtNHOYQAAABQAAAAB"
	result, err := c.GetLedgerEntries(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("GetLedgerEntries returned unexpected error: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Key != key {
		t.Errorf("Entries[0].Key = %q, want %q", entry.Key, key)
	}
	if entry.LastModifiedLedgerSeq != 4609662 {
		t.Errorf("LastModifiedLedgerSeq = %d, want 4609662", entry.LastModifiedLedgerSeq)
	}
	if entry.LiveUntilLedgerSeq != 7561567 {
		t.Errorf("LiveUntilLedgerSeq = %d, want 7561567", entry.LiveUntilLedgerSeq)
	}
	if entry.ExtXDR == "" {
		t.Error("ExtXDR should not be empty — the live response included it")
	}

	req := string(lastRequest())
	if !containsAll(req, `"method":"getLedgerEntries"`, key) {
		t.Errorf("request body %s does not carry the requested key", req)
	}
}

func TestClient_GetTransaction_Success(t *testing.T) {
	srv, _ := fixtureServer(t, "get_transaction_success")
	c := NewClient(srv.URL, nil)

	result, err := c.GetTransaction(context.Background(), "a55f146e117abdd29fb6643168e10f03fca79a761091355ea03effff3d44739a")
	if err != nil {
		t.Fatalf("GetTransaction returned unexpected error: %v", err)
	}
	if result.Status != GetTransactionStatusSuccess {
		t.Errorf("Status = %q, want %q", result.Status, GetTransactionStatusSuccess)
	}
	if result.ApplicationOrder != 2 {
		t.Errorf("ApplicationOrder = %d, want 2", result.ApplicationOrder)
	}
	if result.EnvelopeXDR == "" || result.ResultXDR == "" || result.ResultMetaXDR == "" {
		t.Error("EnvelopeXDR/ResultXDR/ResultMetaXDR should not be empty for a SUCCESS transaction")
	}
	if len(result.DiagnosticEventsXDR) == 0 {
		t.Error("DiagnosticEventsXDR should not be empty — the live response included several")
	}
}

func TestClient_GetTransaction_NotFound(t *testing.T) {
	srv, _ := fixtureServer(t, "get_transaction_not_found")
	c := NewClient(srv.URL, nil)

	result, err := c.GetTransaction(context.Background(), "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("GetTransaction returned unexpected error: %v", err)
	}
	if result.Status != GetTransactionStatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, GetTransactionStatusNotFound)
	}
	// The live node returns these present-but-zero for NOT_FOUND rather than
	// omitting them; decoding must not choke on that, and callers must gate
	// on Status rather than treat a zero Ledger as "unknown".
	if result.Ledger != 0 || result.ApplicationOrder != 0 {
		t.Errorf("expected zero-valued Ledger/ApplicationOrder for NOT_FOUND, got %d/%d", result.Ledger, result.ApplicationOrder)
	}
}

func TestClient_SimulateTransaction_Success(t *testing.T) {
	srv, _ := fixtureServer(t, "simulate_transaction_success")
	c := NewClient(srv.URL, nil)

	result, err := c.SimulateTransaction(context.Background(), SimulateTransactionParams{Transaction: "AAAA"})
	if err != nil {
		t.Fatalf("SimulateTransaction returned unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("Error = %q, want empty for a successful simulation", result.Error)
	}
	if result.MinResourceFee != "90353" {
		t.Errorf("MinResourceFee = %q, want 90353", result.MinResourceFee)
	}
	if len(result.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(result.Results))
	}
	if result.Cost == nil || result.Cost.CPUInsns != "1635562" {
		t.Errorf("Cost = %+v, want CPUInsns 1635562", result.Cost)
	}
}

// TestClient_SimulateTransaction_SimulationError covers the case CLAUDE.md
// calls out for custody's commitment verification: a simulation failure is
// carried in the result's own Error field with HTTP 200 and no top-level
// JSON-RPC error, not raised as a Go error from the call itself — the
// caller must check Error explicitly.
func TestClient_SimulateTransaction_SimulationError(t *testing.T) {
	srv, _ := fixtureServer(t, "simulate_transaction_error")
	c := NewClient(srv.URL, nil)

	result, err := c.SimulateTransaction(context.Background(), SimulateTransactionParams{Transaction: "not-valid-base64-xdr"})
	if err != nil {
		t.Fatalf("SimulateTransaction returned a Go error for a simulation-level failure: %v", err)
	}
	if result.Error == "" {
		t.Error("Error should be set for a failed simulation")
	}
}

func TestClient_SendTransaction_Pending(t *testing.T) {
	srv, _ := fixtureServer(t, "send_transaction_pending")
	c := NewClient(srv.URL, nil)

	result, err := c.SendTransaction(context.Background(), "AAAA")
	if err != nil {
		t.Fatalf("SendTransaction returned unexpected error: %v", err)
	}
	if result.Status != SendTransactionStatusPending {
		t.Errorf("Status = %q, want %q", result.Status, SendTransactionStatusPending)
	}
	if result.Hash != "d8ec9b68780314ffdfdfc2194b1b35dd27d7303c3bceaef6447e31631a1419dc" {
		t.Errorf("Hash = %q", result.Hash)
	}
}

// TestClient_SendTransaction_RPCError covers a protocol-level JSON-RPC
// error (malformed XDR): this must surface as a Go error, unlike
// simulateTransaction's own result.Error.
func TestClient_SendTransaction_RPCError(t *testing.T) {
	srv, _ := fixtureServer(t, "send_transaction_error")
	c := NewClient(srv.URL, nil)

	_, err := c.SendTransaction(context.Background(), "not-valid-base64-xdr")
	if err == nil {
		t.Fatal("SendTransaction returned nil error for an RPC-level error response")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error %v does not wrap *RPCError", err)
	}
	if rpcErr.Code != -32602 {
		t.Errorf("Code = %d, want -32602", rpcErr.Code)
	}
	if rpcErr.Message != "invalid_xdr" {
		t.Errorf("Message = %q, want invalid_xdr", rpcErr.Message)
	}
}

func TestClient_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, nil)
	_, err := c.GetLatestLedger(context.Background())
	if err == nil {
		t.Fatal("GetLatestLedger returned nil error for a 503 response")
	}
	if !containsAll(err.Error(), "503") {
		t.Errorf("error %q does not mention the HTTP status", err.Error())
	}
}

func TestClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, nil)
	_, err := c.GetLatestLedger(context.Background())
	if err == nil {
		t.Fatal("GetLatestLedger returned nil error for a malformed JSON body")
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.GetLatestLedger(ctx)
	if err == nil {
		t.Fatal("GetLatestLedger returned nil error for an already-cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not wrap context.Canceled", err)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// ExampleSimulateTransactionParams builds the request SimulateTransaction
// sends. Transaction is the only field a caller must set: a base64
// TransactionEnvelope holding a single invokeHostFunction operation, which
// is how invoke.go builds it. ResourceConfig, XDRFormat and AuthMode are
// optional and omitempty, so leaving them unset means the RPC node falls
// back to its own defaults, and they are not sent at all. The zero value
// is not a usable request: it sends an empty transaction, which the node
// rejects.
func ExampleSimulateTransactionParams() {
	zero, err := json.Marshal(SimulateTransactionParams{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("zero value:", string(zero))

	// The envelope is a placeholder here; a real caller passes the
	// base64-encoded envelope it just built (see invoke.go).
	params := SimulateTransactionParams{Transaction: "AAAA"}
	wire, err := json.Marshal(params)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("minimal:", string(wire))

	// Optional tuning: extra instruction headroom for a contract call
	// whose cost varies between simulation and submission.
	params.ResourceConfig = &SimulateResourceConfig{InstructionLeeway: 1000000}
	wire, err = json.Marshal(params)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("with leeway:", string(wire))

	// Replays the recorded testnet simulateTransaction response
	// (testdata/stellar/simulate_transaction_success.json).
	client, stop := exampleFixtureClient("simulate_transaction_success")
	defer stop()
	result, err := client.SimulateTransaction(context.Background(), params)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("minResourceFee:", result.MinResourceFee)

	// Output:
	// zero value: {"transaction":""}
	// minimal: {"transaction":"AAAA"}
	// with leeway: {"transaction":"AAAA","resourceConfig":{"instructionLeeway":1000000}}
	// minResourceFee: 90353
}

// ExampleSimulateTransactionResult reads two SimulateTransactionResults as
// SimulateTransaction fills them in from recorded testnet responses. A
// caller never sets any field; the RPC node sets all of them. The important
// part is the Error field: a failed simulation still returns HTTP 200 with
// no JSON-RPC error, so SimulateTransaction returns a nil Go error, and the
// caller must check result.Error before trusting anything else in it. The
// zero value has an empty Error, so on its own it looks like a success.
// That is why a caller only reads a result that came back with a nil error,
// and then checks Error on it.
func ExampleSimulateTransactionResult() {
	var zero SimulateTransactionResult
	fmt.Printf("zero value: error=%q results=%d cost=%t\n", zero.Error, len(zero.Results), zero.Cost != nil)

	for _, fixture := range []string{"simulate_transaction_success", "simulate_transaction_error"} {
		client, stop := exampleFixtureClient(fixture)
		result, err := client.SimulateTransaction(context.Background(), SimulateTransactionParams{Transaction: "AAAA"})
		stop()
		if err != nil {
			// The simulation could not run at all (transport or JSON-RPC error).
			log.Fatal(err)
		}
		if result.Error != "" {
			// The simulation ran and failed.
			fmt.Println("simulation failed:", result.Error)
			continue
		}
		ret, err := UnmarshalScValBase64(result.Results[0].XDR)
		if err != nil {
			log.Fatal(err)
		}
		value, err := DecodeU32(ret)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("return value:", value)
		fmt.Println("minResourceFee:", result.MinResourceFee)
		fmt.Println("cpuInsns:", result.Cost.CPUInsns)
		fmt.Println("needs restore:", result.RestorePreamble != nil)
	}

	// Output:
	// zero value: error="" results=0 cost=false
	// return value: 12
	// minResourceFee: 90353
	// cpuInsns: 1635562
	// needs restore: false
	// simulation failed: Could not unmarshal transaction
}

// ExampleEventsPagination shows the two ways a caller fills in an
// EventsPagination, matching the indexer's ingestion loop. The first page
// carries only a Limit, with the ledger range on GetEventsParams.StartLedger.
// Every later page carries the Cursor copied from the previous
// GetEventsResult, and no StartLedger, because the RPC API takes a ledger
// range or a cursor but not both. The client does not check that rule; the
// RPC node rejects a request that breaks it. Both fields are omitempty, so
// the zero value sends an empty object and the node uses its default page
// size.
func ExampleEventsPagination() {
	zero, err := json.Marshal(EventsPagination{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("zero value:", string(zero))

	// Replays the recorded testnet getEvents response
	// (testdata/stellar/get_events.json) for both pages, so the second
	// request is only shown, not paged forward for real.
	client, stop := exampleFixtureClient("get_events")
	defer stop()

	first := GetEventsParams{
		StartLedger: 4611150,
		Pagination:  &EventsPagination{Limit: 3},
	}
	wire, err := json.Marshal(first.Pagination)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("first page:", string(wire))

	page, err := client.GetEvents(context.Background(), first)
	if err != nil {
		log.Fatal(err)
	}

	// The next page resumes from the result's Cursor. StartLedger is left
	// at zero so it is omitted from the request.
	next := GetEventsParams{
		Pagination: &EventsPagination{Cursor: page.Cursor, Limit: 3},
	}
	wire, err = json.Marshal(next)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("next page:", string(wire))

	// Output:
	// zero value: {}
	// first page: {"limit":3}
	// next page: {"pagination":{"cursor":"0019804738446950400-0000000002","limit":3}}
}

// ExampleGetEventsParams builds the request GetEvents sends, in the two
// forms the indexer uses. Every field is optional and omitempty, so the zero
// value marshals to an empty object. That is not a usable request: with no
// StartLedger and no Cursor the RPC node has nowhere to start, and rejects
// it. A caller sets exactly one starting point: StartLedger for the first
// call, or Pagination.Cursor to resume. Filters narrows the call to the
// contracts the caller cares about. XDRFormat "base64" asks for the default
// encoding that EventInfo.Topic and EventInfo.Value are decoded from.
func ExampleGetEventsParams() {
	zero, err := json.Marshal(GetEventsParams{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("zero value:", string(zero))

	filters := []EventFilter{{
		Type:        "contract",
		ContractIDs: []string{"CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"},
	}}

	// First call: start from a ledger.
	fromLedger := GetEventsParams{
		StartLedger: 4611150,
		Filters:     filters,
		Pagination:  &EventsPagination{Limit: 3},
		XDRFormat:   "base64",
	}
	wire, err := json.Marshal(fromLedger)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("from ledger:", string(wire))

	// Later calls: resume from a stored cursor instead of a ledger.
	fromCursor := GetEventsParams{
		Filters:    filters,
		Pagination: &EventsPagination{Cursor: "0019804738446950400-0000000002", Limit: 3},
		XDRFormat:  "base64",
	}
	wire, err = json.Marshal(fromCursor)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("from cursor:", string(wire))

	// Replays the recorded testnet getEvents response
	// (testdata/stellar/get_events.json).
	client, stop := exampleFixtureClient("get_events")
	defer stop()
	result, err := client.GetEvents(context.Background(), fromLedger)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("events:", len(result.Events))

	// Output:
	// zero value: {}
	// from ledger: {"startLedger":4611150,"filters":[{"type":"contract","contractIds":["CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"]}],"pagination":{"limit":3},"xdrFormat":"base64"}
	// from cursor: {"filters":[{"type":"contract","contractIds":["CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"]}],"pagination":{"cursor":"0019804738446950400-0000000002","limit":3},"xdrFormat":"base64"}
	// events: 3
}

// ExampleEventInfo reads EventInfos as GetEvents fills them in from a
// recorded testnet response. A caller never sets any field; the RPC node
// sets all of them. Topic and Value are base64 ScVals, not plain strings,
// so a caller decodes them before use (MatchesTopic does this for
// all-Symbol topics). ID is unique per event and is what the indexer
// de-duplicates on. InSuccessfulContractCall matters: an event can be
// emitted by a call that later failed, and its effects were rolled back, so
// a caller must not treat it as something that happened on chain. The zero
// value has an empty ID and no topics, so it is not an event.
func ExampleEventInfo() {
	var zero EventInfo
	fmt.Printf("zero value: id=%q topics=%d\n", zero.ID, len(zero.Topic))

	// Replays the recorded testnet getEvents response
	// (testdata/stellar/get_events.json): three fee events from the native
	// asset contract, topic ("fee", payer) and value i128 stroops.
	client, stop := exampleFixtureClient("get_events")
	defer stop()
	result, err := client.GetEvents(context.Background(), GetEventsParams{
		StartLedger: 4611150,
		Pagination:  &EventsPagination{Limit: 3},
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, ev := range result.Events {
		if !ev.InSuccessfulContractCall {
			fmt.Println("skip", ev.ID, "(call failed)")
			continue
		}
		name, err := UnmarshalScValBase64(ev.Topic[0])
		if err != nil {
			log.Fatal(err)
		}
		sym, err := DecodeSymbol(name)
		if err != nil {
			log.Fatal(err)
		}
		val, err := UnmarshalScValBase64(ev.Value)
		if err != nil {
			log.Fatal(err)
		}
		amount, err := DecodeI128(val)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s ledger=%d %s=%s\n", ev.ID, ev.Ledger, sym, amount)
	}

	// Output:
	// zero value: id="" topics=0
	// skip 0019804738446950400-0000000000 (call failed)
	// 0019804738446950400-0000000001 ledger=4611150 fee=26931
	// 0019804738446950400-0000000002 ledger=4611150 fee=17211
}

// ExampleGetEventsResult reads a GetEventsResult as GetEvents fills it in
// from a recorded testnet response. A caller never sets any field. Cursor is
// the field a caller keeps: it is where the next call resumes (via
// EventsPagination.Cursor), and the indexer stores it in the same
// transaction as the events it read. OldestLedger and LatestLedger bound
// the node's retention window; a StartLedger below OldestLedger cannot be
// served. The close times are unix seconds carried as strings. The zero
// value has an empty Cursor and no events, so a caller only reads a result
// that came back with a nil error.
func ExampleGetEventsResult() {
	var zero GetEventsResult
	fmt.Printf("zero value: events=%d cursor=%q\n", len(zero.Events), zero.Cursor)

	// Replays the recorded testnet getEvents response
	// (testdata/stellar/get_events.json).
	client, stop := exampleFixtureClient("get_events")
	defer stop()
	result, err := client.GetEvents(context.Background(), GetEventsParams{
		StartLedger: 4611150,
		Pagination:  &EventsPagination{Limit: 3},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("events:", len(result.Events))
	fmt.Println("resume from:", result.Cursor)
	fmt.Printf("retention: ledgers %d..%d\n", result.OldestLedger, result.LatestLedger)

	closeUnix, err := strconv.ParseInt(result.LatestLedgerCloseTime, 10, 64)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("latest closed at:", time.Unix(closeUnix, 0).UTC().Format(time.RFC3339))

	// Output:
	// zero value: events=0 cursor=""
	// events: 3
	// resume from: 0019804738446950400-0000000002
	// retention: ledgers 4490271..4611230
	// latest closed at: 2026-09-10T22:35:37Z
}

// ExampleSendTransactionResult reads a SendTransactionResult as
// SendTransaction fills it in from a recorded response. A caller
// sets none of these fields — every one of them comes from the node. The
// field that matters is Status, for the reason the type's own doc comment
// gives: PENDING means the node accepted the envelope for inclusion, not
// that the invocation succeeded, so Hash is something to poll
// GetTransaction with rather than a receipt.
//
// This example switches on all four documented constants. invoke.go does
// not: it handles ERROR, PENDING and DUPLICATE and lets TRY_AGAIN_LATER
// fall through to its default as an "unexpected status" error. So the
// TRY_AGAIN_LATER arm below is the shape a caller writes, not a path
// invoke.go takes today — worth knowing before assuming congestion is
// retried on your behalf.
//
// ErrorResultXDR and DiagnosticEventsXDR are omitempty and arrive only
// alongside an ERROR status, so on an accepted submission they are empty.
// That is not missing data; it is a question the node had no reason to
// answer. Every recorded sendTransaction fixture in testdata/stellar that
// carries a Status at all carries PENDING, so the ERROR branch below is
// shown as the code a caller writes rather than asserted against a response
// invented to exercise it.
//
// A malformed envelope fails differently, and the difference is the last
// thing this example shows: that is a JSON-RPC-level error, so
// SendTransaction returns a nil result and a non-nil error wrapping
// *RPCError. There is no Status to read in that case, which is why the
// error is checked before the struct is touched at all.
func ExampleSendTransactionResult() {
	var zero SendTransactionResult
	fmt.Printf("zero value: status=%q hash=%q errorResultXdr=%q\n", zero.Status, zero.Hash, zero.ErrorResultXDR)

	// Replays testdata/stellar/send_transaction_pending.json. That file's
	// README records it as taken from the official RPC method reference
	// rather than captured live, because submitting a real transaction
	// needs a funded, sequenced, signed envelope — so treat the hash and
	// ledger below as the reference response's, not testnet's.
	client, stop := exampleFixtureClient("send_transaction_pending")
	result, err := client.SendTransaction(context.Background(), "AAAA")
	stop()
	if err != nil {
		log.Fatal(err)
	}

	switch result.Status {
	case SendTransactionStatusPending, SendTransactionStatusDuplicate:
		// Queued for inclusion. Nothing has settled yet.
		fmt.Println("accepted:", result.Status)
	case SendTransactionStatusError:
		// Refused outright; ErrorResultXDR is a base64 TransactionResult
		// saying why, and is the only status that populates it.
		fmt.Println("rejected:", result.ErrorResultXDR)
	case SendTransactionStatusTryAgain:
		// The node is congested and the envelope stays valid, so a caller
		// may resubmit it. Note invoke.go does not reach this arm; see
		// above.
		fmt.Println("retry later")
	default:
		fmt.Println("unexpected status:", result.Status)
	}

	fmt.Println("poll this hash:", result.Hash)
	fmt.Printf("diagnostics=%d errorResultXdr=%q\n", len(result.DiagnosticEventsXDR), result.ErrorResultXDR)

	// LatestLedger and LatestLedgerCloseTime describe the node's own tip
	// when it answered, not the ledger this transaction landed in — it has
	// not landed in one. The close time is unix seconds carried as a string.
	closeUnix, err := strconv.ParseInt(result.LatestLedgerCloseTime, 10, 64)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("node tip: ledger %d closed %s\n", result.LatestLedger, time.Unix(closeUnix, 0).UTC().Format(time.RFC3339))

	// A malformed envelope never reaches a Status. Unlike the fixture
	// above, this one is a live testnet capture of the real error shape
	// (testdata/stellar/send_transaction_error.json).
	badClient, badStop := exampleFixtureClient("send_transaction_error")
	bad, err := badClient.SendTransaction(context.Background(), "not-xdr")
	badStop()
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		log.Fatalf("want *RPCError, got %v", err)
	}
	fmt.Printf("rejected call: result=%t code=%d message=%s\n", bad != nil, rpcErr.Code, rpcErr.Message)

	// Output:
	// zero value: status="" hash="" errorResultXdr=""
	// accepted: PENDING
	// poll this hash: d8ec9b68780314ffdfdfc2194b1b35dd27d7303c3bceaef6447e31631a1419dc
	// diagnostics=0 errorResultXdr=""
	// node tip: ledger 2553978 closed 2023-11-16T18:28:57Z
	// rejected call: result=false code=-32602 message=invalid_xdr
}

// ExampleSimulateCost reads the Cost of a real simulation, and shows the
// nil case that is the reason SimulateTransactionResult.Cost is a pointer.
// A caller never builds a SimulateCost: simulateTransaction reports what
// the trial invocation consumed. The field is optional in the RPC response
// and omitempty here, and six of the seven recorded simulate fixtures in
// testdata/stellar carry no cost at all — in fact no live capture in that
// directory reports one, only the docs-sourced fixture below. So a caller
// that dereferences Cost without a nil check panics against a successful
// simulation. MinResourceFee is the number a caller actually needs in order
// to fund the transaction, and the node sets that one on any simulation
// that did not error — but it too is omitempty, and a failed simulation
// carries neither.
//
// Both fields are decimal strings rather than numbers: the counters are
// u64-ranged and a JSON number is not, so the RPC carries them as strings
// and the caller parses them. The zero value therefore holds two empty
// strings, which does not mean "cost nothing" — it means "no estimate",
// and it does not parse.
func ExampleSimulateCost() {
	var zero SimulateCost
	_, parseErr := strconv.ParseUint(zero.CPUInsns, 10, 64)
	fmt.Printf("zero value: cpuInsns=%q memBytes=%q parses=%t\n", zero.CPUInsns, zero.MemBytes, parseErr == nil)

	// Three recorded simulateTransaction responses. The first, from the
	// official RPC method reference, reports a cost. The second, captured
	// live against the deployed price_book contract, succeeded but reports
	// none. The third, a live capture of a failed simulation, has neither a
	// cost nor a MinResourceFee — which is why Error is checked first.
	for _, fixture := range []string{
		"simulate_transaction_success",
		"pricebook_publish_02_simulate",
		"simulate_transaction_error",
	} {
		client, stop := exampleFixtureClient(fixture)
		result, err := client.SimulateTransaction(context.Background(), SimulateTransactionParams{Transaction: "AAAA"})
		stop()
		if err != nil {
			log.Fatal(err)
		}
		if result.Error != "" {
			fmt.Println("simulation failed:", result.Error)
			continue
		}
		if result.Cost == nil {
			fmt.Printf("%s: no cost reported, minResourceFee=%s\n", fixture, result.MinResourceFee)
			continue
		}
		cpu, err := strconv.ParseUint(result.Cost.CPUInsns, 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		mem, err := strconv.ParseUint(result.Cost.MemBytes, 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: cpuInsns=%d memBytes=%d minResourceFee=%s\n", fixture, cpu, mem, result.MinResourceFee)
	}

	// Output:
	// zero value: cpuInsns="" memBytes="" parses=false
	// simulate_transaction_success: cpuInsns=1635562 memBytes=1295756 minResourceFee=90353
	// pricebook_publish_02_simulate: no cost reported, minResourceFee=1454260
	// simulation failed: Could not unmarshal transaction
}

// ExampleSimulateHostFunctionResult reads the Results of four real
// simulations. A caller never builds one: the node returns one entry per
// host function the transaction invoked, and because simulateTransaction
// only accepts an envelope carrying a single invokeHostFunction operation
// (see SimulateTransactionParams), there is exactly one entry to read — on
// a simulation that did not fail. A failed one carries no Results at all
// (testdata/stellar/simulate_transaction_error.json holds just an error and
// a ledger) and SimulateTransaction still returns a nil Go error for it, so
// Error is checked here before Results is ever indexed.
//
// XDR is the invocation's return value as a base64 ScVal, decoded with this
// package's own helpers — note that a contract returning nothing still
// produces a value here, ScvVoid, not an empty string.
//
// Auth is the interesting half, and it is why this example reads three
// successful fixtures rather than one. The node reports the authorization
// entries the invocation would require, so the count is a property of the
// invocation rather than of the caller: the reference response from the RPC
// docs needs none, price_book's publish needs one covered by the envelope's
// own signature (source-account credentials), and statement_registry's
// resolve_dispute needs two, because CLAUDE.md §4 has it require both the
// operator's and the consumer's authorization.
//
// Reading Auth is not academic. The third fixture is the deliberately
// auth-less first pass of that dual-authorization call, whose whole purpose
// per testdata/stellar/README.md is to learn what authorization is needed —
// its second entry carries address credentials and a nonce, and getting
// that nonce into the footprint before the final simulation is exactly the
// problem invoke.go's two-pass submission exists to solve.
//
// The zero value has an empty XDR and no entries, so it is not a result.
func ExampleSimulateHostFunctionResult() {
	var zero SimulateHostFunctionResult
	fmt.Printf("zero value: xdr=%q auth=%d\n", zero.XDR, len(zero.Auth))

	for _, fixture := range []string{
		"simulate_transaction_success",
		"pricebook_publish_02_simulate",
		"registry_resolve_dispute_simulate_pass1",
		"simulate_transaction_error",
	} {
		client, stop := exampleFixtureClient(fixture)
		result, err := client.SimulateTransaction(context.Background(), SimulateTransactionParams{Transaction: "AAAA"})
		stop()
		if err != nil {
			log.Fatal(err)
		}
		if result.Error != "" {
			// Ran and failed: there is no Results[0] to index.
			fmt.Println("simulation failed:", result.Error)
			continue
		}
		entry := result.Results[0]

		ret, err := UnmarshalScValBase64(entry.XDR)
		if err != nil {
			log.Fatal(err)
		}

		signers := make([]string, 0, len(entry.Auth))
		for _, raw := range entry.Auth {
			var auth xdr.SorobanAuthorizationEntry
			if err := xdr.SafeUnmarshalBase64(raw, &auth); err != nil {
				log.Fatal(err)
			}
			switch auth.Credentials.Type {
			case xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount:
				// Authorized by the transaction's own signature; there is
				// no separate entry to sign.
				signers = append(signers, "source-account")
			case xdr.SorobanCredentialsTypeSorobanCredentialsAddress:
				// A second party, who must sign this entry itself.
				addr, err := auth.Credentials.Address.Address.String()
				if err != nil {
					log.Fatal(err)
				}
				signers = append(signers, "address:"+addr)
			default:
				// The XDR union has four arms, not two: CAP-71 added
				// ADDRESS_V2, and ADDRESS_WITH_DELEGATES follows it.
				// CLAUDE.md §4 records that ADDRESS_V2 authenticates
				// identically, so it is a shape to expect rather than a
				// theoretical one. Naming it keeps the count printed below
				// honest instead of quietly dropping an entry.
				signers = append(signers, "other:"+auth.Credentials.Type.String())
			}
		}

		fmt.Printf("%s:\n  returns %v = %v, auth=%d %v\n", fixture, ret.Type, ret, len(entry.Auth), signers)
	}

	// Output:
	// zero value: xdr="" auth=0
	// simulate_transaction_success:
	//   returns ScValTypeScvU32 = 12, auth=0 []
	// pricebook_publish_02_simulate:
	//   returns ScValTypeScvU32 = 2, auth=1 [source-account]
	// registry_resolve_dispute_simulate_pass1:
	//   returns ScValTypeScvVoid = (void), auth=2 [source-account address:GCWV2YQTXWP72QTU5YX5FM237RCHY6REV6BL6TKBEFCQ5R2QCTX67VUG]
	// simulation failed: Could not unmarshal transaction
}

// ExampleSimulateResourceConfig is the one type in simulateTransaction's
// request that a caller fills in, and even then its single field is
// optional. InstructionLeeway raises the CPU instruction budget the node
// estimates against, which is worth setting when an invocation's real cost
// can land above what simulation predicted — a contract whose work depends
// on ledger state that moves between the simulation and the submission.
//
// Because SimulateTransactionParams.ResourceConfig is a pointer and
// InstructionLeeway is omitempty, there are three distinct wire forms, and
// the middle one is the trap worth seeing: a non-nil config holding a zero
// leeway marshals to an empty object, which asks the node for nothing while
// looking deliberate in Go. Leaving the pointer nil is how a caller says
// "use the node's defaults", and it is what invoke.go does.
func ExampleSimulateResourceConfig() {
	for _, c := range []struct {
		label  string
		config *SimulateResourceConfig
	}{
		{"nil pointer", nil},
		{"zero value", &SimulateResourceConfig{}},
		{"explicit leeway", &SimulateResourceConfig{InstructionLeeway: 3_000_000}},
	} {
		wire, err := json.Marshal(SimulateTransactionParams{Transaction: "AAAA", ResourceConfig: c.config})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-15s %s\n", c.label, wire)
	}

	// The config changes nothing about the result's shape: the node still
	// reports its own estimate, and MinResourceFee is still the number the
	// caller must fund. Replays testdata/stellar/simulate_transaction_success.json,
	// which its README records as taken from the official RPC method
	// reference rather than captured live.
	client, stop := exampleFixtureClient("simulate_transaction_success")
	defer stop()
	result, err := client.SimulateTransaction(context.Background(), SimulateTransactionParams{
		Transaction:    "AAAA",
		ResourceConfig: &SimulateResourceConfig{InstructionLeeway: 3_000_000},
	})
	if err != nil {
		log.Fatal(err)
	}
	// Nil-checked for the reason ExampleSimulateCost gives: this fixture
	// happens to report a cost, and a node that does not is still a
	// successful simulation.
	if result.Cost != nil {
		fmt.Println("estimated cpuInsns:", result.Cost.CPUInsns)
	}
	fmt.Println("minResourceFee:", result.MinResourceFee)

	// Output:
	// nil pointer     {"transaction":"AAAA"}
	// zero value      {"transaction":"AAAA","resourceConfig":{}}
	// explicit leeway {"transaction":"AAAA","resourceConfig":{"instructionLeeway":3000000}}
	// estimated cpuInsns: 1635562
	// minResourceFee: 90353
}

func ExampleRPCError() {
	// RPCError is a JSON-RPC 2.0 protocol-level error returned by any
	// Client method when the node rejects the request before invoking the
	// Soroban method (e.g. malformed XDR, unknown method). It is distinct
	// from a Soroban-level simulation failure, which appears in
	// SimulateTransactionResult.Error instead.
	//
	// Callers receive RPCError wrapped in the error value returned by
	// Client methods; use errors.As to inspect Code and Message. The Data
	// field is optional — the node may omit it; leave it nil when
	// constructing one for tests.
	e := &RPCError{
		Code:    -32600,
		Message: "Invalid Request",
	}
	fmt.Println(e)
	fmt.Println(e.Code)
	// Output:
	// soroban rpc: code -32600: Invalid Request
	// -32600
}

func ExampleRestorePreamble() {
	// RestorePreamble is populated in SimulateTransactionResult.RestorePreamble
	// when the simulated invocation touched an archived ledger entry. Both
	// fields are decoded from the RPC response and passed through to the
	// restore transaction unchanged — callers do not construct or modify them.
	//
	// MinResourceFee is a decimal-string stroop count; read it to budget the
	// restore fee. TransactionData is a base64-encoded SorobanTransactionData
	// XDR blob that must be attached to the restore transaction verbatim.
	rp := RestorePreamble{
		MinResourceFee:  "50000",
		TransactionData: "AAAA", // placeholder; the real value is base64 XDR from the node
	}
	fmt.Println(rp.MinResourceFee)
	// TransactionData is intentionally not printed: its value is opaque
	// base64 XDR that cannot be asserted portably in an example.
	// Output:
	// 50000
}
