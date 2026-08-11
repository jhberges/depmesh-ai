package policy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/metrics"
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

// versionVerdict is a pin some distance behind the latest release, with the
// cadence numbers the version rules read.
func versionVerdict(version string, releasesSince int, intervalsBehind float64) *vet.Verdict {
	v := verdict(vet.Adopt, 90, "MIT")
	v.Version = version
	v.LatestVersion = "3.0.0"
	v.VersionPace = metrics.VersionPace{
		Located:         true,
		ReleasesSince:   releasesSince,
		Normalized:      intervalsBehind > 0,
		IntervalsBehind: intervalsBehind,
	}
	return v
}

// Every version rule is inert on a package-level question. A policy file
// written for pins must not start blocking the callers that ask about
// packages.
func TestVersionRulesAreInertWithoutAVersion(t *testing.T) {
	no := false
	p := &Policy{
		MaxReleasesBehind:  1,
		MaxIntervalsBehind: 1,
		AllowPrerelease:    &no,
		RequireLatest:      true,
	}
	if r := p.Apply(verdict(vet.Adopt, 90, "MIT"), today); !r.Allowed {
		t.Fatalf("a package-level verdict was judged by version rules: %+v", r)
	}
}

func TestMaxReleasesBehindBlocks(t *testing.T) {
	p := &Policy{MaxReleasesBehind: 20}
	if r := p.Apply(versionVerdict("1.0.0", 21, 0), today); r.Allowed {
		t.Error("21 releases behind passed a maximum of 20")
	}
	if r := p.Apply(versionVerdict("1.0.0", 20, 0), today); !r.Allowed {
		t.Errorf("exactly at the maximum should pass: %+v", r)
	}
}

func TestMaxIntervalsBehindBlocks(t *testing.T) {
	p := &Policy{MaxIntervalsBehind: 8}
	if r := p.Apply(versionVerdict("1.0.0", 3, 9.5), today); r.Allowed {
		t.Error("9.5 intervals behind passed a maximum of 8")
	}
	if r := p.Apply(versionVerdict("1.0.0", 3, 4), today); !r.Allowed {
		t.Errorf("4 intervals behind should pass: %+v", r)
	}
}

// An unmeasurable cadence must not read as zero intervals behind, which would
// wave through every pin in a project whose history could not be dated.
func TestMaxIntervalsBehindIsInertWithoutACadence(t *testing.T) {
	p := &Policy{MaxIntervalsBehind: 8}
	v := versionVerdict("1.0.0", 3, 0)
	v.VersionPace.Normalized = false
	if r := p.Apply(v, today); !r.Allowed {
		t.Errorf("an unmeasurable cadence was treated as a violation: %+v", r)
	}
}

func TestAllowPrereleaseFalseBlocksAPrereleasePin(t *testing.T) {
	no, yes := false, true
	if r := (&Policy{AllowPrerelease: &no}).Apply(versionVerdict("2.0.0-rc1", 0, 0), today); r.Allowed {
		t.Error("a prerelease pin passed a policy that forbids one")
	}
	if r := (&Policy{AllowPrerelease: &yes}).Apply(versionVerdict("2.0.0-rc1", 0, 0), today); !r.Allowed {
		t.Errorf("allow_prerelease: true should permit it: %+v", r)
	}
	// Absent leaves prereleases to the score, which already penalizes them.
	if r := (&Policy{}).Apply(versionVerdict("2.0.0-rc1", 0, 0), today); !r.Allowed {
		t.Errorf("an absent setting should not block: %+v", r)
	}
}

func TestRequireLatestBlocksAnOlderPin(t *testing.T) {
	p := &Policy{RequireLatest: true}
	if r := p.Apply(versionVerdict("1.0.0", 5, 0), today); r.Allowed {
		t.Error("an older pin passed require_latest")
	}
	if r := p.Apply(versionVerdict("3.0.0", 0, 0), today); !r.Allowed {
		t.Errorf("the latest release should pass require_latest: %+v", r)
	}
}

// The narrow form is the one people write: "we reviewed 2.14.1 and accepted
// it" must not silently cover 2.14.2, which nobody reviewed.
func TestVersionedExceptionCoversOnlyThatVersion(t *testing.T) {
	p := &Policy{Exceptions: []Exception{
		{Ecosystem: "npm", Package: "some-lib", Version: "1.0.0", Reason: "reviewed"},
	}}
	v := versionVerdict("1.0.0", 5, 0)
	v.Advice = vet.Reject
	if r := p.Apply(v, today); !r.Allowed || r.Exception == nil {
		t.Fatalf("the reviewed version was not excepted: %+v", r)
	}
	other := versionVerdict("1.0.1", 5, 0)
	other.Advice = vet.Reject
	if r := p.Apply(other, today); r.Allowed {
		t.Error("the exception covered a version it did not name")
	}
}

// An exception with no version means what it always meant: the package, every
// version of it. Policy files written before versions existed keep working.
func TestUnversionedExceptionStillCoversAVersionedQuestion(t *testing.T) {
	p := &Policy{Exceptions: []Exception{
		{Ecosystem: "npm", Package: "some-lib", Reason: "internal fork"},
	}}
	v := versionVerdict("1.0.0", 5, 0)
	v.Advice = vet.Reject
	if r := p.Apply(v, today); !r.Allowed || r.Exception == nil {
		t.Fatalf("an unversioned exception stopped covering a version: %+v", r)
	}
}
