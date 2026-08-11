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
	Retirements map[string]struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"retirements"`
}

func fetchHex(name string) (*model.PackageFacts, error) {
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
		facts.DeprecationMessage = strings.TrimSpace(
			firstNonEmpty(retirement.Message, "") + " (" + firstNonEmpty(retirement.Reason, "retired") + ")")
	} else if len(doc.Releases) > 0 && len(doc.Retirements) >= len(doc.Releases) {
		facts.Deprecated = true
		facts.DeprecationMessage = "every published release has been retired on Hex"
	}
	return facts, nil
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
