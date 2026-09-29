package policy

import (
	"os"
	"path/filepath"
	"strings"
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

// require_declared still fails closed on a license we could not read — not
// having managed to check is not evidence of compliance. The message has to
// separate the two, though, or it sends somebody hunting for a missing
// declaration that is really a rate-limited fetch.
func TestUnreadableLicenseFailsRequireDeclaredWithItsOwnReason(t *testing.T) {
	p := &Policy{Licenses: Licenses{RequireDeclared: true}}

	v := verdict(vet.Adopt, 90, "")
	v.LicenseUnknown = true
	r := p.Apply(v, today)
	if r.Allowed {
		t.Fatal("an unreadable license passed a policy requiring a declared one")
	}
	if !strings.Contains(r.Violations[0], "could not be read") {
		t.Errorf("violation %q does not say the license was unreadable", r.Violations[0])
	}

	r = p.Apply(verdict(vet.Adopt, 90, ""), today)
	if r.Allowed || !strings.Contains(r.Violations[0], "no license declared") {
		t.Errorf("an undeclared license lost its own message: %+v", r.Violations)
	}
}

// The example policy file is documentation people copy. It has to keep
// parsing, registries and all.
func TestExamplePolicyFileLoads(t *testing.T) {
	p, err := Load("../../docs/example.policy.json")
	if err != nil {
		t.Fatalf("the documented example does not load: %v", err)
	}
	if len(p.Registries.Maven) == 0 {
		t.Error("the example no longer shows how to configure a repository")
	}
	var patterns int
	for i := range p.Exceptions {
		if p.Exceptions[i].wildcard() {
			patterns++
		}
	}
	if patterns == 0 {
		t.Error("the example no longer shows a pattern exception")
	}
	var internal, public int
	for _, repo := range p.Registries.Maven {
		if len(repo.Namespaces) > 0 {
			internal++
		} else {
			public++
		}
	}
	if internal == 0 || public == 0 {
		t.Errorf("the example should show both forms; got %d namespaced, %d public", internal, public)
	}
}

func mavenVerdict(pkg, version string) *vet.Verdict {
	v := verdict(vet.Reject, 0, "")
	v.Ecosystem = "maven"
	v.Package = pkg
	v.Version = version
	return v
}

func TestWildcardExceptionCoversAGroup(t *testing.T) {
	p := &Policy{MinScore: 70, Exceptions: []Exception{{
		Ecosystem: "maven", Package: "com.acme.platform:*",
		Reason: "in-house, reviewed internally", Expires: "2027-06-30",
	}}}
	for _, pkg := range []string{"com.acme.platform:audit-log", "com.acme.platform:metrics-core"} {
		if r := p.Apply(mavenVerdict(pkg, ""), today); !r.Allowed || r.Exception == nil {
			t.Errorf("%s not covered by its group pattern: %+v", pkg, r)
		}
	}
	// The pattern names a group; a different one is not it.
	if r := p.Apply(mavenVerdict("com.acme.other:thing", ""), today); r.Allowed {
		t.Error("a group pattern reached outside its group")
	}
}

// The form from the original question: a namespace and everything under it.
func TestWildcardExceptionCoversChildNamespaces(t *testing.T) {
	p := &Policy{MinScore: 70, Exceptions: []Exception{{
		Ecosystem: "maven", Package: "com.acme.proj.*:*",
		Reason: "internal namespace", Expires: "2027-06-30",
	}}}
	for _, pkg := range []string{"com.acme.proj.api:client", "com.acme.proj.core.util:helpers"} {
		if r := p.Apply(mavenVerdict(pkg, ""), today); !r.Allowed {
			t.Errorf("%s not covered by the namespace pattern", pkg)
		}
	}
	// "com.acme.proj.*" requires the dot, so a sibling group that merely
	// starts with the same letters is not swept in.
	if r := p.Apply(mavenVerdict("com.acme.projection:x", ""), today); r.Allowed {
		t.Error("the namespace pattern swept in a sibling group")
	}
}

func TestWildcardSpansNpmScopeSeparators(t *testing.T) {
	p := &Policy{MinScore: 70, Exceptions: []Exception{{
		Ecosystem: "npm", Package: "@acme/*", Reason: "ours", Expires: "2027-06-30",
	}}}
	v := verdict(vet.Reject, 0, "")
	v.Package = "@acme/design-tokens"
	if r := p.Apply(v, today); !r.Allowed {
		t.Error("a scope pattern did not cross the / in an npm scope")
	}
}

func TestWildcardExceptionStillExpires(t *testing.T) {
	p := &Policy{Exceptions: []Exception{{
		Ecosystem: "maven", Package: "com.acme.platform:*", Reason: "old", Expires: "2020-01-01",
	}}}
	if r := p.Apply(mavenVerdict("com.acme.platform:audit-log", ""), today); r.Allowed {
		t.Error("an expired pattern was applied")
	}
}

// An exact entry is the better record of a decision, so it is consulted
// first — otherwise adding a broad pattern could silently widen what a
// specific, version-scoped entry already said.
func TestExactExceptionWinsOverAPattern(t *testing.T) {
	p := &Policy{Exceptions: []Exception{
		{Ecosystem: "maven", Package: "com.acme.platform:*", Reason: "group", Expires: "2027-06-30"},
		{Ecosystem: "maven", Package: "com.acme.platform:audit-log", Reason: "reviewed 2.5.0", Version: "2.5.0"},
	}}
	r := p.Apply(mavenVerdict("com.acme.platform:audit-log", "2.5.0"), today)
	if r.Exception == nil || r.Exception.Reason != "reviewed 2.5.0" {
		t.Fatalf("the pattern shadowed the exact entry: %+v", r.Exception)
	}
	// The exact entry named a version and does not cover another; the pattern
	// still does, which is what makes the ordering safe rather than lossy.
	r = p.Apply(mavenVerdict("com.acme.platform:audit-log", "2.6.0"), today)
	if r.Exception == nil || r.Exception.Reason != "group" {
		t.Fatalf("the pattern did not cover what the exact entry declined: %+v", r.Exception)
	}
}

func TestWildcardIsScopedToItsEcosystem(t *testing.T) {
	p := &Policy{Exceptions: []Exception{{
		Ecosystem: "maven", Package: "*:*", Reason: "x", Expires: "2027-06-30",
	}}}
	v := verdict(vet.Reject, 0, "")
	v.Ecosystem = "npm"
	v.Package = "left-pad"
	if r := p.Apply(v, today); r.Allowed {
		t.Error("a maven pattern excepted an npm package")
	}
}

func TestPatternValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		e       Exception
		wantErr string
	}{
		{"pattern without expiry", Exception{Ecosystem: "maven", Package: "com.acme.*", Reason: "r"}, "expires"},
		{"pattern matching everything", Exception{Ecosystem: "maven", Package: "*", Reason: "r", Expires: "2027-06-30"}, "every package"},
		{"coordinate-shaped catch-all", Exception{Ecosystem: "maven", Package: "*:*", Reason: "r", Expires: "2027-06-30"}, "every package"},
		{"wildcard version", Exception{Ecosystem: "npm", Package: "left-pad", Version: "1.3.*", Reason: "r"}, "no wildcard"},
		{"valid pattern", Exception{Ecosystem: "maven", Package: "com.acme.*:*", Reason: "r", Expires: "2027-06-30"}, ""},
		{"exact needs no expiry", Exception{Ecosystem: "npm", Package: "left-pad", Reason: "r"}, ""},
	} {
		err := tc.e.validate()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && err == nil:
			t.Errorf("%s: wanted an error mentioning %q, got none", tc.name, tc.wantErr)
		case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.wantErr)
		}
	}
}

// Validation runs at load, so a policy file that does not say what it means
// stops the tool rather than being half-applied.
func TestLoadRejectsAnInvalidPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(path, []byte(
		`{"exceptions":[{"ecosystem":"maven","package":"com.acme.*","reason":"r"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "expires") {
		t.Fatalf("wanted a load error about expires, got %v", err)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"com.acme.platform:*", "com.acme.platform:audit-log", true},
		{"com.acme.platform:*", "com.acme.platform:", true},
		{"com.acme.platform:*", "com.acme.platformx:y", false},
		{"com.acme.proj.*:*", "com.acme.proj.api:client", true},
		{"com.acme.proj.*:*", "com.acme.proj:client", false},
		{"*:driver-license", "com.vendor.db:driver-license", true},
		{"left-pad", "left-pad", true},
		{"left-pad", "left-pads", false},
		{"COM.ACME.*:*", "com.acme.platform:audit-log", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "ac", false},
	} {
		if got := globMatch(tc.pattern, tc.s); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, wanted %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}
