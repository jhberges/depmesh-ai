package sources

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Hex source, covering Elixir *and* Erlang: rebar3 resolves from Hex by
// default, so both languages are served by this one adapter and neither needs
// a git or GitHub-based check — which would run into the 60-requests-an-hour
// unauthenticated GitHub limit that keeps the Go adapter on the module proxy.
//
// The richest single response of any registry here. One unauthenticated
// request to phoenix returns 176 dated releases, the licenses, the retirement
// map and — uniquely outside npm and Packagist — the owner list, which is what
// the bus-factor signal needs.
//
// deps.dev does not cover hex, so the advisory signal is absent.
var hexRegistry = "https://hex.pm/api/packages"

type hexDoc struct {
	HTMLURL             string `json:"html_url"`
	LatestVersion       string `json:"latest_version"`
	LatestStableVersion string `json:"latest_stable_version"`
	Meta                struct {
		Description string            `json:"description"`
		Licenses    []string          `json:"licenses"`
		Links       map[string]string `json:"links"`
	} `json:"meta"`
	Owners   []struct{} `json:"owners"`
	Releases []struct {
		Version    string `json:"version"`
		InsertedAt string `json:"inserted_at"`
	} `json:"releases"`
	Retirements map[string]hexRetirement `json:"retirements"`
}

type hexRetirement struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// hexReleaseDoc is the per-version resource. It exists for two things the
// package document cannot give: it is authoritative about one version, and it
// carries that version's own licenses.
type hexReleaseDoc struct {
	Version string `json:"version"`
	Meta    struct {
		Licenses []string `json:"licenses"`
	} `json:"meta"`
}

func fetchHex(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.Hex, Name: name}
	var doc hexDoc
	err := getJSON(hexRegistry+"/"+url.PathEscape(name), &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	facts.Description = doc.Meta.Description
	facts.License = strings.Join(doc.Meta.Licenses, " OR ")
	facts.RepositoryURL = hexLink(doc.Meta.Links, "github", "source", "repository")
	facts.Homepage = firstNonEmpty(
		hexLink(doc.Meta.Links, "website", "homepage"),
		doc.HTMLURL,
	)
	if len(doc.Owners) > 0 {
		facts.MaintainerCount = model.Int(len(doc.Owners))
	}

	// Retired versions stay in Releases. Unlike a crates.io yank or a pub.dev
	// retraction, a Hex retirement does not withdraw the version from
	// resolution — Mix still installs it and prints a warning — so it is a
	// release that happened and that people are still running, and dropping it
	// would understate the history.
	for _, release := range doc.Releases {
		ref := model.ReleaseRef{Version: release.Version}
		if when, err := time.Parse(time.RFC3339, release.InsertedAt); err == nil {
			when := when.UTC()
			ref.ReleaseDate = &when
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	facts.LatestVersion = firstNonEmpty(doc.LatestStableVersion, doc.LatestVersion)
	if facts.LatestVersion == "" && len(facts.Releases) > 0 {
		facts.LatestVersion = facts.Releases[0].Version
	}

	// What a retirement means depends on which version carries it. An old
	// release marked "invalid" is a retraction — plug_cowboy has two, with a
	// healthy 2.9.0 on top — and says nothing about adopting the package
	// today. The current release being marked is the maintainers telling you
	// not to use what they are shipping, which is what Deprecated is for.
	if retirement, retired := doc.Retirements[facts.LatestVersion]; retired {
		facts.Deprecated = true
		facts.DeprecationMessage = hexRetirementMessage(retirement)
	} else if len(doc.Releases) > 0 && len(doc.Retirements) >= len(doc.Releases) {
		facts.Deprecated = true
		facts.DeprecationMessage = "every published release has been retired on Hex"
	}

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = hexVersionFacts(facts, doc, name, version)
	}
	return facts, nil
}

// hexVersionFacts answers about one requested release.
//
// Retirement is already in hand for every version — the package document
// carries the whole map — so the only thing worth a request is the version's
// own licenses, which is the signal that says a pin permits something different
// from what the latest release permits. That request doubles as the
// authoritative existence check on a spelling we failed to match, so the miss
// path costs nothing extra, and it fails soft: an unreachable release document
// leaves the license empty, exactly as if it had never been asked.
func hexVersionFacts(facts *model.PackageFacts, doc hexDoc, name, version string) *model.VersionFacts {
	resolved := ""
	// Hex requires canonical semver, so the only spelling that varies is a
	// leading "v" people paste out of a tag name.
	for _, published := range doc.Releases {
		if published.Version == version || published.Version == strings.TrimPrefix(version, "v") {
			resolved = published.Version
			break
		}
	}

	var release *hexReleaseDoc
	fetch := func(spelling string) (*hexReleaseDoc, error) {
		var one hexReleaseDoc
		endpoint := hexRegistry + "/" + url.PathEscape(name) + "/releases/" + url.PathEscape(spelling)
		if err := getJSON(endpoint, &one); err != nil {
			return nil, err
		}
		return &one, nil
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		one, err := fetch(version)
		if err != nil {
			return "", err
		}
		release = one
		return firstNonEmpty(one.Version, version), nil
	})
	if vf.Exists == nil || !*vf.Exists {
		return vf
	}

	// A retirement is a maintainer saying not to use this release. It does not
	// withdraw it from resolution — Mix still installs it, with a warning — so
	// it reads as a deprecation of this version rather than as a yank.
	if retirement, retired := doc.Retirements[vf.Resolved]; retired {
		vf.Deprecated = true
		vf.DeprecationMessage = hexRetirementMessage(retirement)
	}

	if release == nil {
		release, _ = fetch(vf.Resolved)
	}
	if release != nil {
		vf.License = strings.Join(release.Meta.Licenses, " OR ")
	}
	return vf
}

func hexRetirementMessage(retirement hexRetirement) string {
	return strings.TrimSpace(
		firstNonEmpty(retirement.Message, "") + " (" + firstNonEmpty(retirement.Reason, "retired") + ")")
}

// hexLink reads meta.links, whose keys are whatever the maintainer typed
// ("github", "GitHub", "Website"), so they are matched case-insensitively.
func hexLink(links map[string]string, names ...string) string {
	for _, name := range names {
		for key, value := range links {
			if strings.EqualFold(key, name) && value != "" {
				return value
			}
		}
	}
	return ""
}
