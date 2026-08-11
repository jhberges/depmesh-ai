// Package upstream is the client half of the self-hosted deployment model:
// instead of talking to npm/PyPI/Maven itself, a developer's CLI or MCP
// server asks one central `depmesh-ai api` instance inside the network
// boundary, which holds the policy, the audit log, and the telemetry key.
//
// An unreachable gate is never a reason to fall back to direct registry
// access — a machine that is not permitted to reach npm must not start doing
// so because the gate is down. Failures surface as sources.UnavailableError
// so every caller already treats them as "unknown", never as approval.
package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/sources"
)

// Headers carrying the caller's identity to the gate, so its audit record
// names the developer who asked rather than the server that answered.
const (
	SurfaceHeader  = "X-Depmesh-Surface"
	ActorHeader    = "X-Depmesh-Actor"
	HostnameHeader = "X-Depmesh-Hostname"
)

// Registry lookups happen on the far side, so allow for a slower answer than
// a single registry call would need.
var client = &http.Client{Timeout: 30 * time.Second}

// Request is one delegated vet.
type Request struct {
	Base      string
	Surface   string
	Actor     string
	Hostname  string
	Ecosystem string
	Package   string
	// Version is the exact release to ask about, empty for a package-level
	// question. It travels as a query parameter rather than a path segment
	// because the gate's route ends in {package...}, which already swallows
	// the slashes and colons a coordinate is made of.
	Version string
	Enrich  bool
}

// Normalize validates a gate address and trims its trailing slash. It is
// deliberately strict: a bare host with no scheme is more likely a typo than
// an intent, and silently guessing http:// would downgrade a link that was
// meant to be encrypted.
func Normalize(base string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid upstream %q: %w", base, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid upstream %q: need an http:// or https:// address", base)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream %q: no host", base)
	}
	return trimmed, nil
}

// Vet asks the gate about one package and returns the raw Outcome JSON.
// 200 (allowed) and 409 (blocked) both carry an outcome; every other status
// is an error, because a gate that cannot answer must not be read as a yes.
func Vet(request Request) ([]byte, error) {
	// The package segment keeps its slashes and colons: the gate's route ends
	// in {package...}, which matches npm scopes and maven coordinates whole.
	endpoint := request.Base + "/v1/vet/" + request.Ecosystem + "/" + request.Package
	query := url.Values{}
	if !request.Enrich {
		query.Set("enrich", "false")
	}
	if request.Version != "" {
		query.Set("version", request.Version)
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	httpRequest, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &sources.UnavailableError{URL: endpoint, Err: err}
	}
	httpRequest.Header.Set("Accept", "application/json")
	setIfPresent(httpRequest, SurfaceHeader, request.Surface)
	setIfPresent(httpRequest, ActorHeader, request.Actor)
	setIfPresent(httpRequest, HostnameHeader, request.Hostname)

	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, &sources.UnavailableError{URL: endpoint, Err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, &sources.UnavailableError{URL: endpoint, Err: err}
	}

	switch response.StatusCode {
	case http.StatusOK, http.StatusConflict:
		return body, nil
	case http.StatusBadGateway:
		// The gate answered; it just couldn't reach the registry. Same
		// meaning as a local registry failure, so same error type.
		return nil, &sources.UnavailableError{
			URL: endpoint,
			Err: fmt.Errorf("gate reports: %s", detail(body, "registry unreachable")),
		}
	case http.StatusBadRequest:
		return nil, fmt.Errorf("gate rejected the request: %s", detail(body, "bad request"))
	default:
		return nil, fmt.Errorf("gate returned HTTP %d: %s", response.StatusCode, detail(body, "no detail"))
	}
}

func setIfPresent(r *http.Request, header, value string) {
	if value != "" {
		r.Header.Set(header, value)
	}
}

// detail pulls the message out of the gate's {"error": "..."} body.
func detail(body []byte, fallback string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return fallback
}
