package systemone

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls how a failed request is retried. A request is retried
// after a connection failure, a timeout, or an HTTP 408, 429, or 5xx response,
// and never after its context ends.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the first attempt; zero
	// disables retries.
	MaxRetries int
	// BackoffInitial is the first delay, doubled per retry up to BackoffMax.
	// Zero disables backoff.
	BackoffInitial time.Duration
	// BackoffMax caps the delay. Zero disables backoff.
	BackoffMax time.Duration
	// BackoffJitter is the fraction of each delay removed at random, from zero
	// to one.
	BackoffJitter float64
	// Budget limits retrying: a retry is not made when the time since the call
	// began plus its delay would reach the budget. It does not bound an attempt
	// already started; use the context deadline for that. Zero disables it, and
	// a server's Retry-After is then waited out however long it is.
	Budget time.Duration
}

// DefaultRetryPolicy returns the policy a Client uses unless configured
// otherwise.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     2,
		BackoffInitial: 500 * time.Millisecond,
		BackoffMax:     5 * time.Second,
		BackoffJitter:  0.25,
		Budget:         30 * time.Second,
	}
}

func (p RetryPolicy) validate() error {
	switch {
	case p.MaxRetries < 0:
		return errors.New("MaxRetries is negative")
	case p.BackoffInitial < 0 || p.BackoffMax < 0:
		return errors.New("backoff is negative")
	case p.BackoffJitter < 0 || p.BackoffJitter > 1 || math.IsNaN(p.BackoffJitter):
		return errors.New("BackoffJitter is outside zero to one")
	case p.Budget < 0:
		return errors.New("budget is negative")
	}

	return nil
}

// delay is the wait before the given retry, counted from one. A server's
// Retry-After wins over backoff.
func (p RetryPolicy) delay(retry int, err error) time.Duration {
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.retryAfterSet {
		return apiErr.RetryAfter
	}

	if p.BackoffInitial == 0 || p.BackoffMax == 0 {
		return 0
	}

	backoff := p.BackoffMax
	if shift := retry - 1; shift < 63 && p.BackoffInitial <= p.BackoffMax>>shift {
		backoff = p.BackoffInitial << shift
	}

	jitter := rand.Float64() * p.BackoffJitter //nolint:gosec // Backoff jitter needs no cryptographic randomness.

	return time.Duration(float64(backoff) * (1 - jitter))
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}

	if apiErr, ok := errors.AsType[*APIError](err); ok {
		status := apiErr.StatusCode

		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	}

	return !errors.Is(err, ErrInvalidResponse)
}

// parseRetryAfter reads retry-after-ms, then Retry-After as seconds or an HTTP
// date. A negative Retry-After is ignored.
func parseRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	if raw := header.Get("Retry-After-Ms"); raw != "" {
		if ms, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && ms >= 0 && !math.IsInf(ms, 0) {
			return duration(ms, time.Millisecond), true
		}
	}

	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}

	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds < 0 || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
			return 0, false
		}

		return duration(seconds, time.Second), true
	}

	if at, err := http.ParseTime(raw); err == nil {
		return max(at.Sub(now), 0), true
	}

	return 0, false
}

// duration converts a nonnegative count of units, saturating at the largest
// Duration instead of overflowing.
func duration(count float64, unit time.Duration) time.Duration {
	if nanos := count * float64(unit); nanos < math.MaxInt64 {
		return time.Duration(nanos)
	}

	return math.MaxInt64
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("systemone: waiting to retry: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
