package sources

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// NuGet source (api.nuget.org), covering C# and the rest of .NET.
//
// The endpoint is the *semver2* registration resource, not `registration5-semver1`.
// Both answer existence correctly and both carry per-version dates, but only
// semver2 carries `deprecation` — verified against Microsoft.Bcl, which is
// deprecated and shows nothing at all on semver1. Deprecation is a 50-point
// signal, so reading the resource that omits it would leave it permanently
// dark. semver2 documents are served gzipped with a proper `Content-Encoding`
// header, which net/http transparently decodes because `get` does not set
// Accept-Encoding itself.
//
// The flat-container resource (`/v3-flatcontainer/{id}/index.json`) is cheaper
// and also answers existence, but returns versions without dates — not enough
// for the staleness, package-age or release-pace signals.
//
// A var, not a const, so the tests can point it at an httptest server: these
// adapters are all HTTP shape-readers, and the shapes worth pinning are the
// ones a live registry will not produce on demand — a deprecated package, an
// unlisted version, a 503.
var nugetRegistration = "https://api.nuget.org/v3/registration5-gz-semver2"

// maxNuGetPages bounds the request budget for one vet. The registration index
// splits version history into pages of 64, and inlines them for packages small
// enough — Newtonsoft.Json's 84 versions arrive in the index itself, for one
// request total. Widely-used packages do not: Serilog has 10 pages, each its
// own document, and fetching all of them for one verdict is neither fast nor
// polite.
//
// So the budget goes where the signals are. selectNuGetPages spends it on the
// newest pages, which decide LatestVersion, staleness and recent cadence, and
// reserves one for the oldest page, which decides the first-publication date
// behind the young-package signal. What is left out is middle history: the
// release count and the all-time average pace under-report, and both are
// reported rather than scored. The gap is named in Degraded.
const maxNuGetPages = 4

type nugetIndex struct {
	Items []nugetPage `json:"items"`
}

// A page is either inlined in the index (Items populated) or a reference to a
// document that has to be fetched from ID.
type nugetPage struct {
	ID    string      `json:"@id"`
	Items []nugetLeaf `json:"items"`
}

type nugetLeaf struct {
	CatalogEntry nugetEntry `json:"catalogEntry"`
}

type nugetEntry struct {
	Version           string `json:"version"`
	Published         string `json:"published"`
	LicenseExpression string `json:"licenseExpression"`
	Authors           string `json:"authors"`
	Description       string `json:"description"`
	ProjectURL        string `json:"projectUrl"`
	// Listed is a pointer because absent means listed; only an explicit false
	// hides a version.
	Listed      *bool `json:"listed"`
	Deprecation *struct {
		Message string   `json:"message"`
		Reasons []string `json:"reasons"`
	} `json:"deprecation"`
}

func (e nugetEntry) listed() bool { return e.Listed == nil || *e.Listed }

// prerelease is NuGet's own rule: a SemVer pre-release label after a hyphen.
func (e nugetEntry) prerelease() bool { return strings.Contains(e.Version, "-") }

func fetchNuGet(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.NuGet, Name: name}

	// Package ids are flat and case-insensitive; the URL wants them lowercased.
	var index nugetIndex
	indexURL := nugetRegistration + "/" + url.PathEscape(strings.ToLower(name)) + "/index.json"
	err := getJSON(indexURL, &index)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}
	facts.Exists = model.Bool(true)

	for _, i := range selectNuGetPages(index.Items, maxNuGetPages) {
		var page nugetPage
		// A page that will not load costs history, not correctness: it leaves
		// the same hole as one the budget did not reach, and is reported the
		// same way. It must not fail the vet.
		if err := getJSON(index.Items[i].ID, &page); err != nil {
			continue
		}
		index.Items[i].Items = page.Items
	}
	unread := 0
	for _, page := range index.Items {
		if page.Items == nil {
			unread++
		}
	}
	if unread > 0 {
		facts.Degraded = append(facts.Degraded,
			"nuget registration (release dates for older versions not fetched)")
	}

	// Pages are ordered oldest-first and so are the entries within them, so
	// the last listed entry overall is the newest version.
	var newest, newestStable *nugetEntry
	seen := map[string]*nugetEntry{}
	for _, page := range index.Items {
		for _, leaf := range page.Items {
			entry := leaf.CatalogEntry
			if !entry.listed() {
				// Unlisted versions are hidden from search and resolution;
				// letting them drive the pace metric would credit a package
				// for releases nobody can install.
				continue
			}
			ref := model.ReleaseRef{Version: entry.Version}
			if published, err := time.Parse(time.RFC3339, entry.Published); err == nil {
				published := published.UTC()
				ref.ReleaseDate = &published
			}
			// Per-version deprecation, read off the entry we already have.
			ref.Yanked = entry.Deprecation != nil
			facts.Releases = append(facts.Releases, ref)
			seen[entry.Version] = &leaf.CatalogEntry

			newest = &leaf.CatalogEntry
			if !entry.prerelease() {
				newestStable = &leaf.CatalogEntry
			}
		}
	}
	sortNewestFirst(facts.Releases)

	// Prefer the newest stable version, so a pre-release does not make a
	// healthy package look like it has an unreleased latest.
	latest := newestStable
	if latest == nil {
		latest = newest
	}
	if latest != nil {
		facts.LatestVersion = latest.Version
		facts.License = latest.LicenseExpression
		facts.Description = latest.Description
		facts.Homepage = latest.ProjectURL
		if latest.Deprecation != nil {
			facts.Deprecated = true
			facts.DeprecationMessage = nugetDeprecationMessage(latest)
		}
	}
	// MaintainerCount stays nil: `authors` is a free-text display string
	// ("James Newton-King"), not a list, so any count read out of it would be
	// a guess feeding a bus-factor penalty.

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = nugetVersionFacts(facts, name, version, seen, unread > 0)
	}
	return facts, nil
}

func nugetDeprecationMessage(entry *nugetEntry) string {
	return firstNonEmpty(entry.Deprecation.Message, strings.Join(entry.Deprecation.Reasons, ", "))
}

// nugetVersionFacts reads the requested version's own license and deprecation
// off the catalog entry, when the page carrying it was one of the ones we
// fetched.
//
// The page budget is why this needs a confirming request at all. Unlike npm's
// packument, the entries in hand are deliberately *not* a complete enumeration:
// middle history is skipped to keep one vet to a handful of requests. A version
// missing from them may simply live on a page nobody read, so "not in the pages
// we fetched" is never grounds for saying it does not exist — the registration
// leaf for that one version is.
func nugetVersionFacts(facts *model.PackageFacts, name, version string, seen map[string]*nugetEntry, pagesMissing bool) *model.VersionFacts {
	resolved := ""
	if _, ok := seen[version]; ok {
		resolved = version
	} else {
		// NuGet ids and versions are case-insensitive, so a lockfile's casing
		// need not match the catalog's.
		for candidate := range seen {
			if strings.EqualFold(candidate, version) {
				resolved = candidate
				break
			}
		}
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		leaf := nugetRegistration + "/" + url.PathEscape(strings.ToLower(name)) +
			"/" + url.PathEscape(strings.ToLower(version)) + ".json"
		var found struct{}
		if err := getJSON(leaf, &found); err != nil {
			return "", err
		}
		return version, nil
	})
	entry, ok := seen[vf.Resolved]
	if !ok {
		// Confirmed to exist but on a page the budget skipped: its license and
		// deprecation are unknown rather than absent, and the same gap is
		// already named in Degraded.
		if vf.Exists != nil && *vf.Exists && pagesMissing {
			facts.Degraded = append(facts.Degraded,
				"nuget registration (license and deprecation for "+version+" not fetched)")
		}
		return vf
	}
	vf.License = entry.LicenseExpression
	if entry.Deprecation != nil {
		vf.Deprecated = true
		vf.DeprecationMessage = nugetDeprecationMessage(entry)
	}
	return vf
}

// selectNuGetPages returns the indices of pages that must be fetched, spending
// a budget of at most n requests. Inlined pages cost nothing and are never
// returned. Of the rest it takes the newest, and reserves the last slot of the
// budget for the oldest page so the first-publication date stays honest.
func selectNuGetPages(pages []nugetPage, n int) []int {
	if n < 1 {
		return nil
	}
	var missing []int
	for i, page := range pages {
		if page.Items == nil {
			missing = append(missing, i)
		}
	}
	if len(missing) <= n {
		return missing
	}
	selected := append([]int{missing[0]}, missing[len(missing)-(n-1):]...)
	return selected
}
