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

	"github.com/jhberges/depmesh-ai/internal/bytesize"
	"github.com/jhberges/depmesh-ai/internal/model"
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
	// Version, when set, narrows the exception to one release. Absent means
	// every version, which is what an exception meant before versions existed
	// and so keeps every policy file working unchanged.
	//
	// The narrow form is the more useful one: "we reviewed 2.14.1 and accepted
	// it" is the exception people actually write, and scoping it to that
	// release stops it silently covering the next one, which nobody reviewed.
	Version string `json:"version,omitempty"`
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
	// AuditMaxSize rotates the audit log once a record would carry it past
	// this size, written either as bytes or as "100MB". Absent means the
	// audit package's default; an explicit 0 means never rotate.
	AuditMaxSize *bytesize.Size `json:"audit_max_size,omitempty"`
	// AuditKeep is how many rotated files to retain (absent = default).
	AuditKeep *int `json:"audit_keep,omitempty"`
	// TelemetryURL enables opt-in slopsquat telemetry (see telemetry pkg).
	// Absent/empty = telemetry fully disabled.
	TelemetryURL string `json:"telemetry_url,omitempty"`

	// The rules below judge the requested version and are inert when the
	// caller asked only about a package — a policy file may name them, but
	// nothing can violate a rule about a version nobody asked about.

	// MaxReleasesBehind fails a pin with more than this many releases
	// published after it (0 disables). It counts what the registry published,
	// excluding prereleases and yanked releases, which are not upgrade
	// targets.
	MaxReleasesBehind int `json:"max_releases_behind,omitempty"`
	// MaxIntervalsBehind fails a pin further behind than this many of the
	// project's own average release intervals (0 disables). It is the rule to
	// reach for in a policy that spans ecosystems: twenty releases means
	// something different for a weekly project than a yearly one, and this
	// does not. Inert where the project has no measurable cadence — an
	// unmeasurable pin is not a violated one.
	MaxIntervalsBehind float64 `json:"max_intervals_behind,omitempty"`
	// AllowPrerelease permits pinning a prerelease. Pointer so that an
	// explicit false is distinguishable from an absent field; absent leaves
	// prereleases to the score, which already penalizes them.
	AllowPrerelease *bool `json:"allow_prerelease,omitempty"`
	// RequireLatest fails any pin that is not the latest release. Blunt, and
	// deliberately so: it is the rule for the org that has decided its
	// dependencies track head.
	RequireLatest bool `json:"require_latest,omitempty"`
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
	violations = append(violations, p.versionViolations(v)...)

	return Result{Allowed: len(violations) == 0, Violations: violations}
}

// versionViolations applies the rules that judge the pin rather than the
// package. They are all inert when no version was requested, which is what
// keeps a policy file written for either half working against both.
func (p *Policy) versionViolations(v *vet.Verdict) []string {
	if v.Version == "" {
		return nil
	}
	var violations []string

	if p.RequireLatest && !v.RequestedIsLatest() {
		latest := v.LatestVersion
		if latest == "" {
			latest = "unknown"
		}
		violations = append(violations,
			fmt.Sprintf("version %s is not the latest release (%s) and policy requires it",
				v.Version, latest))
	}
	if p.AllowPrerelease != nil && !*p.AllowPrerelease && model.IsPrerelease(v.Version) {
		violations = append(violations,
			fmt.Sprintf("version %s is a prerelease and policy does not allow one", v.Version))
	}
	if p.MaxReleasesBehind > 0 && v.VersionPace.ReleasesSince > p.MaxReleasesBehind {
		violations = append(violations,
			fmt.Sprintf("%d releases have been published since version %s, above the policy maximum of %d",
				v.VersionPace.ReleasesSince, v.Version, p.MaxReleasesBehind))
	}
	// Normalized is the guard that keeps an unmeasurable cadence from reading
	// as zero intervals behind, which would silently pass every pin in a
	// project whose release history we could not date.
	if p.MaxIntervalsBehind > 0 && v.VersionPace.Normalized &&
		v.VersionPace.IntervalsBehind > p.MaxIntervalsBehind {
		violations = append(violations,
			fmt.Sprintf("version %s is %.1f of this project's release intervals behind, above the policy maximum of %.1f",
				v.Version, v.VersionPace.IntervalsBehind, p.MaxIntervalsBehind))
	}
	return violations
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
		// A versioned exception covers that release and no other. An
		// unversioned one covers the package, which is what it has always
		// meant — including when the question is about a version.
		if e.Version != "" && !strings.EqualFold(e.Version, v.Version) {
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
