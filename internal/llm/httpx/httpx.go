// Package httpx provides shared retry/backoff plumbing for the llm provider
// adapters. Each adapter keeps its own wire types and stream parser; only the
// retrying POST loop and a handful of helpers live here.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/debug"
)

// Request describes a POST to send with retry on transient failures.
type Request struct {
	URL  string
	Body []byte

	// SetHeaders applies provider-specific headers (API key, version, Accept).
	// Content-Type: application/json is set automatically.
	SetHeaders func(*http.Request)

	MaxAttempts int           // total attempts including the first
	BackoffCap  time.Duration // upper bound on the base delay before jitter

	// Retryable decides whether a non-200 status should be retried. Nil means
	// only transport errors retry — non-200 returns immediately.
	Retryable func(int) bool

	// LogTag prefixes debug logs and the formatted error ("<tag>: 429: ..."),
	// typically the provider name.
	LogTag string
}

// Do sends r with exponential-backoff retry. On success the caller owns
// resp.Body; on error the body has been drained and closed. Honors
// Retry-After on retryable statuses.
func Do(ctx context.Context, client *http.Client, r Request) (*http.Response, error) {
	var lastErr error
	var nextWait time.Duration
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		if nextWait > 0 {
			t := time.NewTimer(nextWait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", r.URL, bytes.NewReader(r.Body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if r.SetHeaders != nil {
			r.SetHeaders(req)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				debug.Logf("%s.doRequest: ctx canceled attempt=%d err=%v", r.LogTag, attempt, ctx.Err())
				return nil, ctx.Err()
			}
			debug.Logf("%s.doRequest: transport err attempt=%d err=%v", r.LogTag, attempt, err)
			lastErr = err
			nextWait = BackoffFor(attempt, r.BackoffCap)
			continue
		}
		if resp.StatusCode == 200 {
			debug.Logf("%s.doRequest: 200 attempt=%d", r.LogTag, attempt)
			return resp, nil
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		debug.Logf("%s.doRequest: status=%s attempt=%d body=%q", r.LogTag, resp.Status, attempt, Truncate(string(respBody), 300))
		lastErr = fmt.Errorf("%s: %s: %s", r.LogTag, resp.Status, Truncate(string(respBody), 300))
		if r.Retryable == nil || !r.Retryable(resp.StatusCode) {
			return nil, lastErr
		}
		nextWait = BackoffFor(attempt, r.BackoffCap)
		if d, ok := ParseRetryAfter(resp.Header.Get("Retry-After")); ok {
			nextWait = d
		}
	}
	return nil, lastErr
}

// PostJSON marshals body, calls Do, and returns the full response body.
func PostJSON(ctx context.Context, client *http.Client, body any, r Request) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r.Body = b
	resp, err := Do(ctx, client, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// BackoffFor returns the delay before retry attempt (attempt+1): 1s, 2s, 4s,
// 8s, ... capped at cap, plus up to 50% jitter so concurrent clients don't
// synchronize their retries.
func BackoffFor(attempt int, cap time.Duration) time.Duration {
	base := time.Second << attempt
	if base > cap {
		base = cap
	}
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	return base + jitter
}

// ParseRetryAfter parses an HTTP Retry-After header (delta-seconds or
// HTTP-date) into a wait duration.
func ParseRetryAfter(h string) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// Truncate clips s to n bytes, appending "..." when it had to cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// RetryableStatus is the default retry policy for cloud APIs: transient HTTP
// errors plus Anthropic's 529 "overloaded".
func RetryableStatus(code int) bool {
	switch code {
	case 408, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}
