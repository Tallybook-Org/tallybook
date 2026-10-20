package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/Tallybook-Org/tallybook/internal/merkle"
)

// exampleRecorder is a Recorder that prints what it is handed, standing in
// for the production Recorder (store.RequestStore) so the example below
// goes through Recorder.RecordRequest the way any caller of the interface
// does.
type exampleRecorder struct{}

func (exampleRecorder) RecordRequest(_ context.Context, req RecordedRequest) error {
	fmt.Printf("%s %s consumer %s protocol %s channel %q\n",
		req.Method, req.PathTemplate, req.Consumer, req.Protocol, req.Channel)
	fmt.Printf("%d unit(s) charged %s stroops price v%d observed ledger %d period %d\n",
		req.UnitCount, req.ChargedAmount, req.PriceVersion, req.ObservedLedger, req.PeriodID)
	fmt.Printf("endpoint hash %x\n", req.EndpointHash)
	fmt.Printf("request id %x\n", req.RequestID)
	return nil
}

// ExampleRecordedRequest constructs a RecordedRequest and passes it to a
// Recorder — the use every caller of this type shares, whether that caller
// is a test fake or a production Recorder such as store.RequestStore.
//
// The type is the complete shape of one requests-table row (§5), so
// whoever constructs it sets every field: there is no valid partially
// populated form, and ChargedAmount in particular must be non-nil or a
// Recorder will reject it. The fields the metering middleware derives when
// it builds its own records still have to be derived, not invented, by
// anyone else constructing one: RequestID is fresh per request (the
// middleware draws it from crypto/rand, and the requests table's UNIQUE
// constraint on request_id rejects a reused one), and EndpointHash comes
// from merkle.EndpointHash on the same (method, path template) pair the
// price schedule and every other consumer of this endpoint's identity key
// on (Config's doc comment). Channel is left at its zero value here because
// Protocol is ProtocolX402; it is set if and only if Protocol is
// ProtocolMPPSession, mirroring both RequestInfo.Validate and the
// requests_channel_iff_session CHECK constraint (§5). PeriodID names the
// billing period the request rolls up into — the middleware opens that
// period (or finds the open one) before recording, so it is never
// invented by a caller.
//
// The request ID and ledger number are fixed purely so the Output is
// deterministic; production values come from crypto/rand and the
// LedgerSource.
func ExampleRecordedRequest() {
	// Fresh per request. Fixed here only for a deterministic Output.
	requestIDBytes, err := hex.DecodeString("6a1f" + strings.Repeat("0", 56) + "abcd")
	if err != nil {
		fmt.Println("bad request id:", err)
		return
	}
	var requestID [32]byte
	copy(requestID[:], requestIDBytes)

	rec := RecordedRequest{
		RequestID:     requestID,
		Operator:      "GDOLCHAOYP63BEHGAUJJS5IVQNUXLPBWCHO2HZRBTZRU52XBMW2TRJLM",
		Consumer:      "GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY",
		EndpointHash:  merkle.EndpointHash("GET", "/v1/items"),
		Method:        "GET",
		PathTemplate:  "/v1/items",
		UnitCount:     1,
		PriceVersion:  1,
		ChargedAmount: big.NewInt(1000),
		Protocol:      ProtocolX402,
		// Channel left at its zero value: set only when Protocol is
		// ProtocolMPPSession.
		ObservedLedger: 1500,
		PeriodID:       7,
	}

	recorder := exampleRecorder{}
	if err := recorder.RecordRequest(context.Background(), rec); err != nil {
		fmt.Println("record failed:", err)
		return
	}
	// Output:
	// GET /v1/items consumer GAJ46LDZSSYAB4YY6VMPM763P652ZROT7TBG3YYE3BZBAUZDXYDDK6OY protocol x402 channel ""
	// 1 unit(s) charged 1000 stroops price v1 observed ledger 1500 period 7
	// endpoint hash 79a8cbca8c21b745b034907bd0d84cad5dc580f9370b446c9f4030d4e4affb03
	// request id 6a1f00000000000000000000000000000000000000000000000000000000abcd
}
