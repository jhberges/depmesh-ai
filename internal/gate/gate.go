// Package gate composes the full decision pipeline — vet, policy, audit,
// telemetry — so the CLI, the MCP server, and the HTTP API all behave
// identically.
package gate

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/telemetry"
	"github.com/jhberges/depmesh-ai/internal/upstream"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

// Version is the build version. It defaults to a dev value and is stamped at
// release time via -ldflags "-X .../internal/gate.Version=vX.Y.Z".
var Version = "0.2.0-dev"

type Gate struct {
	Policy    *policy.Policy
	Audit     audit.Log
	Telemetry telemetry.Config

	// Upstream, when set, is the base URL of a central `depmesh-ai api`
	// instance that decides instead of this process. Local policy, audit,
	// and telemetry are then the gate's business, not ours — see Vet.
	Upstream string
}

// Outcome is a verdict plus the policy decision about it. Policy is nil when
// no policy is configured.
type Outcome struct {
	Verdict *vet.Verdict   `json:"verdict"`
	Policy  *policy.Result `json:"policy,omitempty"`
}

// Allowed is the overall gate decision: with a policy, the policy decides;
// without one, only a REJECT verdict blocks.
func (o *Outcome) Allowed() bool {
	if o.Policy != nil {
		return o.Policy.Allowed
	}
	return o.Verdict.Advice != vet.Reject
}

func (o *Outcome) JSON() string {
	out, _ := json.MarshalIndent(o, "", "  ")
	return string(out)
}

// New loads policy (explicit path, $DEPMESH_POLICY, or ./depmesh.policy.json)
// and resolves audit/telemetry configuration. auditOverride, when non-empty,
// wins over the policy file's audit_log. A non-empty upstreamURL switches
// this process into delegating mode; resolving that from configuration is
// the caller's job, so a serving gate can never be talked into chaining to
// itself by an environment variable meant for developer machines.
func New(policyPath, auditOverride, upstreamURL string) (*Gate, error) {
	if upstreamURL != "" {
		normalized, err := upstream.Normalize(upstreamURL)
		if err != nil {
			return nil, err
		}
		upstreamURL = normalized
	}
	p, err := policy.Load(policyPath)
	if err != nil {
		return nil, err
	}
	g := &Gate{
		Policy:   p,
		Upstream: upstreamURL,
		Audit:    audit.Log{MaxSize: audit.DefaultMaxSize, Keep: audit.DefaultKeep},
	}
	var policyTelemetry string
	if p != nil {
		g.Audit.Path = p.AuditLog
		policyTelemetry = p.TelemetryURL
		// Pointers, so that an explicit 0 ("never rotate") is distinguishable
		// from an absent field ("use the default").
		if p.AuditMaxSize != nil {
			g.Audit.MaxSize = *p.AuditMaxSize
		}
		if p.AuditKeep != nil {
			g.Audit.Keep = *p.AuditKeep
		}
	}
	if auditOverride != "" {
		g.Audit.Path = auditOverride
	}
	g.Telemetry = telemetry.Resolve(policyTelemetry)
	return g, nil
}

// Vet runs the pipeline for one package. Audit failures surface as the
// returned error alongside a valid outcome — compliance-critical callers can
// fail closed; others can log and continue.
//
// With Upstream set the whole pipeline runs on the far side instead: the
// gate applies its policy, writes its audit record, and reports its
// telemetry, and we return what it decided. Applying local policy on top
// would judge the same package twice against two files; reporting telemetry
// on both ends would count one hallucination twice.
func (g *Gate) Vet(caller audit.Caller, ecosystem, name, version string, enrich bool) (*Outcome, error) {
	if g.Upstream != "" {
		return g.vetUpstream(caller, ecosystem, name, version, enrich)
	}
	verdict, err := vet.Vet(ecosystem, name, version, enrich)
	if err != nil {
		return nil, err
	}
	outcome := &Outcome{Verdict: verdict}
	if g.Policy != nil {
		result := g.Policy.Apply(verdict, time.Now().UTC())
		outcome.Policy = &result
	}
	telemetry.ReportNonexistent(g.Telemetry, Version, verdict)
	if err := g.Audit.Write(caller, Version, verdict, outcome.Policy); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func (g *Gate) vetUpstream(caller audit.Caller, ecosystem, name, version string, enrich bool) (*Outcome, error) {
	// The wire format has no version field yet, so a delegating client cannot
	// ask a gate about one. Silently dropping it would return a package-level
	// verdict to a caller who asked a version-level question — approval for
	// something that was never examined — so refuse instead.
	if version != "" {
		return nil, fmt.Errorf(
			"version-aware vetting is not available through --upstream yet: the gate at %s would be asked about %s only",
			g.Upstream, name)
	}
	caller = caller.Resolve()
	body, err := upstream.Vet(upstream.Request{
		Base:      g.Upstream,
		Surface:   caller.Surface,
		Actor:     caller.Actor,
		Hostname:  caller.Hostname,
		Ecosystem: ecosystem,
		Package:   name,
		Enrich:    enrich,
	})
	if err != nil {
		return nil, err
	}
	var outcome Outcome
	if err := json.Unmarshal(body, &outcome); err != nil {
		return nil, &sources.UnavailableError{URL: g.Upstream, Err: fmt.Errorf("invalid JSON: %w", err)}
	}
	if outcome.Verdict == nil {
		return nil, &sources.UnavailableError{URL: g.Upstream, Err: fmt.Errorf("response carried no verdict")}
	}
	return &outcome, nil
}
