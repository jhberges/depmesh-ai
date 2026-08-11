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
	NPM       Ecosystem = "npm"
	PyPI      Ecosystem = "pypi"
	Maven     Ecosystem = "maven"
	NuGet     Ecosystem = "nuget"
	Cargo     Ecosystem = "cargo"
	Go        Ecosystem = "go"
	Packagist Ecosystem = "packagist"
	Pub       Ecosystem = "pub"
	Hex       Ecosystem = "hex"
)

// Ecosystems is every ecosystem depmesh-ai can vet, oldest support first.
// It is the single list the rest of the tool reads from: ParseEcosystem
// accepts exactly these, the CLI usage and the MCP tool schema render them,
// and adding an ecosystem means appending here rather than remembering four
// separate places that drifted apart.
var Ecosystems = []Ecosystem{NPM, PyPI, Maven, NuGet, Cargo, Go, Packagist, Pub, Hex}

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
