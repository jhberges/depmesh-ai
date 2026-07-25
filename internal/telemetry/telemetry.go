// Package telemetry implements the opt-in slopsquat sensor.
//
// When a vet call REJECTs a package because it does not exist on its
// registry, that name is — with high probability — something an AI assistant
// hallucinated. Aggregated across installations, those names are a live feed
// of slopsquatting targets *before* attackers register them.
//
// Privacy by design (GDPR-relevant, and deliberately boring):
//   - OFF by default; enabled only by an explicit telemetry_url in the
//     policy file, DEPMESH_TELEMETRY_URL in the environment, or a consent
//     file the installer writes *after* the user says yes. There is no
//     implicit fallback to the hosted endpoint.
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
	"path/filepath"
	"time"

	"github.com/jhberges/depmesh-ai/internal/vet"
)

const EnvVar = "DEPMESH_TELEMETRY_URL"

// TokenEnvVar carries the per-tenant ingest key issued by depmesh-cloud,
// sent as an Authorization bearer header. Kept in the environment rather
// than the (version-controlled) policy file so the secret stays out of git.
const TokenEnvVar = "DEPMESH_TELEMETRY_TOKEN"

// DefaultEndpoint is the hosted receiver the installer offers as the default
// answer once the user has agreed to share. It is deliberately *not* a
// fallback in Resolve: nothing is ever sent without explicit configuration.
const DefaultEndpoint = "https://depmesh.com/v1/telemetry"

var client = &http.Client{Timeout: 3 * time.Second}

// Config is the resolved telemetry destination. A zero Config is disabled,
// which is what every unconfigured installation gets.
type Config struct {
	// URL is the receiver endpoint; empty means telemetry is off.
	URL string `json:"url"`
	// Token is the per-tenant ingest key for URL, sent as a bearer header.
	Token string `json:"token,omitempty"`
}

func (c Config) Enabled() bool { return c.URL != "" }

type report struct {
	Kind        string `json:"kind"` // always "nonexistent_package"
	Ecosystem   string `json:"ecosystem"`
	Package     string `json:"package"`
	Time        string `json:"time"`
	ToolVersion string `json:"tool_version"`
}

// ConfigPath is where install.sh records the user's answer to the consent
// prompt. $XDG_CONFIG_HOME is honoured; otherwise ~/.config — the same path
// on Linux and macOS, so the shell installer and the binary always agree
// (os.UserConfigDir would diverge on macOS). Empty when there is no home
// directory to speak of.
func ConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "depmesh", "telemetry.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "depmesh", "telemetry.json")
}

// Resolve determines where telemetry goes, most specific source first:
//
//  1. telemetry_url from the policy file — a repo/org decision, reviewed and
//     version-controlled like the rest of the policy;
//  2. $DEPMESH_TELEMETRY_URL — per-shell or per-CI override;
//  3. the consent file written by the installer — the individual developer's
//     standing answer, which also reaches MCP servers started by an agent
//     with no environment of their own.
//
// An empty result means disabled, and that stays the default: none of these
// sources exists unless somebody created it.
func Resolve(policyURL string) Config {
	stored := load(ConfigPath())

	config := Config{URL: policyURL}
	if config.URL == "" {
		config.URL = os.Getenv(EnvVar)
	}
	if config.URL == "" {
		config.URL = stored.URL
	}

	// The stored key belongs to the stored endpoint. If policy or environment
	// redirected us somewhere else, that key must not travel along — leaking a
	// tenant's depmesh.com ingest key to an unrelated host is exactly the kind
	// of accident a telemetry feature must not have.
	switch {
	case os.Getenv(TokenEnvVar) != "":
		config.Token = os.Getenv(TokenEnvVar)
	case config.URL != "" && config.URL == stored.URL:
		config.Token = stored.Token
	}
	return config
}

// load reads the consent file. Any problem — absent, unreadable, malformed —
// means "no consent recorded", never a hard failure: telemetry is the least
// important thing the tool does.
func load(path string) Config {
	if path == "" {
		return Config{}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	var config Config
	if err := json.Unmarshal(body, &config); err != nil {
		return Config{}
	}
	return config
}

// ReportNonexistent submits a hallucinated-name observation if (and only if)
// an endpoint is configured and the verdict is a non-existence REJECT.
func ReportNonexistent(config Config, version string, v *vet.Verdict) {
	if !config.Enabled() || v.Advice != vet.Reject || v.Facts == nil ||
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
	request, err := http.NewRequest(http.MethodPost, config.URL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	response.Body.Close()
}
