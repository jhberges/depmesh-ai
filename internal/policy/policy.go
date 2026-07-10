// Package policy is the organizational gate on top of the health verdict.
//
// The vet engine answers "is this package healthy?"; policy answers "is this
// package allowed *here*?" — license rules, score floors, and explicit,
// justified, expiring exceptions. It is configured from a JSON file so it can
// be reviewed and version-controlled like any other piece of compliance
// configuration (JSON rather than YAML keeps the binary dependency-free).
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/vet"
)

// DefaultFile is auto-discovered in the working directory when no explicit
// path is given.
const DefaultFile = "depmesh.policy.json"

// EnvVar names a policy file location, overriding auto-discovery. This is how
// the MCP server (which takes no CLI flags from the agent) picks up policy.
const EnvVar = "DEPMESH_POLICY"

type Exception struct {
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	Reason    string `json:"reason"`
	// Expires is an ISO date (YYYY-MM-DD). An expired exception is ignored —
	// exceptions must be re-justified, not immortal.
	Expires string `json:"expires,omitempty"`
}

type Licenses struct {
	// Allow: when non-empty, the package license must contain one of these
	// (case-insensitive substring, e.g. "MIT", "Apache"). Empty = allow any.
	Allow []string `json:"allow,omitempty"`
	// Deny: license must not contain any of these. Checked before Allow.
	Deny []string `json:"deny,omitempty"`
	// RequireDeclared: fail packages with no license at all.
	RequireDeclared bool `json:"require_declared,omitempty"`
}

type Policy struct {
	// MinScore fails any package scoring below it (0 disables).
	MinScore int `json:"min_score,omitempty"`
	// FailOn: "reject" (default) fails only REJECT verdicts; "caution" also
	// fails CAUTION.
	FailOn     string      `json:"fail_on,omitempty"`
	Licenses   Licenses    `json:"licenses,omitempty"`
	Ecosystems []string    `json:"ecosystems,omitempty"` // allowed ecosystems; empty = all
	Exceptions []Exception `json:"exceptions,omitempty"`
	// AuditLog is a path to append JSONL decision records to (see audit pkg).
	AuditLog string `json:"audit_log,omitempty"`
	// TelemetryURL enables opt-in slopsquat telemetry (see telemetry pkg).
	// Absent/empty = telemetry fully disabled.
	TelemetryURL string `json:"telemetry_url,omitempty"`
}

type Result struct {
	Allowed    bool       `json:"allowed"`
	Violations []string   `json:"violations,omitempty"`
	Exception  *Exception `json:"exception,omitempty"`
}

// Load reads a policy file. Load("") tries $DEPMESH_POLICY, then
// ./depmesh.policy.json, and returns (nil, nil) when neither exists — no
// policy configured is a valid state, not an error.
func Load(path string) (*Policy, error) {
	explicit := path != ""
	if path == "" {
		path = os.Getenv(EnvVar)
		explicit = path != ""
	}
	if path == "" {
		path = DefaultFile
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) && !explicit {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	switch strings.ToLower(p.FailOn) {
	case "", "reject", "caution":
	default:
		return nil, fmt.Errorf("policy %s: fail_on must be \"reject\" or \"caution\", got %q", path, p.FailOn)
	}
	return &p, nil
}

// Apply evaluates a verdict against the policy.
func (p *Policy) Apply(v *vet.Verdict, today time.Time) Result {
	if exception := p.findException(v, today); exception != nil {
		return Result{Allowed: true, Exception: exception}
	}

	var violations []string

	if len(p.Ecosystems) > 0 && !containsFold(p.Ecosystems, v.Ecosystem) {
		violations = append(violations,
			fmt.Sprintf("ecosystem %q is not in the allowed set %v", v.Ecosystem, p.Ecosystems))
	}

	failOn := strings.ToLower(p.FailOn)
	if v.Advice == vet.Reject || (failOn == "caution" && v.Advice == vet.Caution) {
		violations = append(violations, fmt.Sprintf("verdict is %s", v.Advice))
	}
	if p.MinScore > 0 && v.Score < p.MinScore {
		violations = append(violations,
			fmt.Sprintf("score %d is below the policy minimum %d", v.Score, p.MinScore))
	}

	violations = append(violations, p.licenseViolations(v.License)...)

	return Result{Allowed: len(violations) == 0, Violations: violations}
}

func (p *Policy) licenseViolations(license string) []string {
	rules := p.Licenses
	if license == "" {
		if rules.RequireDeclared {
			return []string{"no license declared and policy requires one"}
		}
		return nil
	}
	upper := strings.ToUpper(license)
	for _, denied := range rules.Deny {
		if strings.Contains(upper, strings.ToUpper(denied)) {
			return []string{fmt.Sprintf("license %q matches denied pattern %q", license, denied)}
		}
	}
	if len(rules.Allow) > 0 {
		for _, allowed := range rules.Allow {
			if strings.Contains(upper, strings.ToUpper(allowed)) {
				return nil
			}
		}
		return []string{fmt.Sprintf("license %q matches none of the allowed patterns %v", license, rules.Allow)}
	}
	return nil
}

func (p *Policy) findException(v *vet.Verdict, today time.Time) *Exception {
	for i := range p.Exceptions {
		e := &p.Exceptions[i]
		if !strings.EqualFold(e.Ecosystem, v.Ecosystem) || !strings.EqualFold(e.Package, v.Package) {
			continue
		}
		if e.Expires != "" {
			expiry, err := time.Parse("2006-01-02", e.Expires)
			if err != nil || !today.Before(expiry.Add(24*time.Hour)) {
				continue // malformed or expired: fail closed
			}
		}
		return e
	}
	return nil
}

func containsFold(haystack []string, needle string) bool {
	for _, item := range haystack {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}
