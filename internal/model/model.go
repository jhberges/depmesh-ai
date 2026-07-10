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
)

func ParseEcosystem(value string) (Ecosystem, error) {
	switch Ecosystem(strings.ToLower(value)) {
	case NPM:
		return NPM, nil
	case PyPI:
		return PyPI, nil
	case Maven:
		return Maven, nil
	}
	return "", fmt.Errorf("unknown ecosystem %q; expected npm, pypi or maven", value)
}

// ReleaseRef is a single released version. Descendant of legacy VersionRef.
type ReleaseRef struct {
	Version     string
	ReleaseDate *time.Time // nil when the source has no date for it
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
}

func Bool(v bool) *bool { return &v }
func Int(v int) *int    { return &v }
