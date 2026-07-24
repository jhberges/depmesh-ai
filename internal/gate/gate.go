// Package gate composes the full decision pipeline — vet, policy, audit,
// telemetry — so the CLI, the MCP server, and the HTTP API all behave
// identically.
package gate

import (
	"encoding/json"
	"time"

	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/telemetry"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

// Version is the build version. It defaults to a dev value and is stamped at
// release time via -ldflags "-X .../internal/gate.Version=vX.Y.Z".
var Version = "0.2.0-dev"

type Gate struct {
	Policy    *policy.Policy
	AuditLog  string
	Telemetry string
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
// wins over the policy file's audit_log.
func New(policyPath, auditOverride string) (*Gate, error) {
	p, err := policy.Load(policyPath)
	if err != nil {
		return nil, err
	}
	g := &Gate{Policy: p}
	var policyTelemetry string
	if p != nil {
		g.AuditLog = p.AuditLog
		policyTelemetry = p.TelemetryURL
	}
	if auditOverride != "" {
		g.AuditLog = auditOverride
	}
	g.Telemetry = telemetry.Endpoint(policyTelemetry)
	return g, nil
}

// Vet runs the pipeline for one package. Audit failures surface as the
// returned error alongside a valid outcome — compliance-critical callers can
// fail closed; others can log and continue.
func (g *Gate) Vet(surface, ecosystem, name string, enrich bool) (*Outcome, error) {
	verdict, err := vet.Vet(ecosystem, name, enrich)
	if err != nil {
		return nil, err
	}
	outcome := &Outcome{Verdict: verdict}
	if g.Policy != nil {
		result := g.Policy.Apply(verdict, time.Now().UTC())
		outcome.Policy = &result
	}
	telemetry.ReportNonexistent(g.Telemetry, Version, verdict)
	if err := audit.Write(g.AuditLog, surface, Version, verdict, outcome.Policy); err != nil {
		return outcome, err
	}
	return outcome, nil
}
