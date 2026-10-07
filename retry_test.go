package systemone

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func fastRetry(maxRetries int) RetryPolicy {
	return RetryPolicy{MaxRetries: maxRetries, BackoffInitial: time.Millisecond, BackoffMax: time.Millisecond}
}

func TestRetryHonorsRetryAfterAndCountsAttempts(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, attempt int) {
		if attempt == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			writeJSON(w, http.StatusTooManyRequests, `{"error":"slow down"}`)

			return
		}

		writeJSON(w, http.StatusOK, `{"models":[]}`)
	})

	client := newClient(t, server.URL, WithRetryPolicy(RetryPolicy{MaxRetries: 1, BackoffInitial: time.Hour, BackoffMax: time.Hour}))
	if _, err := client.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(calls.all()) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls.all()))
	}

	if got := calls.all()[1].header.Get(retryCountHeader); got != "1" {
		t.Errorf("retry count = %q, want 1", got)
	}
}

func TestRetryStopsAfterMaxRetries(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusServiceUnavailable, `{}`)
	})

	_, err := newClient(t, server.URL, WithRetryPolicy(fastRetry(2))).ListModels(context.Background())
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want APIError 503", err)
	}

	if len(calls.all()) != 3 {
		t.Errorf("calls = %d, want 3", len(calls.all()))
	}

	if got := calls.all()[2].header.Get(retryCountHeader); got != "2" {
		t.Errorf("last retry count = %q, want 2", got)
	}
}

func TestRetrySkipsNonRetryableStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				writeJSON(w, status, `{}`)
			})

			if _, err := newClient(t, server.URL, WithRetryPolicy(fastRetry(3))).ListModels(context.Background()); err == nil {
				t.Fatal("ListModels succeeded, want error")
			}

			if len(calls.all()) != 1 {
				t.Errorf("calls = %d, want 1", len(calls.all()))
			}
		})
	}
}

func TestRetryRetriesConnectionFailure(t *testing.T) {
	server, _ := newServer(t, func(http.ResponseWriter, *http.Request, int) {})
	url := server.URL
	server.Close()

	start := time.Now()

	_, err := newClient(t, url, WithRetryPolicy(RetryPolicy{MaxRetries: 2, BackoffInitial: 20 * time.Millisecond, BackoffMax: 20 * time.Millisecond})).
		ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels succeeded, want error")
	}

	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("elapsed %v, want two backoffs of 20ms", elapsed)
	}
}

func TestRetryStopsWhenDelayExceedsBudget(t *testing.T) {
	for _, retryAfter := range []string{"60", "1e20"} {
		t.Run(retryAfter, func(t *testing.T) {
			server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Retry-After", retryAfter)
				writeJSON(w, http.StatusTooManyRequests, `{}`)
			})

			policy := RetryPolicy{MaxRetries: 3, Budget: time.Second}
			if _, err := newClient(t, server.URL, WithRetryPolicy(policy)).ListModels(context.Background()); err == nil {
				t.Fatal("ListModels succeeded, want error")
			}

			if len(calls.all()) != 1 {
				t.Errorf("calls = %d, want 1", len(calls.all()))
			}
		})
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		cancel()
		writeJSON(w, http.StatusInternalServerError, `{}`)
	})

	policy := RetryPolicy{MaxRetries: 3, BackoffInitial: time.Hour, BackoffMax: time.Hour}
	if _, err := newClient(t, server.URL, WithRetryPolicy(policy)).ListModels(ctx); err == nil {
		t.Fatal("ListModels succeeded, want error")
	}

	if len(calls.all()) != 1 {
		t.Errorf("calls = %d, want 1", len(calls.all()))
	}
}

func TestSleepReturnsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRetryDelay(t *testing.T) {
	policy := RetryPolicy{BackoffInitial: 500 * time.Millisecond, BackoffMax: 5 * time.Second}
	transient := errors.New("connection reset")

	for retry, want := range map[int]time.Duration{
		1:   500 * time.Millisecond,
		2:   time.Second,
		4:   4 * time.Second,
		5:   5 * time.Second,
		100: 5 * time.Second,
	} {
		if got := policy.delay(retry, transient); got != want {
			t.Errorf("delay(%d) = %v, want %v", retry, got, want)
		}
	}

	if got := (RetryPolicy{BackoffMax: time.Second}).delay(1, transient); got != 0 {
		t.Errorf("delay without initial backoff = %v, want 0", got)
	}

	jittered := RetryPolicy{BackoffInitial: time.Second, BackoffMax: time.Second, BackoffJitter: 0.25}
	for range 100 {
		if got := jittered.delay(1, transient); got < 750*time.Millisecond || got > time.Second {
			t.Fatalf("jittered delay = %v, want within [750ms, 1s]", got)
		}
	}

	retryAfter := &APIError{RetryAfter: 0, retryAfterSet: true}
	if got := policy.delay(3, retryAfter); got != 0 {
		t.Errorf("delay with Retry-After 0 = %v, want 0", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		header map[string]string
		want   time.Duration
		ok     bool
	}{
		{"none", nil, 0, false},
		{"milliseconds", map[string]string{"Retry-After-Ms": "250"}, 250 * time.Millisecond, true},
		{"milliseconds win", map[string]string{"Retry-After-Ms": "5", "Retry-After": "9"}, 5 * time.Millisecond, true},
		{"invalid milliseconds fall back", map[string]string{"Retry-After-Ms": "soon", "Retry-After": "2"}, 2 * time.Second, true},
		{"fractional seconds", map[string]string{"Retry-After": "1.5"}, 1500 * time.Millisecond, true},
		{"negative seconds", map[string]string{"Retry-After": "-1"}, 0, false},
		{"http date", map[string]string{"Retry-After": now.Add(3 * time.Second).Format(http.TimeFormat)}, 3 * time.Second, true},
		{"past date", map[string]string{"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat)}, 0, true},
		{"garbage", map[string]string{"Retry-After": "later"}, 0, false},
		{"huge seconds saturate", map[string]string{"Retry-After": "1e20"}, math.MaxInt64, true},
		{"huge milliseconds saturate", map[string]string{"Retry-After-Ms": "1e300"}, math.MaxInt64, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			for name, value := range tt.header {
				header.Set(name, value)
			}

			got, ok := parseRetryAfter(header, now)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseRetryAfter = %v, %v, want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
