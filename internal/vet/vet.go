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

	LatestVersion string   `json:"latest_version,omitempty"`
	License       string   `json:"license,omitempty"`
	ReleaseCount  int      `json:"release_count"`
	Degraded      []string `json:"degraded_sources,omitempty"`

	Pace  metrics.ReleasePaceMetrics `json:"-"`
	Facts *model.PackageFacts        `json:"-"`
}

func (v *Verdict) JSON() string {
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
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

	if latest := pace.LatestRelease; latest != nil && latest.ReleaseDate != nil {
		age := today.Sub(*latest.ReleaseDate)
		years := func(n int) time.Duration { return time.Duration(n) * 365 * 24 * time.Hour }
		switch {
		case age > years(staleYearsHard):
			signals = append(signals, Signal{
				"staleness", -40,
				fmt.Sprintf("latest release %s is %d years old — likely unmaintained",
					latest.Version, int(age.Hours()/24/365)),
			})
		case age > years(staleYearsSoft):
			signals = append(signals, Signal{
				"staleness", -20,
				fmt.Sprintf("latest release %s is over %d years old", latest.Version, staleYearsSoft),
			})
		default:
			signals = append(signals, Signal{
				"staleness", 0,
				fmt.Sprintf("latest release %s is %d days old", latest.Version, int(age.Hours()/24)),
			})
		}
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

	pace := metrics.BuildReleasePace(facts.Releases)
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

	if facts.AdvisoryCount != nil {
		if count := *facts.AdvisoryCount; count > 0 {
			signals = append(signals, Signal{
				"advisories", -20 * min(count, 3),
				fmt.Sprintf("%d known advisory(ies) affect the latest version", count),
			})
		} else {
			signals = append(signals, Signal{"advisories", 0, "no known advisories for the latest version"})
		}
	}

	score := 100
	for _, s := range signals {
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

// Vet gathers facts for the package and evaluates them.
func Vet(ecosystem, name string, enrich bool) (*Verdict, error) {
	eco, err := model.ParseEcosystem(ecosystem)
	if err != nil {
		return nil, err
	}
	facts, err := sources.Gather(eco, name, enrich)
	if err != nil {
		return nil, err
	}
	return Evaluate(facts, time.Now().UTC()), nil
}
