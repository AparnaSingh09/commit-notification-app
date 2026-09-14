// Package llmerr classifies LLM provider API errors as retryable or
// permanent, so the summary worker doesn't waste retries on something that
// will fail identically every time (a bad model ID, an unscoped API key, an
// empty billing balance - all real errors this project has actually hit).
package llmerr

import "fmt"

// APIError wraps a non-2xx response from an LLM provider (Claude, Groq, or
// any future one), carrying the HTTP status so callers can decide whether
// retrying makes sense.
type APIError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s returned status %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Retryable reports whether retrying the exact same request might succeed.
// 429 (rate limited) and 5xx (server-side) are transient. Everything else -
// 400/401/403/404/... - reflects a request or account problem (bad model
// ID, unscoped or invalid key, insufficient credits, malformed input) that
// will fail identically on every retry.
func (e *APIError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}
