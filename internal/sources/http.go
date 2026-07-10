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
	"net/http"
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

func get(url, accept string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, &UnavailableError{url, err}
	}
	request.Header.Set("User-Agent", userAgent)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, &UnavailableError{url, err}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", url, ErrNotFound)
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
