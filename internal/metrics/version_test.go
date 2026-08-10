package metrics

import (
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

var now = time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

// monthly builds a newest-first history of n releases 30 days apart, the newest
// one youngest days before now, named v1 (oldest) upward.
func monthly(n, youngest int) []model.ReleaseRef {
	refs := make([]model.ReleaseRef, 0, n)
	for i := 0; i < n; i++ {
		date := now.AddDate(0, 0, -(youngest + i*30))
		refs = append(refs, model.ReleaseRef{
			Version:     "v" + string(rune('a'+n-1-i)),
			ReleaseDate: &date,
		})
	}
	return refs
}

func TestAnchorIgnoresABackportPublishedAfterTheLatestRelease(t *testing.T) {
	// A project maintaining parallel majors: 3.1.5 is a patch on the old branch,
	// published after 4.0.0 shipped. Newest by date is not the latest release.
	refs := []model.ReleaseRef{
		ref("3.1.5", "2024-05-20"),
		ref("4.0.0", "2024-05-01"),
		ref("3.1.4", "2024-01-01"),
		ref("3.1.3", "2023-01-01"),
	}
	if got := BuildReleasePace(refs).LatestRelease.Version; got != "3.1.5" {
		t.Fatalf("unanchored latest = %q, want the date-newest 3.1.5", got)
	}
	if got := BuildReleasePaceAnchored(refs, "4.0.0").LatestRelease.Version; got != "4.0.0" {
		t.Fatalf("anchored latest = %q, want 4.0.0", got)
	}
	// An anchor the history does not contain leaves the date ordering alone
	// rather than dropping the anchor entirely.
	if got := BuildReleasePaceAnchored(refs, "9.9.9").LatestRelease.Version; got != "3.1.5" {
		t.Fatalf("unknown anchor changed latest to %q", got)
	}
}

func TestOverdueCountsTheProjectsOwnIntervals(t *testing.T) {
	// Four releases 30 days apart, the newest 300 days ago: ten intervals of
	// silence, on a project that normally ships monthly.
	pace := BuildReleasePace(monthly(4, 300))
	overdue, ok := pace.Overdue(now)
	if !ok {
		t.Fatal("overdue not computable for an OK history")
	}
	if overdue < 9.9 || overdue > 10.1 {
		t.Fatalf("overdue = %.2f, want ~10", overdue)
	}
}

func TestOverdueIsNotComputableWithoutACadence(t *testing.T) {
	// Two releases is NOT_ENOUGH_DATA: there is no pace to divide by, and 0
	// must not be readable as "on time".
	pace := BuildReleasePace([]model.ReleaseRef{ref("2", "2024-01-01"), ref("1", "2023-01-01")})
	if _, ok := pace.Overdue(now); ok {
		t.Fatal("overdue reported for a history with no cadence")
	}
	if _, ok := BuildReleasePace(nil).Overdue(now); ok {
		t.Fatal("overdue reported for an empty history")
	}
}

func TestVersionPaceMeasuresLagInReleaseIntervals(t *testing.T) {
	refs := monthly(10, 10) // newest 10 days ago, 30-day cadence
	pace := BuildReleasePace(refs)
	oldest := refs[len(refs)-1].Version

	vp := BuildVersionPace(refs, pace, oldest, now)
	if !vp.Located || vp.Index != 9 {
		t.Fatalf("located=%v index=%d, want true/9", vp.Located, vp.Index)
	}
	if !vp.LagKnown || vp.Lag != days(270) {
		t.Fatalf("lag = %v, want 270 days", vp.Lag)
	}
	if vp.ReleasesSince != 9 {
		t.Fatalf("releases since = %d, want 9", vp.ReleasesSince)
	}
	if !vp.Normalized || vp.IntervalsBehind < 8.9 || vp.IntervalsBehind > 9.1 {
		t.Fatalf("intervals behind = %.2f (normalized %v), want ~9", vp.IntervalsBehind, vp.Normalized)
	}
	// Superseded since the release that immediately followed it.
	if !vp.SupersededForKnown || vp.SupersededFor != days(10+8*30) {
		t.Fatalf("superseded for %v, want %v", vp.SupersededFor, days(10+8*30))
	}
}

func TestVersionPaceOnTheLatestReleaseHasNoLag(t *testing.T) {
	refs := monthly(5, 10)
	vp := BuildVersionPace(refs, BuildReleasePace(refs), refs[0].Version, now)
	if !vp.Located || vp.Lag != 0 || vp.ReleasesSince != 0 {
		t.Fatalf("lag=%v since=%d, want 0/0", vp.Lag, vp.ReleasesSince)
	}
	if vp.SupersededForKnown {
		t.Fatal("the latest release reported as superseded")
	}
}

func TestVersionPaceNotLocated(t *testing.T) {
	refs := monthly(5, 10)
	vp := BuildVersionPace(refs, BuildReleasePace(refs), "9.9.9", now)
	if vp.Located || vp.Index != -1 || vp.Normalized {
		t.Fatalf("missing version reported as located: %+v", vp)
	}
}

func TestPaceAtVersionNeedsARealWindow(t *testing.T) {
	refs := monthly(5, 10)
	pace := BuildReleasePace(refs)

	// A window of one returns zero from CalculateAveragePace, which would read
	// as instantaneous releases, so the two oldest entries must report no
	// measurable local pace rather than a bogus one.
	for _, idx := range []int{3, 4} {
		vp := BuildVersionPace(refs, pace, refs[idx].Version, now)
		if vp.PaceAtVersion != 0 {
			t.Errorf("index %d reported pace-at-version %v with no window", idx, vp.PaceAtVersion)
		}
		if vp.CadenceShift != 0 {
			t.Errorf("index %d reported a cadence shift with no window", idx)
		}
	}
	if vp := BuildVersionPace(refs, pace, refs[0].Version, now); vp.PaceAtVersion != days(30) {
		t.Errorf("pace at the newest version = %v, want 30 days", vp.PaceAtVersion)
	}
}

func TestCadenceShiftDetectsAProjectSlowingDown(t *testing.T) {
	// Weekly early on, then yearly: a project that decayed after v1 was pinned.
	refs := []model.ReleaseRef{
		ref("5", "2024-01-01"),
		ref("4", "2023-01-01"),
		ref("3", "2022-01-01"),
		ref("2", "2021-12-25"),
		ref("1", "2021-12-18"),
	}
	// Measured at "3", which has the two older releases below it that a local
	// pace window needs.
	pace := BuildReleasePace(refs)
	vp := BuildVersionPace(refs, pace, "3", now)
	if vp.PaceAtVersion != days(7) {
		t.Fatalf("pace at v3 = %v, want 7 days", vp.PaceAtVersion)
	}
	if vp.CadenceShift < 40 {
		t.Fatalf("cadence shift = %.1f, want a large slowdown", vp.CadenceShift)
	}
}

func TestReleasesSinceExcludesPrereleasesAndYanks(t *testing.T) {
	yanked := ref("1.3.0", "2024-04-01")
	yanked.Yanked = true
	refs := []model.ReleaseRef{
		ref("1.4.0", "2024-05-01"),
		ref("1.4.0-rc1", "2024-04-15"),
		yanked,
		ref("1.2.0", "2024-03-01"),
		ref("1.1.0", "2024-02-01"),
	}
	vp := BuildVersionPace(refs, BuildReleasePace(refs), "1.1.0", now)
	// Three releases sit above 1.1.0, but one is a prerelease and one is
	// yanked; neither is an upgrade target.
	if vp.ReleasesSince != 2 {
		t.Fatalf("releases since = %d, want 2", vp.ReleasesSince)
	}
}

func TestLagIsNotNegativeWhenABackportFollowsTheAnchor(t *testing.T) {
	refs := []model.ReleaseRef{
		ref("3.1.5", "2024-05-20"),
		ref("4.0.0", "2024-05-01"),
		ref("3.1.4", "2024-01-01"),
	}
	pace := BuildReleasePaceAnchored(refs, "4.0.0")
	vp := BuildVersionPace(refs, pace, "3.1.5", now)
	if vp.Lag < 0 {
		t.Fatalf("lag = %v, want it clamped to zero", vp.Lag)
	}
}

func TestVersionPaceWithoutReleaseDates(t *testing.T) {
	// Maven release dates come from a scraped directory listing and are often
	// missing entirely. The version is still located; nothing is measured.
	refs := []model.ReleaseRef{ref("3.0", ""), ref("2.0", ""), ref("1.0", "")}
	vp := BuildVersionPace(refs, BuildReleasePace(refs), "1.0", now)
	if !vp.Located {
		t.Fatal("version not located")
	}
	if vp.LagKnown || vp.Normalized {
		t.Fatalf("lag reported without dates: %+v", vp)
	}
}

func TestAnchorKeepsAFreshPrereleaseAsActivity(t *testing.T) {
	// The NuGet shape: LatestVersion is the newest *stable*, because that is
	// what a reader installs and what advisories are looked up against. But a
	// beta published last week is someone actively working, and staleness asks
	// whether anyone has touched this at all — so the anchor must stay on the
	// prerelease rather than jumping back to the older stable release.
	refs := []model.ReleaseRef{
		ref("3.0.0-beta1", "2024-05-20"),
		ref("2.0.0", "2021-06-01"),
		ref("1.0.0", "2019-03-04"),
	}
	if got := BuildReleasePaceAnchored(refs, "2.0.0").LatestRelease.Version; got != "3.0.0-beta1" {
		t.Fatalf("anchored latest = %q, want the fresh prerelease to count as activity", got)
	}
	// Whereas a *stable* release above the declared latest is a backport, and
	// the anchor does move. The two cases are indistinguishable by date; this
	// is the only thing separating them without version ordering.
	backport := []model.ReleaseRef{
		ref("3.1.5", "2024-05-20"),
		ref("4.0.0", "2024-05-01"),
		ref("3.1.4", "2024-01-01"),
	}
	if got := BuildReleasePaceAnchored(backport, "4.0.0").LatestRelease.Version; got != "4.0.0" {
		t.Fatalf("anchored latest = %q, want the backport ignored", got)
	}
}
