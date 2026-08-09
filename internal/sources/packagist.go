package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Packagist source, covering PHP. Names are namespaced `vendor/package`, the
// way Maven coordinates are `groupId:artifactId`, and a bare name is rejected
// rather than guessed at.
//
// The p2 metadata endpoint returns every version with a date in one
// unauthenticated request. deps.dev does not cover packagist, so the advisory
// signal stays absent — see fetchPackagist for what the response does offer.
var packagistRepo = "https://repo.packagist.org/p2"

type packagistDoc struct {
	// Minified says the version list is delta-encoded (see packagistVersions).
	Minified string                        `json:"minified"`
	Packages map[string][]packagistVersion `json:"packages"`
}

type packagistVersion struct {
	Version     string          `json:"version"`
	Time        string          `json:"time"`
	License     []string        `json:"license"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage"`
	Authors     []struct{}      `json:"authors"`
	Abandoned   json.RawMessage `json:"abandoned"`
	Source      struct {
		URL string `json:"url"`
	} `json:"source"`
}

func fetchPackagist(name string) (*model.PackageFacts, error) {
	vendor, pkg, ok := strings.Cut(name, "/")
	if !ok || vendor == "" || pkg == "" || strings.Contains(pkg, "/") {
		return nil, fmt.Errorf("packagist package names must be 'vendor/package', got %q", name)
	}
	facts := &model.PackageFacts{Ecosystem: model.Packagist, Name: name}

	var doc packagistDoc
	endpoint := packagistRepo + "/" + url.PathEscape(vendor) + "/" + url.PathEscape(pkg) + ".json"
	err := getJSON(endpoint, &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}
	facts.Exists = model.Bool(true)

	versions := doc.Packages[name]
	if len(versions) == 0 {
		// The key is the canonical casing, which may differ from what was
		// asked for; there is only ever one.
		for _, only := range doc.Packages {
			versions = only
			break
		}
	}
	for _, version := range versions {
		ref := model.ReleaseRef{Version: version.Version}
		if when, err := time.Parse(time.RFC3339, version.Time); err == nil {
			when := when.UTC()
			ref.ReleaseDate = &when
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)
	if len(versions) == 0 {
		return facts, nil
	}

	// The list is newest-first and delta-encoded ("minified": composer/2.0):
	// after the first entry, a field is omitted when it is unchanged. The
	// first entry is therefore the only complete one — which is also the
	// newest, and the one every package-level fact should come from.
	newest := versions[0]
	facts.LatestVersion = newest.Version
	facts.License = strings.Join(newest.License, " OR ")
	facts.Description = newest.Description
	facts.Homepage = newest.Homepage
	facts.RepositoryURL = newest.Source.URL
	if len(newest.Authors) > 0 {
		facts.MaintainerCount = model.Int(len(newest.Authors))
	}
	// `abandoned` is Packagist's deprecation marker: true, or a string naming
	// the package to move to. Both mean the same thing to the verdict; the
	// string is the more useful reason.
	if replacement := flexibleString(newest.Abandoned, ""); replacement != "" {
		facts.Deprecated = true
		facts.DeprecationMessage = "abandoned on Packagist; use " + replacement + " instead"
	} else if string(newest.Abandoned) == "true" {
		facts.Deprecated = true
		facts.DeprecationMessage = "abandoned on Packagist, with no replacement named"
	}
	return facts, nil
}
