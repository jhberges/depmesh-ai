package vet

import (
	"strings"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

var today = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

func daysAgo(n int) *time.Time {
	t := today.Add(-time.Duration(n) * 24 * time.Hour)
	return &t
}

func healthyFacts() *model.PackageFacts {
	return &model.PackageFacts{
		Ecosystem: model.NPM,
		Name:      "good-lib",
		Exists:    model.Bool(true),
		Releases: []model.ReleaseRef{
			{Version: "3.0.0", ReleaseDate: daysAgo(30)},
			{Version: "2.0.0", ReleaseDate: daysAgo(200)},
			{Version: "1.0.0", ReleaseDate: daysAgo(400)},
		},
		LatestVersion:   "3.0.0",
		License:         "MIT",
		MaintainerCount: model.Int(4),
		AdvisoryCount:   model.Int(0),
	}
}

func hasSignal(v *Verdict, name string, negative bool) bool {
	for _, s := range v.Signals {
		if s.Name == name && (!negative || s.Delta < 0) {
			return true
		}
	}
	return false
}

func TestNonexistentPackageIsRejected(t *testing.T) {
	facts := &model.PackageFacts{Ecosystem: model.NPM, Name: "hallucinated-lib", Exists: model.Bool(false)}
	v := Evaluate(facts, today)
	if v.Advice != Reject || v.Score != 0 {
		t.Fatalf("got %s/%d, want REJECT/0", v.Advice, v.Score)
	}
	if !strings.Contains(v.Signals[0].Reason, "does not exist") {
		t.Fatalf("missing existence reason: %q", v.Signals[0].Reason)
	}
}

func TestHealthyPackageIsAdopted(t *testing.T) {
	v := Evaluate(healthyFacts(), today)
	if v.Advice != Adopt || v.Score < 75 {
		t.Fatalf("got %s/%d, want ADOPT/>=75", v.Advice, v.Score)
	}
}

func TestDeprecatedPackageIsPenalized(t *testing.T) {
	facts := healthyFacts()
	facts.Deprecated = true
	facts.DeprecationMessage = "use other-lib instead"
	v := Evaluate(facts, today)
	if v.Advice == Adopt {
		t.Fatalf("deprecated package got ADOPT")
	}
	if !hasSignal(v, "deprecation", true) {
		t.Fatalf("missing deprecation signal")
	}
}

func TestStalePackageIsPenalized(t *testing.T) {
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "1.2.0", ReleaseDate: daysAgo(6 * 365)},
		{Version: "1.1.0", ReleaseDate: daysAgo(7 * 365)},
		{Version: "1.0.0", ReleaseDate: daysAgo(8 * 365)},
	}
	v := Evaluate(facts, today)
	if v.Advice == Adopt {
		t.Fatalf("stale package got ADOPT")
	}
	if !hasSignal(v, "staleness", true) {
		t.Fatalf("missing staleness penalty")
	}
}

func TestBrandNewPackageFlagsSlopsquattingRisk(t *testing.T) {
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "1.0.1", ReleaseDate: daysAgo(3)},
		{Version: "1.0.0", ReleaseDate: daysAgo(5)},
		{Version: "0.0.1", ReleaseDate: daysAgo(6)},
	}
	v := Evaluate(facts, today)
	if !hasSignal(v, "package-age", true) {
		t.Fatalf("missing package-age signal")
	}
	if v.Advice == Adopt {
		t.Fatalf("brand-new package got ADOPT")
	}
}

func TestMissingLicenseIsPenalized(t *testing.T) {
	facts := healthyFacts()
	facts.License = ""
	if v := Evaluate(facts, today); !hasSignal(v, "license", true) {
		t.Fatalf("missing license penalty")
	}
}

func TestCopyleftLicenseIsFlaggedButLGPLIsNot(t *testing.T) {
	facts := healthyFacts()
	facts.License = "GPL-3.0"
	if v := Evaluate(facts, today); !hasSignal(v, "license", true) {
		t.Fatalf("GPL not flagged")
	}
	facts.License = "LGPL-2.1"
	if v := Evaluate(facts, today); hasSignal(v, "license", true) {
		t.Fatalf("LGPL wrongly flagged as copyleft")
	}
}

func TestAdvisoriesArePenalized(t *testing.T) {
	facts := healthyFacts()
	facts.AdvisoryCount = model.Int(2)
	if v := Evaluate(facts, today); v.Advice == Adopt {
		t.Fatalf("package with advisories got ADOPT")
	}
}

func TestNetworkFailureDoesNotMeanNonexistent(t *testing.T) {
	facts := &model.PackageFacts{
		Ecosystem: model.PyPI,
		Name:      "unknown-status-lib",
		Exists:    nil, // tri-state: unknown
		Degraded:  []string{"pypi (unavailable)"},
	}
	v := Evaluate(facts, today)
	// Exists == nil must not trigger the anti-slopsquatting REJECT path.
	if v.Score == 0 {
		t.Fatalf("unknown existence treated as non-existence")
	}
}
