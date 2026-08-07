package vet

import (
	"strings"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// monthlyHistory is a newest-first history of n releases 30 days apart, the
// newest youngest days ago, named v0 (newest) through v{n-1} (oldest).
func monthlyHistory(n, youngest int) []model.ReleaseRef {
	refs := make([]model.ReleaseRef, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, model.ReleaseRef{
			Version:     versionName(i),
			ReleaseDate: daysAgo(youngest + i*30),
		})
	}
	return refs
}

func versionName(i int) string { return "1." + string(rune('0'+i/10)) + "." + string(rune('0'+i%10)) }

// pinned returns healthy facts with a long release history and version pinned.
func pinned(version string, releases int) *model.PackageFacts {
	facts := healthyFacts()
	facts.Releases = monthlyHistory(releases, 10)
	facts.LatestVersion = facts.Releases[0].Version
	facts.Requested = &model.VersionFacts{
		Version:  version,
		Resolved: version,
		Exists:   model.Bool(true),
		IsLatest: version == facts.LatestVersion,
	}
	return facts
}

func signal(v *Verdict, name string) (Signal, bool) {
	for _, s := range v.Signals {
		if s.Name == name {
			return s, true
		}
	}
	return Signal{}, false
}

func TestNoRequestedVersionEmitsNoVersionSignals(t *testing.T) {
	v := Evaluate(healthyFacts(), today)
	for _, s := range v.Signals {
		if strings.HasPrefix(s.Name, "version-") {
			t.Fatalf("version signal %q emitted without a requested version", s.Name)
		}
	}
	if v.Version != "" {
		t.Fatalf("verdict carries version %q", v.Version)
	}
}

func TestNonexistentVersionIsRejected(t *testing.T) {
	facts := healthyFacts()
	facts.Requested = &model.VersionFacts{Version: "4.18.99", Exists: model.Bool(false)}
	v := Evaluate(facts, today)
	if v.Advice != Reject || v.Score != 0 {
		t.Fatalf("got %s/%d, want REJECT/0", v.Advice, v.Score)
	}
	existence, ok := signal(v, "version-existence")
	if !ok || existence.Delta != -100 {
		t.Fatalf("version-existence signal = %+v", existence)
	}
	// The package is real; only the version is not. Saying otherwise would
	// send someone hunting for a slopsquatted package name.
	if !strings.Contains(existence.Reason, "package is real") {
		t.Fatalf("reason does not distinguish package from version: %q", existence.Reason)
	}
	if pkg, _ := signal(v, "existence"); pkg.Delta != 0 {
		t.Fatalf("package existence penalized: %+v", pkg)
	}
}

func TestUnconfirmedVersionIsNotRejected(t *testing.T) {
	facts := healthyFacts()
	// Exists nil: the registry never answered. Tri-state exists so this cannot
	// be reported as a hallucinated version.
	facts.Requested = &model.VersionFacts{Version: "2.0.0", Exists: nil}
	v := Evaluate(facts, today)
	if v.Score == 0 || v.Advice == Reject {
		t.Fatalf("unconfirmed version treated as nonexistent: %s/%d", v.Advice, v.Score)
	}
	existence, ok := signal(v, "version-existence")
	if !ok || existence.Delta != 0 {
		t.Fatalf("version-existence = %+v, want a 0-delta caveat", existence)
	}
	if !strings.Contains(existence.Reason, "not approval") {
		t.Fatalf("unconfirmed existence does not warn against reading it as approval: %q", existence.Reason)
	}
}

func TestPinningTheLatestReleaseIsClean(t *testing.T) {
	facts := pinned("", 12)
	facts.Requested.Version = facts.LatestVersion
	facts.Requested.Resolved = facts.LatestVersion
	facts.Requested.IsLatest = true

	v := Evaluate(facts, today)
	if v.Advice != Adopt {
		t.Fatalf("latest release got %s/%d", v.Advice, v.Score)
	}
	if _, ok := signal(v, "version-latest"); !ok {
		t.Fatal("missing version-latest signal")
	}
	if currency, ok := signal(v, "version-currency"); ok {
		t.Fatalf("currency penalty on the latest release: %+v", currency)
	}
}

func TestStalePinIsPenalizedInReleaseIntervals(t *testing.T) {
	facts := pinned(versionName(29), 30) // oldest of thirty monthly releases
	v := Evaluate(facts, today)

	currency, ok := signal(v, "version-currency")
	if !ok || currency.Delta >= 0 {
		t.Fatalf("version-currency = %+v, want a penalty", currency)
	}
	// The number that matters is the ratio, not the raw day count.
	if !strings.Contains(currency.Reason, "average gap") {
		t.Fatalf("currency reason is not cadence-relative: %q", currency.Reason)
	}
	if !strings.Contains(currency.Reason, "29 release(s) published since") {
		t.Fatalf("currency reason miscounts releases: %q", currency.Reason)
	}
	// The package itself is fine, so lag alone must not block it.
	if v.Advice == Reject {
		t.Fatalf("a healthy package with a stale pin got REJECT (%d)", v.Score)
	}
}

func TestLagAloneCannotProduceReject(t *testing.T) {
	// Package signals put the base at 55: no license (-15), one maintainer
	// (-10), one advisory (-20). The soft currency penalty is -25, which would
	// carry the score to 30 and a REJECT if it were applied in full.
	facts := pinned(versionName(29), 30)
	facts.License = ""
	facts.MaintainerCount = model.Int(1)
	facts.AdvisoryCount = model.Int(1)

	v := Evaluate(facts, today)
	if v.Advice == Reject {
		t.Fatalf("lag pushed a CAUTION package into REJECT (%d)", v.Score)
	}
	if v.Score != cautionThreshold {
		t.Fatalf("score = %d, want it trimmed to the CAUTION floor %d", v.Score, cautionThreshold)
	}
	// The trimmed delta must be what is printed, so the reasons still add up.
	currency, _ := signal(v, "version-currency")
	if currency.Delta != -15 {
		t.Fatalf("currency delta = %d, want it trimmed to -15", currency.Delta)
	}
	sum := 100
	for _, s := range v.Signals {
		sum += s.Delta
	}
	if sum != v.Score {
		t.Fatalf("signal deltas sum to %d but score is %d", sum, v.Score)
	}
}

func TestUnsafeVersionCanStillReject(t *testing.T) {
	// A yanked version and advisories are not "behind", they are unsafe, and
	// they are free to reach REJECT on their own.
	facts := pinned(versionName(5), 12)
	facts.Requested.Yanked = true
	facts.Requested.DeprecationMessage = "critical regression, do not use"
	facts.Requested.AdvisoryCount = model.Int(2)

	v := Evaluate(facts, today)
	if v.Advice != Reject {
		t.Fatalf("yanked version with advisories got %s/%d", v.Advice, v.Score)
	}
	yanked, ok := signal(v, "version-yanked")
	if !ok || !strings.Contains(yanked.Reason, "critical regression") {
		t.Fatalf("version-yanked = %+v", yanked)
	}
	if advisories, ok := signal(v, "version-advisories"); !ok || advisories.Delta != -40 {
		t.Fatalf("version-advisories = %+v, want -40", advisories)
	}
}

func TestAdvisoriesNameTheVersionTheyWereCheckedAgainst(t *testing.T) {
	facts := pinned(versionName(5), 12)
	facts.Requested.AdvisoryCount = model.Int(1)
	v := Evaluate(facts, today)

	advisories, _ := signal(v, "version-advisories")
	if !strings.Contains(advisories.Reason, versionName(5)) {
		t.Fatalf("advisory reason does not name the version: %q", advisories.Reason)
	}
	// The package-level signal must name the release it examined rather than
	// claiming "the latest version" generically.
	packageFacts := healthyFacts()
	packageFacts.AdvisoryCount = model.Int(0)
	pkg, _ := signal(Evaluate(packageFacts, today), "advisories")
	if !strings.Contains(pkg.Reason, packageFacts.LatestVersion) {
		t.Fatalf("package advisory reason does not name the version: %q", pkg.Reason)
	}
}

func TestRelicensingBetweenVersionsIsFlagged(t *testing.T) {
	facts := pinned(versionName(5), 12)
	facts.License = "BUSL-1.1"          // the latest release relicensed
	facts.Requested.License = "MPL-2.0" // the pin predates it

	v := Evaluate(facts, today)
	license, ok := signal(v, "version-license")
	if !ok || license.Delta >= 0 {
		t.Fatalf("version-license = %+v, want a penalty", license)
	}
	if !strings.Contains(license.Reason, "MPL-2.0") || !strings.Contains(license.Reason, "BUSL-1.1") {
		t.Fatalf("reason does not name both licenses: %q", license.Reason)
	}
}

func TestMatchingLicenseIsNotFlagged(t *testing.T) {
	facts := pinned(versionName(5), 12)
	facts.Requested.License = "mit" // same license, different case
	if _, ok := signal(Evaluate(facts, today), "version-license"); ok {
		t.Fatal("identical license flagged as relicensing")
	}
}

func TestAbsentPerVersionLicenseIsNotPenalized(t *testing.T) {
	// PyPI gives no per-version license without an extra request. That gap is
	// ours, not the package's, so it must not cost the package anything.
	facts := pinned(versionName(5), 12)
	facts.Requested.License = ""
	if _, ok := signal(Evaluate(facts, today), "version-license"); ok {
		t.Fatal("missing per-version license penalized")
	}
}

func TestPrereleasePinIsFlagged(t *testing.T) {
	facts := pinned("2.0.0-rc1", 12)
	v := Evaluate(facts, today)
	if _, ok := signal(v, "version-prerelease"); !ok {
		t.Fatal("missing version-prerelease signal")
	}
	if v.Advice == Adopt {
		t.Fatalf("prerelease pin got ADOPT (%d)", v.Score)
	}
}

func TestSnapshotIsReportedAsUnvettable(t *testing.T) {
	facts := pinned("1.0.0-SNAPSHOT", 12)
	// A snapshot is absent from release metadata by design, so existence is
	// unknown rather than false.
	facts.Requested.Exists = nil
	facts.Requested.Snapshot = true

	v := Evaluate(facts, today)
	snapshot, ok := signal(v, "version-snapshot")
	if !ok || snapshot.Delta >= 0 {
		t.Fatalf("version-snapshot = %+v", snapshot)
	}
	if !strings.Contains(snapshot.Reason, "cannot be vetted") {
		t.Fatalf("snapshot reason does not say it is unvettable: %q", snapshot.Reason)
	}
	if v.Advice == Reject {
		t.Fatal("a snapshot was rejected rather than reported as unvettable")
	}
	// A snapshot has no history to be behind, so no currency claim at all.
	if _, ok := signal(v, "version-currency"); ok {
		t.Fatal("currency measured for a snapshot")
	}
}

func TestVersionWithNoReleaseDateReportsCurrencyUnknown(t *testing.T) {
	// The Maven case: metadata listed the version but the scraped directory
	// listing had no dates.
	facts := healthyFacts()
	facts.Ecosystem = model.Maven
	facts.Releases = []model.ReleaseRef{{Version: "3.0"}, {Version: "2.0"}, {Version: "1.0"}}
	facts.LatestVersion = "3.0"
	facts.Requested = &model.VersionFacts{
		Version: "1.0", Resolved: "1.0", Exists: model.Bool(true),
	}
	v := Evaluate(facts, today)
	currency, ok := signal(v, "version-currency")
	if !ok || currency.Delta != 0 {
		t.Fatalf("version-currency = %+v, want an unscored unknown", currency)
	}
	if !strings.Contains(currency.Reason, "cannot be measured") {
		t.Fatalf("reason does not state the measurement is unavailable: %q", currency.Reason)
	}
}

func TestQuietFastMovingProjectIsStaleBeforeTwoYears(t *testing.T) {
	// Weekly releases, then 200 days of silence. The absolute two-year
	// threshold says nothing; the project's own cadence says 28 intervals.
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "1.0.4", ReleaseDate: daysAgo(200)},
		{Version: "1.0.3", ReleaseDate: daysAgo(207)},
		{Version: "1.0.2", ReleaseDate: daysAgo(214)},
		{Version: "1.0.1", ReleaseDate: daysAgo(221)},
	}
	facts.LatestVersion = "1.0.4"

	staleness, ok := signal(Evaluate(facts, today), "staleness")
	if !ok || staleness.Delta >= 0 {
		t.Fatalf("staleness = %+v, want a penalty well before two years", staleness)
	}
	if !strings.Contains(staleness.Reason, "own average gap") {
		t.Fatalf("staleness reason is not cadence-relative: %q", staleness.Reason)
	}
}

func TestShortSilenceOnAFastProjectIsNotStale(t *testing.T) {
	// Same cadence, only 60 days quiet. Many intervals, but a fortnight or two
	// of silence is not abandonment however fast a project normally ships.
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "1.0.4", ReleaseDate: daysAgo(60)},
		{Version: "1.0.3", ReleaseDate: daysAgo(67)},
		{Version: "1.0.2", ReleaseDate: daysAgo(74)},
		{Version: "1.0.1", ReleaseDate: daysAgo(81)},
	}
	facts.LatestVersion = "1.0.4"

	staleness, _ := signal(Evaluate(facts, today), "staleness")
	if staleness.Delta != 0 {
		t.Fatalf("staleness = %+v, want no penalty inside the quiet-period floor", staleness)
	}
}

func TestSlowProjectQuietForYearsIsStillStale(t *testing.T) {
	// Yearly cadence, quiet for six years: only six intervals overdue, but
	// unmaintained by any honest reading. The absolute threshold is a floor,
	// not merely a fallback.
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "1.2.0", ReleaseDate: daysAgo(6 * 365)},
		{Version: "1.1.0", ReleaseDate: daysAgo(7 * 365)},
		{Version: "1.0.0", ReleaseDate: daysAgo(8 * 365)},
	}
	staleness, _ := signal(Evaluate(facts, today), "staleness")
	if staleness.Delta != -40 {
		t.Fatalf("staleness = %+v, want the absolute -40 floor", staleness)
	}
}

func TestStalenessAnchorsOnTheLatestReleaseNotABackport(t *testing.T) {
	// 3.1.5 is a patch on the old branch, published after 4.0.0. Measuring
	// staleness from it would read this project as three days fresh.
	facts := healthyFacts()
	facts.Releases = []model.ReleaseRef{
		{Version: "3.1.5", ReleaseDate: daysAgo(3)},
		{Version: "4.0.0", ReleaseDate: daysAgo(3 * 365)},
		{Version: "3.1.4", ReleaseDate: daysAgo(4 * 365)},
		{Version: "3.1.3", ReleaseDate: daysAgo(5 * 365)},
	}
	facts.LatestVersion = "4.0.0"

	staleness, _ := signal(Evaluate(facts, today), "staleness")
	if staleness.Delta == 0 {
		t.Fatalf("staleness = %+v, want the backport ignored", staleness)
	}
	if !strings.Contains(staleness.Reason, "4.0.0") {
		t.Fatalf("staleness measured from the wrong release: %q", staleness.Reason)
	}
}
