// Package sources implements the source layer: one registry-direct module
// per ecosystem, plus optional enrichment (deps.dev). Registry modules are
// authoritative for existence; enrichment only adds signals and degrades
// gracefully when unreachable.
package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const userAgent = "depmesh-ai/0.1 (+https://github.com/jhberges/depmesh-ai)"

// ErrNotFound means the source authoritatively reported the resource absent.
var ErrNotFound = errors.New("not found")

// UnavailableError means the source could not be reached or answered
// abnormally — the caller must treat the result as unknown, never as absent.
type UnavailableError struct {
	URL string
	Err error
}

func (e *UnavailableError) Error() string { return fmt.Sprintf("%s unavailable: %v", e.URL, e.Err) }
func (e *UnavailableError) Unwrap() error { return e.Err }

var client = &http.Client{Timeout: 20 * time.Second}

// Registries rate-limit, and a bulk caller — an agent vetting a whole
// lockfile, fanned out across parallel workers — is exactly the traffic shape
// that trips them. Maven Central answers 429 at surprisingly low rates.
//
// Two mechanisms, because they solve different halves. The limiter caps how
// many requests are in flight from this process at once, which keeps a fan-out
// from becoming a burst. The retry handles the 429 that arrives anyway, since
// the limit is the registry's to set and we cannot know it.
//
// Neither can turn an unavailable registry into an absent package: retries
// exhaust into UnavailableError, and 404 is never retried. A rate limit must
// not read as a missing package, which is the failure that would turn a busy
// afternoon into a slopsquatting false alarm.
const (
	maxRetryAfter  = 30 * time.Second
	maxConcurrency = 8
)

// var rather than const so tests can exercise an exhausted retry budget
// without spending the real backoff waiting for it.
var (
	maxAttempts = 4
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 8 * time.Second
)

// inFlight is the process-wide concurrency limit. Buffered channel rather than
// x/sync/semaphore to keep the binary dependency-free, as elsewhere.
var inFlight = make(chan struct{}, maxConcurrency)

// retryable statuses: the rate limit, and the server-side faults that a
// registry recovers from on its own. Everything else is answered once.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// backoff is exponential with full jitter. Jitter matters more than the curve
// here: without it, parallel workers throttled by the same 429 retry in
// lockstep and reproduce the burst that caused it.
func backoff(attempt int) time.Duration {
	d := baseBackoff << attempt
	if d > maxBackoff {
		d = maxBackoff
	}
	return time.Duration(rand.Int63n(int64(d)) + int64(d)/2)
}

// retryAfter reads the header the registry sets to tell us how long to wait.
// Honouring it is the difference between backing off and guessing. Capped, so
// a hostile or broken value cannot park a vet for an hour; the delta-seconds
// form is the one registries send, and an HTTP-date is read too.
func retryAfter(h http.Header) (time.Duration, bool) {
	value := h.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return min(time.Duration(seconds)*time.Second, maxRetryAfter), true
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return min(d, maxRetryAfter), true
		}
		return 0, true
	}
	return 0, false
}

func get(url, accept string) ([]byte, error) {
	var last *retryableError
	for attempt := range maxAttempts {
		if last != nil {
			time.Sleep(last.delay(attempt - 1))
		}
		body, err := getOnce(url, accept)
		if err == nil {
			return body, nil
		}
		// Only a retryable failure gets another attempt. ErrNotFound never
		// does: a registry saying the package is absent is the authoritative
		// answer this whole tool turns on, and asking again cannot improve it.
		var retry *retryableError
		if !errors.As(err, &retry) {
			return nil, err
		}
		last = retry
	}
	// Out of attempts. This is unavailable, never absent.
	return nil, &UnavailableError{url, last.Err}
}

// retryableError wraps a failure worth another attempt, carrying the delay
// before it. It never escapes get: callers see UnavailableError or ErrNotFound,
// so nothing downstream has to know retrying happened.
type retryableError struct {
	Err error
	// after is the registry's own Retry-After, when it sent one.
	after    time.Duration
	hasAfter bool
}

func (e *retryableError) Error() string { return e.Err.Error() }
func (e *retryableError) Unwrap() error { return e.Err }

func (e *retryableError) delay(attempt int) time.Duration {
	if e.hasAfter {
		return e.after
	}
	return backoff(attempt)
}

func getOnce(url, accept string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, &UnavailableError{url, err}
	}
	request.Header.Set("User-Agent", userAgent)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if auth, ok := credentialsFor(url); ok {
		auth(request)
	}

	inFlight <- struct{}{}
	defer func() { <-inFlight }()

	response, err := client.Do(request)
	if err != nil {
		// A transport error is as likely to be a blip as a wall, and the
		// retry budget is small enough to spend on finding out.
		return nil, &retryableError{Err: err}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", url, ErrNotFound)
	case retryable(response.StatusCode):
		after, ok := retryAfter(response.Header)
		return nil, &retryableError{
			Err:      fmt.Errorf("HTTP %d", response.StatusCode),
			after:    after,
			hasAfter: ok,
		}
	case response.StatusCode != http.StatusOK:
		return nil, &UnavailableError{url, fmt.Errorf("HTTP %d", response.StatusCode)}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, &UnavailableError{url, err}
	}
	return body, nil
}

func getJSON(url string, into any) error {
	body, err := get(url, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return &UnavailableError{url, fmt.Errorf("invalid JSON: %w", err)}
	}
	return nil
}

func getText(url string) (string, error) {
	body, err := get(url, "")
	return string(body), err
}
