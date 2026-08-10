package metrics

// Version-anchored cadence: where one requested version sits in a package's
// release history, and how far behind it is measured in the project's *own*
// release interval rather than in absolute time.
//
// Absolute lag says little on its own. Four hundred days behind is routine for
// a library that ships once a year and alarming for one that ships
// fortnightly, so the numbers that carry meaning here are ratios against the
// pace the same history produces. That is also why this file needs almost no
// new machinery: CalculateAveragePace already accepts an arbitrary window,
// which is exactly the primitive a version-anchored cadence wants.

import (
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// VersionPace places one version in a release history.
//
// The three "known" flags exist because zero is a legitimate value for every
// duration and ratio here — a caller must be able to tell "this version is
// current" from "we could not work out how far behind it is". Maven release
// dates in particular are scraped from an HTML directory listing and are
// frequently missing.
type VersionPace struct {
	// Located is false when the version does not appear in the release
	// history at all, in which case nothing below is meaningful.
	Located bool
	// Index is the version's position in the newest-first list, or -1.
	Index int

	// Lag is the time between this release and the package's latest one.
	Lag      time.Duration
	LagKnown bool

	// ReleasesSince counts releases published after this one, excluding
	// prereleases, snapshots, and yanked releases — none of those are upgrade
	// targets.
	//
	// It is named "since" rather than "behind" deliberately. A project
	// maintaining parallel majors publishes 3.x patches after 4.0.0, so a
	// date-ordered history interleaves branches and some of these may not be
	// ahead of the requested version at all. Turning this into a true "behind"
	// count needs version ordering, which is a later phase; until then this is
	// the claim the data actually supports.
	ReleasesSince int

	// SupersededFor is how long this version has been superseded — the age of
	// the release that immediately followed it. It separates "released three
	// years ago and still the latest" (a package-level question) from
	// "released three years ago and superseded ever since" (a version-level
	// one).
	SupersededFor      time.Duration
	SupersededForKnown bool

	// PaceAtVersion is the release interval around the time this version
	// shipped. Zero when there was no window to measure it over.
	PaceAtVersion time.Duration

	// IntervalsBehind is Lag over the project's overall pace: "you are this
	// many releases' worth of time behind". Normalized reports whether it and
	// CadenceShift could be computed at all; when it is false they must not be
	// read as zero.
	Normalized      bool
	IntervalsBehind float64

	// CadenceShift is recent pace over pace-at-version. Above 1 the project
	// slowed down after this release; below 1 it sped up. It answers a
	// question nothing else here does — *did this project decay after we
	// adopted it?* — because a dependency pinned when releases came
	// fortnightly, on a project since silent for a year, is a different risk
	// from one that was always slow. Zero when unknown.
	CadenceShift float64
}

// IndexOf finds a version in a release list by exact string match. Matching is
// exact on purpose: the source layer resolves a requested spelling to the
// registry's own before anything gets here, so this never has to guess.
func IndexOf(refs []model.ReleaseRef, version string) int {
	if version == "" {
		return -1
	}
	for i := range refs {
		if refs[i].Version == version {
			return i
		}
	}
	return -1
}

// BuildReleasePaceAnchored is BuildReleasePace with LatestRelease pinned to a
// named version instead of to whatever is newest by date.
//
// The head of a date-ordered list is not always the latest release: a project
// maintaining parallel majors publishes 3.x patches after 4.0.0, so refs[0] can
// be a backport. Staleness measured from a backport reads a live project as
// fresher than it is, and a version's lag measured against one is measured
// against the wrong end of the history.
//
// A newer *prerelease* is the opposite case, and must not be re-anchored away.
// Adapters that can tell a prerelease apart report the newest stable version as
// the latest, because that is what a reader installs and what advisories are
// looked up against — so a fresh 5.0.0-beta1 sitting above a stable 4.9.0 is
// not a backport, it is someone actively working. Re-anchoring there would
// declare a busy project stale.
//
// Both cases look identical by date, and telling them apart properly needs
// version ordering, which this package does not have. What it does have is
// enough: a *stable* release above the declared latest is a backport, and a
// prerelease above it is activity. So the anchor moves only when the date-newest
// release is stable.
//
// The pace figures are deliberately unchanged — "the gaps between the last
// three publishes" remains the right reading of cadence whichever branch those
// publishes came from.
func BuildReleasePaceAnchored(refs []model.ReleaseRef, latestVersion string) ReleasePaceMetrics {
	m := BuildReleasePace(refs)
	if m.State == NoData || latestVersion == "" {
		return m
	}
	if len(refs) > 0 && model.IsPrerelease(refs[0].Version) {
		return m
	}
	if idx := IndexOf(refs, latestVersion); idx >= 0 {
		m.LatestRelease = &refs[idx]
	}
	return m
}

// Overdue is how many of the project's own average release intervals have
// passed since its latest release.
//
// ok is false when there is no usable cadence to divide by, and a caller must
// then fall back to absolute age rather than read 0 as "on time". This is the
// package-level counterpart to IntervalsBehind, and a better abandonment
// signal than a fixed threshold: it catches a weekly project that has gone
// quiet for eight months, and stops penalizing one that ships annually by
// design.
func (m ReleasePaceMetrics) Overdue(now time.Time) (float64, bool) {
	if m.State != OK || m.OverallPace <= 0 {
		return 0, false
	}
	if m.LatestRelease == nil || m.LatestRelease.ReleaseDate == nil {
		return 0, false
	}
	return float64(now.Sub(*m.LatestRelease.ReleaseDate)) / float64(m.OverallPace), true
}

// BuildVersionPace locates version in refs and derives its position. pace must
// be the metrics for the same list, so that both agree on which release is the
// latest one.
func BuildVersionPace(refs []model.ReleaseRef, pace ReleasePaceMetrics, version string, now time.Time) VersionPace {
	vp := VersionPace{Index: -1}
	idx := IndexOf(refs, version)
	if idx < 0 {
		return vp
	}
	vp.Located, vp.Index = true, idx
	target := refs[idx]

	if anchor := pace.LatestRelease; anchor != nil && anchor.ReleaseDate != nil && target.ReleaseDate != nil {
		vp.LagKnown = true
		vp.Lag = anchor.ReleaseDate.Sub(*target.ReleaseDate)
		// Negative means the anchor is this version itself, or that a backport
		// was published after it. Neither is "ahead", so clamp rather than
		// report a negative lag.
		if vp.Lag < 0 {
			vp.Lag = 0
		}
	}

	vp.ReleasesSince = releasesSince(refs, idx)

	if idx > 0 {
		if next := refs[idx-1]; next.ReleaseDate != nil {
			vp.SupersededFor, vp.SupersededForKnown = now.Sub(*next.ReleaseDate), true
		}
	}

	// CalculateAveragePace over a window of one returns zero: the divisor
	// clamps to 1 and the loop never adds a gap. Zero would read as
	// instantaneous releases and divide badly, so require a real window.
	if idx+2 <= len(refs)-1 {
		vp.PaceAtVersion = CalculateAveragePace(refs, idx, idx+2)
	}

	if pace.State == OK && pace.OverallPace > 0 && vp.LagKnown {
		vp.Normalized = true
		vp.IntervalsBehind = float64(vp.Lag) / float64(pace.OverallPace)
	}
	if vp.PaceAtVersion > 0 && pace.Last3Pace > 0 {
		vp.CadenceShift = float64(pace.Last3Pace) / float64(vp.PaceAtVersion)
	}
	return vp
}

// releasesSince counts the real releases newer than refs[idx].
func releasesSince(refs []model.ReleaseRef, idx int) int {
	count := 0
	for i := 0; i < idx; i++ {
		if refs[i].Yanked || model.IsPrerelease(refs[i].Version) {
			continue
		}
		count++
	}
	return count
}
