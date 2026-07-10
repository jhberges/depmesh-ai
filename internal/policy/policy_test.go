package policy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/vet"
)

var today = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

func verdict(advice vet.Advice, score int, license string) *vet.Verdict {
	return &vet.Verdict{
		Ecosystem: "npm",
		Package:   "some-lib",
		Advice:    advice,
		Score:     score,
		License:   license,
	}
}

func TestNoViolationsOnHealthyPackage(t *testing.T) {
	p := &Policy{MinScore: 60, Licenses: Licenses{Allow: []string{"MIT", "Apache"}}}
	r := p.Apply(verdict(vet.Adopt, 90, "MIT"), today)
	if !r.Allowed || len(r.Violations) != 0 {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestRejectVerdictBlocks(t *testing.T) {
	p := &Policy{}
	if r := p.Apply(verdict(vet.Reject, 10, "MIT"), today); r.Allowed {
		t.Fatal("REJECT verdict passed an empty policy")
	}
}

func TestFailOnCautionBlocksCaution(t *testing.T) {
	p := &Policy{FailOn: "caution"}
	if r := p.Apply(verdict(vet.Caution, 60, "MIT"), today); r.Allowed {
		t.Fatal("CAUTION passed fail_on=caution policy")
	}
	p = &Policy{} // default fail_on=reject
	if r := p.Apply(verdict(vet.Caution, 60, "MIT"), today); !r.Allowed {
		t.Fatal("CAUTION blocked by default policy")
	}
}

func TestMinScoreBlocks(t *testing.T) {
	p := &Policy{MinScore: 80}
	if r := p.Apply(verdict(vet.Adopt, 75, "MIT"), today); r.Allowed {
		t.Fatal("score below minimum passed")
	}
}

func TestLicenseDenyBeatsAllow(t *testing.T) {
	p := &Policy{Licenses: Licenses{Allow: []string{"GPL"}, Deny: []string{"GPL"}}}
	if r := p.Apply(verdict(vet.Adopt, 90, "GPL-3.0"), today); r.Allowed {
		t.Fatal("denied license passed")
	}
}

func TestLicenseAllowListBlocksOthers(t *testing.T) {
	p := &Policy{Licenses: Licenses{Allow: []string{"MIT", "Apache", "BSD"}}}
	if r := p.Apply(verdict(vet.Adopt, 90, "WTFPL"), today); r.Allowed {
		t.Fatal("license outside allow list passed")
	}
}

func TestRequireDeclaredLicense(t *testing.T) {
	p := &Policy{Licenses: Licenses{RequireDeclared: true}}
	if r := p.Apply(verdict(vet.Adopt, 90, ""), today); r.Allowed {
		t.Fatal("missing license passed require_declared")
	}
}

func TestEcosystemRestriction(t *testing.T) {
	p := &Policy{Ecosystems: []string{"maven"}}
	if r := p.Apply(verdict(vet.Adopt, 90, "MIT"), today); r.Allowed {
		t.Fatal("npm passed a maven-only policy")
	}
}

func TestExceptionOverridesEverything(t *testing.T) {
	p := &Policy{
		MinScore: 99,
		Exceptions: []Exception{{
			Ecosystem: "npm", Package: "some-lib",
			Reason: "approved by security architecture", Expires: "2027-01-01",
		}},
	}
	r := p.Apply(verdict(vet.Reject, 0, ""), today)
	if !r.Allowed || r.Exception == nil {
		t.Fatalf("valid exception not applied: %+v", r)
	}
}

func TestExpiredExceptionIsIgnored(t *testing.T) {
	p := &Policy{Exceptions: []Exception{{
		Ecosystem: "npm", Package: "some-lib", Reason: "old", Expires: "2020-01-01",
	}}}
	if r := p.Apply(verdict(vet.Reject, 0, ""), today); r.Allowed {
		t.Fatal("expired exception applied")
	}
}

func TestLoadMissingDefaultIsNil(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(dir)
	t.Setenv(EnvVar, "")

	p, err := Load("")
	if err != nil || p != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", p, err)
	}
}

func TestLoadExplicitMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("explicit missing policy file did not error")
	}
}

func TestLoadParsesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	os.WriteFile(path, []byte(`{"min_score": 50, "fail_on": "caution"}`), 0o644)
	p, err := Load(path)
	if err != nil || p == nil || p.MinScore != 50 {
		t.Fatalf("load failed: %v %+v", err, p)
	}
}

func TestLoadRejectsBadFailOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	os.WriteFile(path, []byte(`{"fail_on": "always"}`), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("bad fail_on accepted")
	}
}
