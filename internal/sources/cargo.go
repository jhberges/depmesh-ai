package sources

import (
	"errors"
	"net/url"
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

func fetchCargo(name string) (*model.PackageFacts, error) {
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
	// MaintainerCount stays nil: owners are a separate, rate-limited endpoint
	// (/crates/{name}/owners), and a second request per vet is not worth one
	// signal.
	return facts, nil
}
