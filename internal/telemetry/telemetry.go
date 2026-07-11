// Package telemetry implements the opt-in slopsquat sensor.
//
// When a vet call REJECTs a package because it does not exist on its
// registry, that name is — with high probability — something an AI assistant
// hallucinated. Aggregated across installations, those names are a live feed
// of slopsquatting targets *before* attackers register them.
//
// Privacy by design (GDPR-relevant, and deliberately boring):
//   - OFF by default; enabled only by an explicit telemetry_url in the
//     policy file or DEPMESH_TELEMETRY_URL in the environment.
//   - Payload is only: ecosystem, package name, timestamp, tool version.
//     No usernames, hostnames, IP-derived data, repository names, or any
//     other context is collected.
//   - Fire-and-forget with a short timeout; a telemetry failure never
//     affects the vet result.
package telemetry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/jhberges/depmesh-ai/internal/vet"
)

const EnvVar = "DEPMESH_TELEMETRY_URL"

// TokenEnvVar carries the per-tenant ingest key issued by depmesh-cloud,
// sent as an Authorization bearer header. Kept in the environment rather
// than the (version-controlled) policy file so the secret stays out of git.
const TokenEnvVar = "DEPMESH_TELEMETRY_TOKEN"

var client = &http.Client{Timeout: 3 * time.Second}

type report struct {
	Kind        string `json:"kind"` // always "nonexistent_package"
	Ecosystem   string `json:"ecosystem"`
	Package     string `json:"package"`
	Time        string `json:"time"`
	ToolVersion string `json:"tool_version"`
}

// Endpoint resolves the configured telemetry URL ("" = disabled).
func Endpoint(policyURL string) string {
	if policyURL != "" {
		return policyURL
	}
	return os.Getenv(EnvVar)
}

// ReportNonexistent submits a hallucinated-name observation if (and only if)
// an endpoint is configured and the verdict is a non-existence REJECT.
func ReportNonexistent(endpoint, version string, v *vet.Verdict) {
	if endpoint == "" || v.Advice != vet.Reject || v.Facts == nil ||
		v.Facts.Exists == nil || *v.Facts.Exists {
		return
	}
	payload, err := json.Marshal(report{
		Kind:        "nonexistent_package",
		Ecosystem:   v.Ecosystem,
		Package:     v.Package,
		Time:        time.Now().UTC().Format(time.RFC3339),
		ToolVersion: version,
	})
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token := os.Getenv(TokenEnvVar); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	response.Body.Close()
}
