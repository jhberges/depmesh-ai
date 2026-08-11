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

func fetchPackagist(name, version string) (*model.PackageFacts, error) {
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

	if len(versions) > 0 {
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
		facts.Deprecated, facts.DeprecationMessage = packagistAbandonment(newest.Abandoned)
	}

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = packagistVersionFacts(facts, vendor, pkg, version, versions)
	}
	return facts, nil
}

// packagistAbandonment reads Packagist's deprecation marker: `true`, or a
// string naming the package to move to. Both mean the same thing to the
// verdict; the string is the more useful reason.
func packagistAbandonment(abandoned json.RawMessage) (bool, string) {
	if replacement := flexibleString(abandoned, ""); replacement != "" {
		return true, "abandoned on Packagist; use " + replacement + " instead"
	}
	if string(abandoned) == "true" {
		return true, "abandoned on Packagist, with no replacement named"
	}
	return false, ""
}

// packagistVersionFacts answers about one requested release out of the list
// already fetched, after expanding the deltas that hide its license and
// abandonment (see packagistExpand).
//
// The tagged releases are all in that list, so the miss path is about the
// versions that are deliberately not: Packagist keeps branch versions
// (`dev-main`, `1.x-dev`) in a separate `~dev` document, and a Composer file
// may well pin one. Asking there is what makes a denial authoritative rather
// than an artefact of which document we happened to read.
func packagistVersionFacts(facts *model.PackageFacts, vendor, pkg, version string, versions []packagistVersion) *model.VersionFacts {
	entry, found := packagistFind(versions, version)
	resolved := ""
	if found {
		resolved = entry.Version
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		var dev packagistDoc
		endpoint := packagistRepo + "/" + url.PathEscape(vendor) + "/" + url.PathEscape(pkg) + "~dev.json"
		if err := getJSON(endpoint, &dev); err != nil {
			return "", err
		}
		for _, branches := range dev.Packages {
			if candidate, ok := packagistFind(branches, version); ok {
				entry, found = candidate, true
				return candidate.Version, nil
			}
		}
		// Both documents together enumerate everything Packagist publishes for
		// this package, and both were read — so this is a real absence rather
		// than a document we did not look in.
		return "", fmt.Errorf("packagist has no version %q of %s/%s: %w", version, vendor, pkg, ErrNotFound)
	})
	if !found {
		return vf
	}
	vf.License = strings.Join(entry.License, " OR ")
	vf.Deprecated, vf.DeprecationMessage = packagistAbandonment(entry.Abandoned)
	if vf.ReleaseDate == nil {
		// Branch versions are not in facts.Releases, so their date is not
		// there to be copied.
		if when, err := time.Parse(time.RFC3339, entry.Time); err == nil {
			when := when.UTC()
			vf.ReleaseDate = &when
		}
	}
	return vf
}

// packagistFind locates a version in a minified list and returns it expanded.
func packagistFind(versions []packagistVersion, version string) (packagistVersion, bool) {
	for i, published := range versions {
		if samePackagistVersion(published.Version, version) {
			return packagistExpand(versions, i), true
		}
	}
	return packagistVersion{}, false
}

// packagistExpand undoes the delta encoding for one entry. In the minified
// format only the first entry is complete; in every later one a field is
// omitted when it has not changed from the entry before it. Reading a version's
// license straight off its own entry would therefore report "no license" for
// every release that simply kept the license it already had — which is most of
// them.
//
// Expansion runs in list order, which is newest-first, because that is the
// order Composer itself expands in. It reads oddly for `abandoned` — an older
// release inherits a marker added after it — and it is right: abandonment is a
// property of the package that Packagist writes onto every version, so pinning
// an older release does not escape it.
func packagistExpand(versions []packagistVersion, idx int) packagistVersion {
	expanded := versions[idx]
	for i := idx - 1; i >= 0; i-- {
		if expanded.License == nil {
			expanded.License = versions[i].License
		}
		if expanded.Abandoned == nil {
			expanded.Abandoned = versions[i].Abandoned
		}
		if expanded.Description == "" {
			expanded.Description = versions[i].Description
		}
	}
	return expanded
}

// samePackagistVersion compares a requested spelling against a published one.
// Packagist reports the tag verbatim, so the same release is "1.2.0" in one
// project and "v1.2.0" in the next, and a lockfile may say either.
func samePackagistVersion(published, requested string) bool {
	if strings.EqualFold(published, requested) {
		return true
	}
	trim := func(v string) string { return strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V") }
	return strings.EqualFold(trim(published), trim(requested))
}
