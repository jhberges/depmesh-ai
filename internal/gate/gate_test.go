// Package gate_test is external so it can import internal/api — the API
// serves a gate, so an in-package test could not.
package gate_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/api"
	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/upstream"
)

// blockedOutcome is what a gate returns for a package its policy refuses.
const blockedOutcome = `{
  "verdict": {"ecosystem":"npm","package":"@types/node","advice":"ADOPT","score":90},
  "policy": {"allowed":false,"violations":["license GPL-3.0 is denied"]}
}`

type seen struct {
	mu       sync.Mutex
	path     string
	surface  string
	actor    string
	hostname string
}

// registryFacing stands in for the one instance that really talks to npm.
// Nothing in this test reaches the network.
func registryFacing(t *testing.T) (string, *seen) {
	t.Helper()
	record := &seen{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record.mu.Lock()
		record.path = r.URL.Path
		record.surface = r.Header.Get(upstream.SurfaceHeader)
		record.actor = r.Header.Get(upstream.ActorHeader)
		record.hostname = r.Header.Get(upstream.HostnameHeader)
		record.mu.Unlock()
		w.WriteHeader(http.StatusConflict) // policy blocked it
		_, _ = w.Write([]byte(blockedOutcome))
	}))
	t.Cleanup(server.Close)
	return server.URL, record
}

// The full developer-machine path: an MCP server delegating to a gate, which
// is itself delegating onward. Two hops proves identity and the decision both
// survive the middle, which one hop cannot.
func TestDelegatedDecisionAndIdentitySurviveTheHop(t *testing.T) {
	origin, record := registryFacing(t)
	middle := httptest.NewServer(api.Handler(&gate.Gate{Upstream: origin}))
	defer middle.Close()

	developer := &gate.Gate{Upstream: middle.URL}
	outcome, err := developer.Vet(
		audit.Caller{Surface: "mcp", Actor: "alice", Hostname: "laptop"},
		"npm", "@types/node", "", true)
	if err != nil {
		t.Fatalf("vet: %v", err)
	}

	if outcome.Verdict.Package != "@types/node" {
		t.Fatalf("package = %q", outcome.Verdict.Package)
	}
	// A blocked package must arrive as a decision, not as an error, and must
	// still read as blocked after crossing two processes.
	if outcome.Policy == nil || outcome.Policy.Allowed || outcome.Allowed() {
		t.Fatalf("blocked decision did not survive: %+v", outcome.Policy)
	}

	record.mu.Lock()
	defer record.mu.Unlock()
	if record.path != "/v1/vet/npm/@types/node" {
		t.Fatalf("path = %q", record.path)
	}
	// The audit record at the far end must name the developer's surface and
	// identity, not the middle server's.
	if record.surface != "mcp" || record.actor != "alice" || record.hostname != "laptop" {
		t.Fatalf("identity lost: surface=%q actor=%q host=%q",
			record.surface, record.actor, record.hostname)
	}
}

// A local policy file must not be applied on top of the gate's answer: the
// package would otherwise be judged twice, against two different files.
func TestLocalPolicyIsNotAppliedWhenDelegating(t *testing.T) {
	origin, _ := registryFacing(t)

	developer := &gate.Gate{
		Upstream: origin,
		// Would block everything if it were consulted.
		Policy: &policy.Policy{MinScore: 100, Licenses: policy.Licenses{RequireDeclared: true}},
	}
	outcome, err := developer.Vet(audit.Local("cli"), "npm", "@types/node", "", true)
	if err != nil {
		t.Fatalf("vet: %v", err)
	}
	if want := "license GPL-3.0 is denied"; len(outcome.Policy.Violations) != 1 ||
		outcome.Policy.Violations[0] != want {
		t.Fatalf("violations came from the local policy, not the gate: %+v", outcome.Policy.Violations)
	}
}

// A gate that answers with something other than an outcome is a failure, not
// an empty approval.
func TestGarbageResponseIsAnError(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"policy":{"allowed":true}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		developer := &gate.Gate{Upstream: server.URL}
		outcome, err := developer.Vet(audit.Local("cli"), "npm", "left-pad", "", true)
		if err == nil {
			t.Fatalf("body %q returned outcome %+v, want error", body, outcome)
		}
		server.Close()
	}
}

// The wire format carries no version yet. Answering a version-level question
// with a package-level verdict would hand back approval for something that was
// never examined, so a delegating gate refuses rather than drops the version.
func TestDelegatingGateRefusesAVersionItCannotForward(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	developer := &gate.Gate{Upstream: server.URL}
	if _, err := developer.Vet(audit.Local("cli"), "npm", "express", "4.18.2", true); err == nil {
		t.Fatal("a version was silently dropped on the way to the gate")
	}
	if asked {
		t.Fatal("the gate was asked a question that omitted the version")
	}
}

func TestNewRejectsBadUpstream(t *testing.T) {
	if _, err := gate.New("", "", "depmesh.lan:8385"); err == nil {
		t.Fatal("a scheme-less upstream should be rejected, not guessed at")
	}
}
