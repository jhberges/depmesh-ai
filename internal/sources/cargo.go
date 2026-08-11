package sources

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// crates.io source, covering Rust.
//
// The most convenient API of the lot: one unauthenticated request returns the
// crate, every version, and a date for each — serde answers with 316 of them,
// unpaginated. crates.io asks callers for a descriptive User-Agent, which
// `sources.get` already sends.
var cratesRegistry = "https://crates.io/api/v1/crates"

type cargoDoc struct {
	Crate struct {
		MaxStableVersion string `json:"max_stable_version"`
		NewestVersion    string `json:"newest_version"`
		Description      string `json:"description"`
		Homepage         string `json:"homepage"`
		Repository       string `json:"repository"`
		// Yanked is crate-level: every version has been withdrawn.
		Yanked bool `json:"yanked"`
	} `json:"crate"`
	Versions []cargoVersion `json:"versions"`
}

type cargoVersion struct {
	Num         string `json:"num"`
	CreatedAt   string `json:"created_at"`
	License     string `json:"license"`
	Yanked      bool   `json:"yanked"`
	YankMessage string `json:"yank_message"`
}

func fetchCargo(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.Cargo, Name: name}
	var doc cargoDoc
	err := getJSON(cratesRegistry+"/"+url.PathEscape(name), &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	facts.Description = doc.Crate.Description
	facts.Homepage = doc.Crate.Homepage
	facts.RepositoryURL = doc.Crate.Repository

	// A yank withdraws a version from resolution without deleting it. It is a
	// per-version retraction, not a deprecation — a maintainer pulling a bad
	// 1.0.5 and leaving 1.0.4 in place has not abandoned the crate, so
	// mapping every yank onto Deprecated would be wrong. What a yank *does*
	// mean is that nobody can resolve that version, so counting it would
	// credit the crate for a release nobody can install: yanked versions stay
	// out of Releases and out of the pace metric.
	byVersion := map[string]cargoVersion{}
	for _, version := range doc.Versions {
		byVersion[version.Num] = version
		if version.Yanked {
			continue
		}
		ref := model.ReleaseRef{Version: version.Num}
		if created, err := time.Parse(time.RFC3339, version.CreatedAt); err == nil {
			created := created.UTC()
			ref.ReleaseDate = &created
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	// Prefer the newest stable, so a pre-release does not stand in for what a
	// reader would actually add to their Cargo.toml. Fall back through the
	// newest version to whatever survived the yanks.
	for _, candidate := range []string{doc.Crate.MaxStableVersion, doc.Crate.NewestVersion} {
		if version, ok := byVersion[candidate]; ok && !version.Yanked {
			facts.LatestVersion = candidate
			break
		}
	}
	if facts.LatestVersion == "" && len(facts.Releases) > 0 {
		facts.LatestVersion = facts.Releases[0].Version
	}
	facts.License = byVersion[facts.LatestVersion].License

	// A crate whose every version is withdrawn *is* deprecation-shaped: there
	// is nothing left to resolve. That is the only yank state that earns the
	// deprecation penalty.
	if doc.Crate.Yanked || len(facts.Releases) == 0 {
		facts.Deprecated = true
		facts.DeprecationMessage = firstNonEmpty(
			byVersion[doc.Crate.NewestVersion].YankMessage,
			"every published version has been yanked from crates.io",
		)
	}

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = cargoVersionFacts(facts, name, version, byVersion)
	}
	// MaintainerCount stays nil: owners are a separate, rate-limited endpoint
	// (/crates/{name}/owners), and a second request per vet is not worth one
	// signal.
	return facts, nil
}

// cargoVersionFacts answers about one requested release. Everything it needs is
// in the document already fetched — crates.io returns every version with its
// date, license and yank state — so the common path costs no extra request.
//
// The yanked versions are the reason this reads from byVersion rather than from
// facts.Releases: a yank keeps a version out of the release history on purpose,
// and a caller pinning a yanked version is exactly the case that most needs an
// answer.
func cargoVersionFacts(facts *model.PackageFacts, name, version string, byVersion map[string]cargoVersion) *model.VersionFacts {
	resolved := ""
	// crates.io publishes canonical semver, so the only spelling that varies is
	// a leading "v" people paste out of a tag name.
	for _, candidate := range []string{version, strings.TrimPrefix(version, "v")} {
		if _, ok := byVersion[candidate]; ok {
			resolved = candidate
			break
		}
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		// The versions in hand come from the crate response, which crates.io
		// has been moving toward paginating. If a spelling misses there, the
		// per-version endpoint is authoritative about that one release, and it
		// is what keeps a paged-out version from being called a hallucination.
		var one struct {
			Version cargoVersion `json:"version"`
		}
		if err := getJSON(cratesRegistry+"/"+url.PathEscape(name)+"/"+url.PathEscape(version), &one); err != nil {
			return "", err
		}
		byVersion[firstNonEmpty(one.Version.Num, version)] = one.Version
		return firstNonEmpty(one.Version.Num, version), nil
	})
	entry, known := byVersion[vf.Resolved]
	if !known {
		return vf
	}
	vf.License = entry.License
	if entry.Yanked {
		// A yanked version is not in facts.Releases, so requestedVersion found
		// neither its date nor its yank state there.
		vf.Yanked = true
		vf.DeprecationMessage = firstNonEmpty(entry.YankMessage, "yanked from crates.io")
	}
	if vf.ReleaseDate == nil {
		if created, err := time.Parse(time.RFC3339, entry.CreatedAt); err == nil {
			created := created.UTC()
			vf.ReleaseDate = &created
		}
	}
	return vf
}
