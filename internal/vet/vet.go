// Package vet is the verdict engine: should this dependency be adopted?
//
// It scores a package 0-100 from the gathered facts and the ported DepMesh
// metrics, then maps the score to ADOPT / CAUTION / REJECT. Every signal
// carries a human-readable reason so the caller (a developer or a coding
// agent) can see *why*, not just a number.
//
// Non-existence is special-cased: a package the registry has never heard of
// is an immediate REJECT regardless of anything else — that is the
// anti-slopsquatting gate.
package vet

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/metrics"
	"github.com/jhberges/depmesh-ai/internal/model"
	"github.com/jhberges/depmesh-ai/internal/sources"
)

const (
	adoptThreshold   = 75
	cautionThreshold = 40

	// Packages younger than this are prime slopsquatting real estate:
	// attackers register names LLMs hallucinate, so a name that appeared on
	// the registry last week deserves heavy suspicion.
	youngPackageDays = 60

	staleYearsSoft = 2
	staleYearsHard = 5

	// Cadence-relative staleness, in multiples of the project's own average
	// release gap. minQuietDays keeps it from firing on a project that ships
	// twice a week: a fortnight of silence is not abandonment however fast a
	// project normally moves, whatever the ratio says.
	minQuietDays = 90
	overdueSoft  = 5
	overdueHard  = 12

	// Version currency, in the same unit — how many of the project's own
	// release intervals have elapsed since the pinned version shipped.
	behindSoft   = 3
	behindMedium = 8
	behindHard   = 20
)

var copyleftMarkers = []string{"GPL", "AGPL", "EUPL", "SSPL"}

type Advice string

const (
	Adopt   Advice = "ADOPT"
	Caution Advice = "CAUTION"
	Reject  Advice = "REJECT"
)

type Signal struct {
	Name   string `json:"name"`
	Delta  int    `json:"delta"`
	Reason string `json:"reason"`
}

type Verdict struct {
	Ecosystem string   `json:"ecosystem"`
	Package   string   `json:"package"`
	Advice    Advice   `json:"advice"`
	Score     int      `json:"score"`
	Signals   []Signal `json:"signals"`

	// Version is the release the caller asked about, empty when they asked
	// only about the package.
	Version       string   `json:"version,omitempty"`
	LatestVersion string   `json:"latest_version,omitempty"`
	License       string   `json:"license,omitempty"`
	ReleaseCount  int      `json:"release_count"`
	Degraded      []string `json:"degraded_sources,omitempty"`

	Pace        metrics.ReleasePaceMetrics `json:"-"`
	VersionPace metrics.VersionPace        `json:"-"`
	Facts       *model.PackageFacts        `json:"-"`
}

func (v *Verdict) JSON() string {
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

func years(n int) time.Duration { return time.Duration(n) * 365 * 24 * time.Hour }

// stalenessSignal judges how long a package has been quiet, taking the worse of
// two readings.
//
// Absolute age is the reading this always used, and it is wrong in both
// directions: it calls a healthy annually-released library stale, and says
// nothing about a weekly-released project that has not shipped in eight months.
// Cadence-relative age — how many of the project's own release intervals have
// elapsed — is the better evidence of abandonment, so it leads.
//
// The absolute thresholds stay as a floor rather than only a fallback, because
// a project whose average gap is three years and whose last release was six
// years ago is two intervals overdue and unmaintained by any honest reading.
func stalenessSignal(pace metrics.ReleasePaceMetrics, latest *model.ReleaseRef, age time.Duration, today time.Time) Signal {
	days := int(age.Hours() / 24)

	absolute := 0
	switch {
	case age > years(staleYearsHard):
		absolute = -40
	case age > years(staleYearsSoft):
		absolute = -20
	}

	cadence, cadenceReason := 0, ""
	if overdue, ok := pace.Overdue(today); ok && days >= minQuietDays {
		switch {
		case overdue >= overdueHard:
			cadence = -30
		case overdue >= overdueSoft:
			cadence = -15
		}
		if cadence < 0 {
			cadenceReason = fmt.Sprintf(
				"no release for %d days — %.0f× this project's own average gap of %d days",
				days, overdue, int(pace.OverallPace.Hours()/24))
		}
	}

	switch delta := min(absolute, cadence); {
	case delta == 0:
		return Signal{"staleness", 0,
			fmt.Sprintf("most recent release %s is %d days old", latest.Version, days)}
	case delta == cadence && cadenceReason != "":
		return Signal{"staleness", delta, cadenceReason}
	case age > years(staleYearsHard):
		return Signal{"staleness", delta,
			fmt.Sprintf("most recent release %s is %d years old — likely unmaintained", latest.Version, days/365)}
	default:
		return Signal{"staleness", delta,
			fmt.Sprintf("most recent release %s is over %d years old", latest.Version, staleYearsSoft)}
	}
}

func paceSignals(pace metrics.ReleasePaceMetrics, today time.Time) []Signal {
	var signals []Signal
	if pace.State == metrics.NoData {
		return []Signal{{"release-history", -15, "no dated releases found; cannot judge cadence"}}
	}
	if pace.State == metrics.NotEnoughData {
		count := 2
		if pace.FirstRelease == pace.LatestRelease {
			count = 1
		}
		signals = append(signals, Signal{
			"release-history", -10,
			fmt.Sprintf("only %d release(s); too little history to judge cadence", count),
		})
	}

	// The version named here is whatever was published most recently, *except*
	// when that is a stable release the adapter did not call the latest — see
	// metrics.BuildReleasePaceAnchored. Adapters that can tell a pre-release
	// apart report the newest stable version as the latest, because that is
	// what a reader would install and what advisories are looked up against.
	// Staleness asks a different question — has anyone touched this at all —
	// so a fresh beta still counts.
	if latest := pace.LatestRelease; latest != nil && latest.ReleaseDate != nil {
		age := today.Sub(*latest.ReleaseDate)
		signals = append(signals, stalenessSignal(pace, latest, age, today))
		if first := pace.FirstRelease; age < time.Duration(youngPackageDays)*24*time.Hour &&
			first != nil && first.ReleaseDate != nil {
			firstAge := today.Sub(*first.ReleaseDate)
			if firstAge < time.Duration(youngPackageDays)*24*time.Hour {
				signals = append(signals, Signal{
					"package-age", -35,
					fmt.Sprintf("package first published only %d days ago — young names are common slopsquatting targets",
						int(firstAge.Hours()/24)),
				})
			}
		}
	}

	if pace.State == metrics.OK {
		signals = append(signals, Signal{
			"release-pace", 0,
			fmt.Sprintf("average gap between releases: %d days (last three: %d days)",
				int(pace.OverallPace.Hours()/24), int(pace.Last3Pace.Hours()/24)),
		})
	}
	return signals
}

// versionSignals judge the requested release rather than the package.
//
// They are prefixed "version-" and reported alongside the package signals
// rather than instead of them, because the two together name an action that
// neither names alone: a healthy package with a stale pin means upgrade, an
// abandoned package with a current pin means drop, and a caller can only tell
// those apart if the verdict says which half the problem is in.
//
// hard signals describe a version that is unsafe and may reach REJECT on their
// own. soft signals describe a version that is merely behind, and are trimmed
// by soften so they cannot.
func versionSignals(
	requested *model.VersionFacts,
	vp metrics.VersionPace,
	pace metrics.ReleasePaceMetrics,
	packageLicense string,
) (hard, soft []Signal) {
	if requested.Snapshot {
		return nil, []Signal{{"version-snapshot", -25, fmt.Sprintf(
			"%s is a snapshot: its contents change without the version changing, so it cannot be vetted",
			requested.Version)}}
	}

	if requested.Exists == nil {
		soft = append(soft, Signal{"version-existence", 0, fmt.Sprintf(
			"could not confirm that version %s exists — this is unverified, not approval", requested.Version)})
	}

	if requested.Yanked || requested.Deprecated {
		message := requested.DeprecationMessage
		if message == "" {
			message = "no message"
		}
		hard = append(hard, Signal{"version-yanked", -50, fmt.Sprintf(
			"version %s has been withdrawn by its maintainers: %s", requested.Version, message)})
	}

	if requested.AdvisoryCount != nil {
		if count := *requested.AdvisoryCount; count > 0 {
			hard = append(hard, Signal{"version-advisories", -20 * min(count, 3), fmt.Sprintf(
				"%d known advisory(ies) affect version %s", count, requested.Version)})
		} else {
			hard = append(hard, Signal{"version-advisories", 0,
				"no known advisories for version " + requested.Version})
		}
	}

	// A license that differs between the pinned version and the latest one is
	// relicensing, and it changes what you may do with this pin specifically.
	// An *absent* per-version license is our own gap rather than the package's
	// — PyPI does not give one without an extra request — so it is not scored.
	if requested.License != "" && packageLicense != "" && !strings.EqualFold(requested.License, packageLicense) {
		delta := -10
		if isCopyleft(requested.License) && !isCopyleft(packageLicense) {
			delta = -15
		}
		hard = append(hard, Signal{"version-license", delta, fmt.Sprintf(
			"version %s is licensed %s where the latest release is %s — relicensing between versions changes what this pin permits",
			requested.Version, requested.License, packageLicense)})
	}

	// Enough to cost a band on its own: pinning a release candidate in
	// production is worth a caution even when everything else is perfect.
	if model.IsPrerelease(requested.Version) {
		soft = append(soft, Signal{"version-prerelease", -30,
			requested.Version + " is a prerelease — not intended for production pinning"})
	}

	soft = append(soft, currencySignal(requested, vp, pace))

	if vp.SupersededForKnown && vp.SupersededFor > years(1) {
		soft = append(soft, Signal{"version-superseded", 0, fmt.Sprintf(
			"superseded for %d days", int(vp.SupersededFor.Hours()/24))})
	}

	// Informational, not scored: the staleness signal already prices a project
	// that has gone quiet, and charging for the same silence twice would be
	// double counting. It is reported because it answers a question nothing
	// else does — whether the project decayed *after* this version was pinned.
	if vp.CadenceShift >= 3 {
		soft = append(soft, Signal{"version-cadence-shift", 0, fmt.Sprintf(
			"the project has slowed since %s: releases came every %d days around then, every %d days lately",
			requested.Version, int(vp.PaceAtVersion.Hours()/24), int(pace.Last3Pace.Hours()/24))})
	}
	return hard, soft
}

// currencySignal prices how far behind a pinned version is, in the project's own
// release intervals rather than in absolute time. Four hundred days behind is
// routine for a library that ships annually and alarming for one that ships
// fortnightly, and the ratio is what makes those two readings different.
//
// Where the ratio cannot be computed the signal still reports what is known and
// scores nothing, rather than letting an absent measurement read as "current".
func currencySignal(requested *model.VersionFacts, vp metrics.VersionPace, pace metrics.ReleasePaceMetrics) Signal {
	switch {
	case requested.IsLatest:
		return Signal{"version-latest", 0, "version " + requested.Version + " is the latest release"}

	case !vp.Located:
		return Signal{"version-currency", 0, fmt.Sprintf(
			"version %s is not in the release history we could read, so how far behind it is is unknown",
			requested.Version)}

	case vp.Normalized:
		delta := 0
		switch {
		case vp.IntervalsBehind >= behindHard:
			delta = -25
		case vp.IntervalsBehind >= behindMedium:
			delta = -15
		case vp.IntervalsBehind >= behindSoft:
			delta = -8
		}
		return Signal{"version-currency", delta, fmt.Sprintf(
			"%d release(s) published since, %d days behind the latest — %.1f× this project's average gap of %d days",
			vp.ReleasesSince, int(vp.Lag.Hours()/24), vp.IntervalsBehind, int(pace.OverallPace.Hours()/24))}

	case vp.LagKnown:
		return Signal{"version-currency", 0, fmt.Sprintf(
			"%d release(s) published since and %d days behind the latest, but this project has no measurable cadence to compare that against",
			vp.ReleasesSince, int(vp.Lag.Hours()/24))}

	default:
		return Signal{"version-currency", 0, fmt.Sprintf(
			"no release date for version %s, so how far behind it is cannot be measured", requested.Version)}
	}
}

// soften trims the version penalties that describe being *behind* rather than
// being unsafe, so that lag or a prerelease pin costs at most one verdict band.
// Being behind is an upgrade task; being unsafe is a block, and only the latter
// should reach REJECT on its own.
//
// The deltas themselves are trimmed rather than the total, so the numbers
// printed beside each reason still reconcile with the score. Integer division
// can leave a little of the budget unspent, which errs toward leniency.
func soften(soft []Signal, base int) []Signal {
	total := 0
	for _, s := range soft {
		total += s.Delta
	}
	budget := max(0, base-cautionThreshold)
	if total >= -budget {
		return soft
	}

	trimmed := make([]Signal, 0, len(soft))
	spent := 0
	for _, signal := range soft {
		if signal.Delta < 0 {
			share := min((-signal.Delta)*budget/-total, budget-spent)
			spent += share
			signal.Delta = -share
		}
		trimmed = append(trimmed, signal)
	}
	return trimmed
}

func Evaluate(facts *model.PackageFacts, today time.Time) *Verdict {
	verdict := &Verdict{
		Ecosystem: string(facts.Ecosystem),
		Package:   facts.Name,
		Degraded:  facts.Degraded,
		Facts:     facts,
	}
	if facts.LatestVersion != "" {
		verdict.LatestVersion = facts.LatestVersion
	}
	if facts.Requested != nil {
		verdict.Version = facts.Requested.Version
	}
	verdict.License = facts.License
	verdict.ReleaseCount = len(facts.Releases)

	if facts.Exists != nil && !*facts.Exists {
		verdict.Advice = Reject
		verdict.Score = 0
		verdict.Signals = []Signal{{
			"existence", -100,
			"package does not exist on the registry — if an AI assistant suggested it, " +
				"this is likely a hallucinated (slopsquattable) name",
		}}
		return verdict
	}

	// A version the registry authoritatively denied is a REJECT of the same
	// family, and the same failure mode: a model that invents a plausible
	// version number ("express@4.18.99") is doing what one that invents a
	// plausible package name does. It is *not* the same telemetry event,
	// though — nobody can register version 4.18.99 of someone else's package,
	// so a hallucinated version is not a slopsquat target and is not reported.
	if r := facts.Requested; r != nil && r.Exists != nil && !*r.Exists {
		verdict.Advice = Reject
		verdict.Score = 0
		verdict.Signals = []Signal{
			{"existence", 0, "package exists on the registry"},
			{"version-existence", -100, fmt.Sprintf(
				"version %s does not exist on the registry — the package is real but this version is not, "+
					"which is what an AI assistant inventing a plausible version number looks like", r.Version)},
		}
		return verdict
	}

	var signals []Signal
	signals = append(signals, Signal{"existence", 0, "package exists on the registry"})

	if facts.Deprecated {
		message := facts.DeprecationMessage
		if message == "" {
			message = "no message"
		}
		signals = append(signals, Signal{
			"deprecation", -50,
			"deprecated by its maintainers: " + message,
		})
	}

	// Anchored on dist-tags.latest rather than on whatever is newest by date:
	// a project maintaining parallel majors publishes 3.x patches after 4.0.0,
	// and staleness measured from a backport reads a live project as fresher
	// than it is.
	pace := metrics.BuildReleasePaceAnchored(facts.Releases, facts.LatestVersion)
	verdict.Pace = pace
	signals = append(signals, paceSignals(pace, today)...)

	if facts.MaintainerCount != nil {
		if *facts.MaintainerCount <= 1 {
			signals = append(signals, Signal{"maintainers", -10, "single maintainer — bus-factor risk"})
		} else {
			signals = append(signals, Signal{
				"maintainers", 0,
				fmt.Sprintf("%d maintainers", *facts.MaintainerCount),
			})
		}
	}

	switch license := facts.License; {
	case license == "":
		signals = append(signals, Signal{"license", -15, "no license declared — legal risk for adoption"})
	case isCopyleft(license):
		signals = append(signals, Signal{
			"license", -10,
			fmt.Sprintf("copyleft license (%s) — verify compatibility with your distribution model", license),
		})
	default:
		signals = append(signals, Signal{"license", 0, "license: " + license})
	}

	// Named rather than "the latest version", because with a version requested
	// deps.dev is asked about that one instead and this signal is absent.
	if facts.AdvisoryCount != nil {
		examined := facts.LatestVersion
		if examined == "" {
			examined = "the latest version"
		}
		if count := *facts.AdvisoryCount; count > 0 {
			signals = append(signals, Signal{
				"advisories", -20 * min(count, 3),
				fmt.Sprintf("%d known advisory(ies) affect %s", count, examined),
			})
		} else {
			signals = append(signals, Signal{"advisories", 0, "no known advisories for " + examined})
		}
	}

	// Version signals split by whether they can block on their own: soft ones
	// are applied against whatever room the package score leaves above the
	// CAUTION floor, so being behind can never alone produce a REJECT.
	var soft []Signal
	if requested := facts.Requested; requested != nil {
		vp := metrics.BuildVersionPace(facts.Releases, pace, requested.Resolved, today)
		verdict.VersionPace = vp
		hard, softVersion := versionSignals(requested, vp, pace, facts.License)
		signals = append(signals, hard...)
		soft = softVersion
	}

	base := 100
	for _, s := range signals {
		base += s.Delta
	}
	base = max(0, min(100, base))

	score := base
	for _, s := range soften(soft, base) {
		signals = append(signals, s)
		score += s.Delta
	}
	score = max(0, min(100, score))

	verdict.Score = score
	verdict.Signals = signals
	switch {
	case score >= adoptThreshold:
		verdict.Advice = Adopt
	case score >= cautionThreshold:
		verdict.Advice = Caution
	default:
		verdict.Advice = Reject
	}
	return verdict
}

func isCopyleft(license string) bool {
	upper := strings.ToUpper(license)
	if strings.Contains(upper, "LGPL") {
		return false
	}
	for _, marker := range copyleftMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// Vet gathers facts for the package and evaluates them. version is optional;
// when given it must be an exact version rather than a constraint, and the
// verdict then judges both the package and that release.
func Vet(ecosystem, name, version string, enrich bool) (*Verdict, error) {
	eco, err := model.ParseEcosystem(ecosystem)
	if err != nil {
		return nil, err
	}
	facts, err := sources.Gather(eco, name, version, enrich)
	if err != nil {
		return nil, err
	}
	return Evaluate(facts, time.Now().UTC()), nil
}
