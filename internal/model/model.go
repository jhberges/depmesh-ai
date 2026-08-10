// Package model holds the core data types.
//
// The vocabulary descends from the original DepMesh backend model
// (com.depmesh.backend.model): Artifact/Version/VersionRef become
// PackageFacts/ReleaseRef; the metric types live in internal/metrics.
package model

import (
	"fmt"
	"strings"
	"time"
)

type Ecosystem string

const (
	NPM   Ecosystem = "npm"
	PyPI  Ecosystem = "pypi"
	Maven Ecosystem = "maven"
	NuGet Ecosystem = "nuget"
)

// Ecosystems is every ecosystem depmesh-ai can vet, oldest support first.
// It is the single list the rest of the tool reads from: ParseEcosystem
// accepts exactly these, the CLI usage and the MCP tool schema render them,
// and adding an ecosystem means appending here rather than remembering four
// separate places that drifted apart.
var Ecosystems = []Ecosystem{NPM, PyPI, Maven, NuGet}

// EcosystemStrings is Ecosystems as plain strings, for JSON schemas.
func EcosystemStrings() []string {
	out := make([]string, len(Ecosystems))
	for i, ecosystem := range Ecosystems {
		out[i] = string(ecosystem)
	}
	return out
}

// EcosystemList renders Ecosystems for help text and error messages:
// "npm, pypi, maven or nuget".
func EcosystemList() string {
	names := EcosystemStrings()
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

func ParseEcosystem(value string) (Ecosystem, error) {
	candidate := Ecosystem(strings.ToLower(value))
	for _, ecosystem := range Ecosystems {
		if ecosystem == candidate {
			return ecosystem, nil
		}
	}
	return "", fmt.Errorf("unknown ecosystem %q; expected %s", value, EcosystemList())
}

// ReleaseRef is a single released version. Descendant of legacy VersionRef.
type ReleaseRef struct {
	Version     string
	ReleaseDate *time.Time // nil when the source has no date for it
	// Yanked marks a release the maintainers have withdrawn (yanked on PyPI,
	// deprecated on npm). It is not an upgrade target, so it does not count
	// toward the gap between a pinned version and the latest one.
	Yanked bool
}

// VersionFacts is what the sources could learn about one *requested* version,
// as opposed to the package as a whole. It is populated only when a caller
// asked about a specific version.
//
// The properties that decide whether a dependency is safe are mostly the ones
// that change between versions — advisories apply to ranges, licenses get
// changed, single releases get yanked — so a package-level verdict can read
// ADOPT while the pinned coordinate is the actual problem.
type VersionFacts struct {
	// Version is what the caller asked for, verbatim.
	Version string
	// Resolved is the registry's own spelling of the same release, which can
	// differ from Version where a registry normalizes (PyPI, per PEP 440).
	// Empty when we could not match it. Everything downstream compares
	// against this, so normalization happens once, in the source layer.
	Resolved string
	// Exists is tri-state for the same reason PackageFacts.Exists is, and the
	// stakes are more asymmetric here: a false "exists" is a missed warning,
	// but a false "does not exist" tells a developer that a real, working,
	// pinned dependency is a hallucination. It is false only when the registry
	// authoritatively denied this version — never because a lookup missed.
	Exists             *bool
	ReleaseDate        *time.Time
	License            string
	Deprecated         bool
	DeprecationMessage string
	Yanked             bool
	AdvisoryCount      *int
	IsLatest           bool
	// Snapshot marks a mutable coordinate (Maven -SNAPSHOT), whose contents
	// change without the version changing. It cannot be vetted, and release
	// metadata legitimately does not list it — so its absence there is not
	// evidence of anything.
	Snapshot bool
}

// PackageFacts is everything the source layer could learn about one package.
//
// Exists is tri-state on purpose: true/false only when the registry answered
// authoritatively. A network failure must never be reported as "does not
// exist" — that would turn an outage into a slopsquatting false alarm (or
// worse, mask one).
type PackageFacts struct {
	Ecosystem Ecosystem
	Name      string
	Exists    *bool
	// Releases is ordered newest-first, matching the legacy builder's contract.
	Releases           []ReleaseRef
	LatestVersion      string
	License            string
	Description        string
	Deprecated         bool
	DeprecationMessage string
	MaintainerCount    *int
	Homepage           string
	RepositoryURL      string
	AdvisoryCount      *int
	// Degraded lists sources that could not be reached; signals depending on
	// them degrade to "unknown" instead of guessing.
	Degraded []string
	// Requested holds the facts for one specific version, when the caller
	// named one. Nil is the switch that keeps a package-only vet behaving
	// exactly as it did before versions existed.
	Requested *VersionFacts
}

func Bool(v bool) *bool { return &v }
func Int(v int) *int    { return &v }

// Version-string classification. These live here rather than in a source or a
// metrics package because every layer needs the same answer: a prerelease and
// a snapshot are both versions you cannot be meaningfully "behind on", and
// both need excluding from a release count.
var prereleaseMarkers = []string{
	"alpha", "beta", "rc", "-a", "-b", "-m", ".m", "dev", "pre", "preview",
	"canary", "next", "nightly", "cr", "-ea", "milestone",
}

// IsPrerelease reports whether a version string looks like a prerelease. It is
// a deliberately loose check across three ecosystems' conventions (1.0.0-rc1,
// 1.0.0a1, 1.0-M3, 2.0.0.CR1), and it errs toward "no": misreading a real
// release as a prerelease would exclude it from a release count that matters.
func IsPrerelease(version string) bool {
	v := strings.ToLower(strings.TrimSpace(version))
	if v == "" {
		return false
	}
	if IsSnapshot(v) {
		return true
	}
	for _, marker := range prereleaseMarkers {
		if strings.Contains(v, marker) {
			return true
		}
	}
	// PEP 440 spells prereleases without a separator: 1.0a1, 2.0b3, 1.0rc2.
	// Only a digit-then-letter transition counts, so "1.0.0.Final" (a real
	// release in Maven-land) is not caught by this.
	for i := 1; i < len(v); i++ {
		if v[i-1] >= '0' && v[i-1] <= '9' && (v[i] == 'a' || v[i] == 'b') &&
			i+1 < len(v) && v[i+1] >= '0' && v[i+1] <= '9' {
			return true
		}
	}
	return false
}

// IsSnapshot reports whether a version is a mutable Maven snapshot.
func IsSnapshot(version string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(version)), "SNAPSHOT")
}

// rangeMarkers are characters and words that mean a version *constraint*
// rather than a version. We do not resolve constraints — that would need a
// semver implementation per ecosystem, plus Maven's ordering rules and PEP
// 440, inside a binary whose whole selling point is having no dependencies.
// Every build tool has already resolved its graph by the time it can enumerate
// it, so the resolved version is both available and the one that matters.
var rangeMarkers = []string{"^", "~", "*", "||", ">", "<", "=", "[", "]", "(", ")", ",", " ", "!", "latest."}

// IsConstraint reports whether a version string is a range or wildcard rather
// than one exact version. It errs toward "no", because an odd-but-exact
// version like "1.0.0.Final" or "2.0-20240115.RC3" must pass through.
func IsConstraint(version string) bool {
	v := strings.TrimSpace(version)
	if v == "" {
		return false
	}
	if strings.EqualFold(v, "latest") || strings.EqualFold(v, "release") {
		return true
	}
	// Gradle's dynamic versions end in "+" ("1.+", "1.2.+"). A "+" elsewhere is
	// semver build metadata ("1.0.0+build.7"), which is part of an exact
	// version, so only the trailing form counts.
	if strings.HasSuffix(v, "+") {
		return true
	}
	for _, marker := range rangeMarkers {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return false
}
