package stellar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func ExampleErrSimulationFailed_Error() {
	err := &ErrSimulationFailed{Message: "contract failed"}
	fmt.Println(err.Error())
	// Output:
	// stellar: simulation failed: contract failed
}

func ExampleInvokeAndSubmitWithAuth() {
	const (
		contractID        = "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M"
		networkPassphrase = "Test SDF Network ; September 2015"
	)
	operator, err := keypair.FromRawSeed([32]byte{1})
	if err != nil {
		log.Fatal(err)
	}
	consumer, err := keypair.FromRawSeed([32]byte{2})
	if err != nil {
		log.Fatal(err)
	}
	operatorAddress, err := ScvAddress(operator.Address())
	if err != nil {
		log.Fatal(err)
	}
	creditedAmount, err := ScvI128(big.NewInt(0))
	if err != nil {
		log.Fatal(err)
	}
	resolutionHash := [32]byte{}
	args := []xdr.ScVal{operatorAddress, ScvU64(1), ScvBytes(resolutionHash[:]), creditedAmount}
	consumerAuth, err := AuthorizeInvocation(networkPassphrase, consumer, contractID, "resolve_dispute", args, 123)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("https://soroban-testnet.stellar.org", nil)
	_, _, err = InvokeAndSubmitWithAuth(ctx, client, networkPassphrase, operator, contractID, "resolve_dispute", args, []xdr.SorobanAuthorizationEntry{consumerAuth})
	fmt.Println(errors.Is(err, context.Canceled))
	// Output: true
}

func ExampleAuthorizeInvocation() {
	const (
		contractID        = "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M"
		networkPassphrase = "Test SDF Network ; September 2015"
	)
	signer, err := keypair.FromRawSeed([32]byte{1})
	if err != nil {
		log.Fatal(err)
	}
	entry, err := AuthorizeInvocation(networkPassphrase, signer, contractID, "close", nil, 1000)
	if err != nil {
		log.Fatal(err)
	}
	// Credentials type is deterministic; the nonce is random each call.
	fmt.Println(entry.Credentials.Type == xdr.SorobanCredentialsTypeSorobanCredentialsAddress)
	fmt.Println(string(entry.RootInvocation.Function.ContractFn.FunctionName))
	// Output:
	// true
	// close
}

// ExampleErrTransactionFailed_Error constructs an ErrTransactionFailed with
// a concrete literal transaction hash and result XDR, then calls its Error()
// method and prints the formatted string. Error names only the hash, never
// the result XDR.
func ExampleErrTransactionFailed_Error() {
	err := &ErrTransactionFailed{
		Hash:      "83806937259d185c694da8e4efd7d8e7e859c8df235f8f77895f244c999068d3",
		ResultXDR: "AAAAAAAAAGT/////AAAAAQAAAAAAAAAY/////gAAAAA=",
	}
	fmt.Println(err.Error())
	// Output:
	// stellar: transaction 83806937259d185c694da8e4efd7d8e7e859c8df235f8f77895f244c999068d3 failed on chain
}

// ExampleBuildCommitment shows the deterministic XDR wire format produced for
// a concrete contract address, network passphrase, and amount. The output is
// 192 bytes (12-byte ScMap header, four keyed ScVal entries: I128 amount,
// Address channel, Symbol domain, BytesN<32> network passphrase hash).
func ExampleBuildCommitment() {
	const (
		contractID  = "CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW"
		networkPass = "Test SDF Network ; September 2015"
	)
	b, err := BuildCommitment(contractID, networkPass, big.NewInt(12345))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(b))
	// Output:
	// 192
}

// ExampleErrSimulationFailed shows how a caller gets an ErrSimulationFailed:
// the package returns it, and the caller matches it with errors.As. A
// caller never constructs one. The package sets Message to
// simulateTransaction's own "error" field, verbatim, when a simulation ran
// but rejected the call. As the type's doc comment says, that is distinct
// from an *RPCError (the request itself was malformed), so the two are
// matched separately. Only the zero value is built by hand here, to show
// that its Message is empty, which is never true of one the package
// returns. The RPC endpoint replays a recorded simulateTransaction response
// whose result carries an error (testdata/stellar/simulate_transaction_error.json).
func ExampleErrSimulationFailed() {
	var zero ErrSimulationFailed
	fmt.Printf("zero value: message=%q\n", zero.Message)

	handler, err := sequencedFixtureHandler(map[string][]string{
		"simulateTransaction": {"simulate_transaction_error"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := NewClient(srv.URL, nil)

	_, err = SimulateCall(context.Background(), client,
		"GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A",
		"CB2IEP4SQ2GC5747HFHNMXEYWEULC5Z5TTTLET2QA4CAA5SYCWAXFKAW",
		"latest", nil)

	var simErr *ErrSimulationFailed
	fmt.Println("simulation rejected:", errors.As(err, &simErr))
	fmt.Println("message:", simErr.Message)
	var rpcErr *RPCError
	fmt.Println("protocol error:", errors.As(err, &rpcErr))

	// Output:
	// zero value: message=""
	// simulation rejected: true
	// message: Could not unmarshal transaction
	// protocol error: false
}

// ExampleErrTransactionFailed shows how a caller gets an
// ErrTransactionFailed: a write such as OpenDispute was accepted and
// settled on chain, but with FAILED status instead of SUCCESS. The package
// fills both fields: Hash is the submitted transaction's hash (also
// returned alongside the error, so the caller can record it), and
// ResultXDR is the TransactionResult the node reported, kept so the
// on-chain reason can be decoded later. A caller never constructs one. It
// matches with errors.As and keeps the hash, because a settled failure has
// already used a sequence number and fee, and retrying it blindly is how a
// fee gets paid twice for nothing. The zero value, with no hash, is never
// returned by the package.
//
// Every response is the recorded testnet open_dispute flow
// (testdata/stellar/registry_open_dispute_*.json) except getTransaction.
// No FAILED settlement has been recorded, so that one response is written
// inline. Its resultXdr is the TransactionResult a Soroban call that
// traps settles with: 100 stroops charged, txFAILED, and one
// invoke_host_function operation result of INVOKE_HOST_FUNCTION_TRAPPED.
func ExampleErrTransactionFailed() {
	var zero ErrTransactionFailed
	fmt.Printf("zero value: hash=%q resultXDR=%q\n", zero.Hash, zero.ResultXDR)

	const failedResultXDR = "AAAAAAAAAGT/////AAAAAQAAAAAAAAAY/////gAAAAA="
	recorded, err := sequencedFixtureHandler(map[string][]string{
		"getLedgerEntries":    {"registry_open_dispute_getledgerentries"},
		"simulateTransaction": {"registry_open_dispute_simulate"},
		"sendTransaction":     {"registry_open_dispute_send"},
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "getTransaction" {
			r.Body = io.NopCloser(bytes.NewReader(body))
			recorded.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"status":       "FAILED",
				"txHash":       "bd49814cf831b4143584892e9b5cc6a8c39d52b63c566211f0a14dd9771c918d",
				"latestLedger": 4612540,
				"ledger":       4612540,
				"resultXdr":    failedResultXDR,
			},
		})
	}))
	defer srv.Close()

	registry := NewStatementRegistry(NewClient(srv.URL, nil), "CB75TTWGP3TLKEDGA2WOLEVCNKLUX6X5KS47GMWGBHUAVES7J55LY25M", "Test SDF Network ; September 2015")
	consumer, err := keypair.ParseFull(registryConsumerSeed)
	if err != nil {
		log.Fatal(err)
	}

	hash, err := registry.OpenDispute(context.Background(), consumer, "GBUSXHXV53RH2DXO4OJZXJY7AULCBFFO6DHGAAL6XK77BG5V2NSE253A", 3, sha256.Sum256([]byte("registry-capture-reason")))

	var txErr *ErrTransactionFailed
	fmt.Println("failed on chain:", errors.As(err, &txErr))
	fmt.Println("hash:", txErr.Hash)
	fmt.Println("same hash returned:", hash == txErr.Hash)
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(txErr.ResultXDR, &result); err != nil {
		log.Fatal(err)
	}
	fmt.Println("result:", result.Result.Code, "fee charged:", result.FeeCharged)
	fmt.Println("operation:", (*result.Result.Results)[0].Tr.InvokeHostFunctionResult.Code)

	// Output:
	// zero value: hash="" resultXDR=""
	// failed on chain: true
	// hash: bd49814cf831b4143584892e9b5cc6a8c39d52b63c566211f0a14dd9771c918d
	// same hash returned: true
	// result: TransactionResultCodeTxFailed fee charged: 100
	// operation: InvokeHostFunctionResultCodeInvokeHostFunctionTrapped
}
