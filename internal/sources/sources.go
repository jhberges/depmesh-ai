package sources

import (
	"errors"
	"fmt"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Gather fetches authoritative facts from the package's own registry, then
// optionally enriches them from deps.dev.
//
// version, when non-empty, asks about one specific release as well as the
// package. It must be an exact version: constraints are the caller's to
// resolve (see model.IsConstraint).
func Gather(ecosystem model.Ecosystem, name, version string, enrich bool) (*model.PackageFacts, error) {
	if version != "" && model.IsConstraint(version) {
		return nil, fmt.Errorf(
			"%q is a version constraint, not a version; resolve it first and pass the exact version", version)
	}
	var facts *model.PackageFacts
	var err error
	switch ecosystem {
	case model.NPM:
		facts, err = fetchNPM(name, version)
	case model.PyPI:
		facts, err = fetchPyPI(name, version)
	case model.Maven:
		facts, err = fetchMaven(name, version)
	case model.NuGet:
		facts, err = fetchNuGet(name, version)
	case model.Cargo:
		facts, err = fetchCargo(name, version)
	case model.Go:
		facts, err = fetchGo(name, version)
	case model.Packagist:
		facts, err = fetchPackagist(name, version)
	case model.Pub:
		facts, err = fetchPub(name, version)
	case model.Hex:
		facts, err = fetchHex(name, version)
	}
	if err != nil {
		return nil, err
	}
	if enrich {
		enrichDepsDev(facts)
	}
	return facts, nil
}

// requestedVersion builds the per-version record for a caller-named version.
//
// resolved is the registry's own spelling of that release, or "" when the
// version list already in hand does not contain it. A miss is not proof of
// absence — a list can be stale, and a spelling can be normalized — so the miss
// path calls confirm, which asks the registry about that one version directly.
// ErrNotFound from confirm is the only thing that sets Exists=false; any other
// failure leaves existence unknown and records a degraded source.
//
// confirm may be nil where the enumeration we already have is itself
// authoritative and complete, as npm's packument is.
func requestedVersion(facts *model.PackageFacts, requested, resolved string, confirm func() (string, error)) *model.VersionFacts {
	vf := &model.VersionFacts{Version: requested, Resolved: resolved}

	if model.IsSnapshot(requested) {
		// Release metadata legitimately omits snapshots, so its silence about
		// one is not evidence. Existence stays unknown, and the verdict says
		// that a snapshot cannot be vetted at all.
		vf.Snapshot = true
		return vf
	}

	if resolved == "" {
		if confirm == nil {
			vf.Exists = model.Bool(false)
			return vf
		}
		confirmed, err := confirm()
		switch {
		case errors.Is(err, ErrNotFound):
			vf.Exists = model.Bool(false)
			return vf
		case err != nil:
			facts.Degraded = append(facts.Degraded,
				fmt.Sprintf("per-version lookup for %s (existence unconfirmed)", requested))
			return vf
		}
		resolved = confirmed
		vf.Resolved = confirmed
	}

	vf.Exists = model.Bool(true)
	vf.IsLatest = resolved == facts.LatestVersion
	if idx := indexOfRelease(facts.Releases, resolved); idx >= 0 {
		vf.ReleaseDate = facts.Releases[idx].ReleaseDate
		vf.Yanked = facts.Releases[idx].Yanked
	}
	return vf
}

func indexOfRelease(refs []model.ReleaseRef, version string) int {
	for i := range refs {
		if refs[i].Version == version {
			return i
		}
	}
	return -1
}
