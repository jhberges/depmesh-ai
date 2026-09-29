package sources

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every test in this package that exercises an abnormal registry answer now
// pays the retry budget, and the real backoff would put minutes on the suite
// for waits none of those tests are about. The curve itself stays covered, by
// TestRetryAfterIsHonoured and by the attempt counts below.
func TestMain(m *testing.M) {
	baseBackoff, maxBackoff = time.Millisecond, 2*time.Millisecond
	os.Exit(m.Run())
}

// fastRetries documents that a test depends on the shortened backoff. TestMain
// already applies it; this keeps the dependency visible at the test itself.
func fastRetries(t *testing.T) {
	t.Helper()
	base, max := baseBackoff, maxBackoff
	baseBackoff, maxBackoff = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { baseBackoff, maxBackoff = base, max })
}

// A rate limit is the failure this retry exists for: registries answer 429 to
// exactly the bulk traffic a lockfile-wide vet produces.
func TestGetRetriesRateLimit(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	body, err := getText(server.URL)
	if err != nil || body != "ok" {
		t.Fatalf("rate limit not retried through: body=%q err=%v", body, err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("made %d attempts, wanted 3", got)
	}
}

func TestGetRetriesServerError(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	if _, err := getText(server.URL); err != nil {
		t.Fatalf("502 not retried through: %v", err)
	}
}

// The whole point of the retry is that it cannot manufacture an absence. A
// registry that never recovers is unavailable, and a caller that mistook that
// for ErrNotFound would report a real package as hallucinated.
func TestExhaustedRetriesAreUnavailableNotAbsent(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := getText(server.URL)
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a rate limit was reported as a missing resource")
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("wanted UnavailableError, got %T: %v", err, err)
	}
	if got := calls.Load(); int(got) != maxAttempts {
		t.Errorf("made %d attempts, wanted %d", got, maxAttempts)
	}
}

// 404 is the authoritative answer this tool turns on. Retrying it would slow
// every genuine slopsquat detection down by the whole retry budget.
func TestNotFoundIsNotRetried(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	if _, err := getText(server.URL); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wanted ErrNotFound, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d attempts for a 404, wanted 1", got)
	}
}

// A 4xx that is not 404 and not 429 is the registry telling us something is
// wrong with the request; repeating it verbatim will not help.
func TestOtherClientErrorIsNotRetried(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := getText(server.URL)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("wanted UnavailableError, got %T", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d attempts for a 401, wanted 1", got)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	start := time.Now()
	if _, err := getText(server.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The header says a second; the jittered backoff would have been ~1ms.
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("waited %v, ignoring the registry's Retry-After of 1s", waited)
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	huge := http.Header{}
	huge.Set("Retry-After", strconv.Itoa(int((2 * time.Hour).Seconds())))
	got, ok := retryAfter(huge)
	if !ok || got != maxRetryAfter {
		t.Errorf("Retry-After of two hours became %v (ok=%v), wanted the %v cap", got, ok, maxRetryAfter)
	}
	if _, ok := retryAfter(http.Header{}); ok {
		t.Error("absent Retry-After reported as present")
	}
	if _, ok := retryAfter(http.Header{"Retry-After": []string{"soon"}}); ok {
		t.Error("unparseable Retry-After reported as present")
	}
}

// The limiter is what keeps a fan-out across parallel workers from becoming
// the burst that earns the 429 in the first place.
func TestConcurrencyIsBounded(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := atomic.AddInt32(&live, 1)
		mu.Lock()
		if now > peak {
			peak = now
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&live, -1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	var wg sync.WaitGroup
	for range maxConcurrency * 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = getText(server.URL)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > maxConcurrency {
		t.Errorf("%d requests were in flight at once, above the limit of %d", peak, maxConcurrency)
	}
}
