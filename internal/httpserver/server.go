// Package httpserver builds the service HTTP servers with one shared timeout policy.
package httpserver

import (
	"fmt"
	"net/http"
	"time"
)

// Timeouts bounds request headers, request processing, response writes, and
// keep-alive connections for each service's HTTP server.
type Timeouts struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
}

// New constructs an HTTP server after verifying every timeout is enabled.
func New(addr string, handler http.Handler, timeouts Timeouts) (*http.Server, error) {
	for _, timeout := range []struct {
		name  string
		value time.Duration
	}{
		{"ReadHeaderTimeout", timeouts.ReadHeader},
		{"ReadTimeout", timeouts.Read},
		{"WriteTimeout", timeouts.Write},
		{"IdleTimeout", timeouts.Idle},
	} {
		if timeout.value <= 0 {
			return nil, fmt.Errorf("httpserver: %s must be positive, got %s", timeout.name, timeout.value)
		}
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: timeouts.ReadHeader,
		ReadTimeout:       timeouts.Read,
		WriteTimeout:      timeouts.Write,
		IdleTimeout:       timeouts.Idle,
	}, nil
}
