package pricebook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultMaxScheduleBytes bounds how large a published schedule document
// may be. A price schedule is a list of endpoint prices; a document
// larger than this is not one, and reading an unbounded body off the
// network into memory is not something a billing daemon should do on
// behalf of a URI it did not choose.
const DefaultMaxScheduleBytes = 1 << 20 // 1 MiB

// HTTPFetcher fetches a published schedule document over HTTP.
//
// Only http and https are accepted: the document is fetched over the
// network, and a file:// or data: URI would turn a URI an operator
// published into a local read, which is not what "published at this URI"
// means.
type HTTPFetcher struct {
	// Client performs the request. Nil means http.DefaultClient.
	Client *http.Client
	// MaxBytes bounds the response body. Zero means
	// DefaultMaxScheduleBytes.
	MaxBytes int64
}

// NewHTTPFetcher returns a fetcher that uses client, with the default
// body size limit.
func NewHTTPFetcher(client *http.Client) *HTTPFetcher {
	return &HTTPFetcher{Client: client, MaxBytes: DefaultMaxScheduleBytes}
}

// Fetch retrieves the document at uri.
//
// The errors it returns name the failure, not the URI: the caller knows
// which version it asked for and says so itself, and repeating the URI
// in both places only makes the message harder to read.
func (f *HTTPFetcher) Fetch(ctx context.Context, uri string) ([]byte, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("pricebook: parse schedule URI: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("pricebook: %w: scheme is %q", ErrUnsupportedScheme, parsed.Scheme)
	}

	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	maxBytes := f.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxScheduleBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("pricebook: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pricebook: fetch schedule: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricebook: fetch schedule: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pricebook: read schedule: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("pricebook: schedule is larger than the %d byte limit", maxBytes)
	}
	return body, nil
}

// DefaultHTTPTimeout is the timeout the collector gives a schedule
// fetch when it builds the fetcher. A schedule document is a small file
// on a CDN; a fetch that has not completed in this long is not going to.
const DefaultHTTPTimeout = 10 * time.Second
