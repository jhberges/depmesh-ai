package sources

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// pub.dev source, covering Dart and — through Flutter — a large share of
// cross-platform mobile work, which gives it a bigger real footprint than the
// language rankings suggest.
//
// deps.dev does not cover pub, so advisories stay absent.
var pubRegistry = "https://pub.dev/api/packages"

type pubDoc struct {
	Latest   pubVersion   `json:"latest"`
	Versions []pubVersion `json:"versions"`
}

type pubVersion struct {
	Version   string `json:"version"`
	Published string `json:"published"`
	// Retracted is absent rather than false on a healthy version.
	Retracted bool `json:"retracted"`
	Pubspec   struct {
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
		Repository  string `json:"repository"`
	} `json:"pubspec"`
}

// pubScore is the analysis endpoint. It is a second request, and it exists
// here for one reason: the package response carries no license at all, and a
// missing license costs -15 — on every Dart package there is, forever. One
// extra call to turn a permanent ecosystem-wide penalty into a real answer is
// a trade worth making, and it fails soft: an unreachable score endpoint just
// leaves the license empty, exactly as if it had never been asked.
type pubScore struct {
	Tags []string `json:"tags"`
}

// Tags that sit in the license: namespace without naming a license.
var pubLicenseClassifiers = map[string]bool{
	"fsf-libre": true, "osi-approved": true, "unknown": true,
}

func fetchPub(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.Pub, Name: name}
	var doc pubDoc
	escaped := url.PathEscape(name)
	err := getJSON(pubRegistry+"/"+escaped, &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	facts.Description = doc.Latest.Pubspec.Description
	facts.Homepage = doc.Latest.Pubspec.Homepage
	facts.RepositoryURL = doc.Latest.Pubspec.Repository

	// A retraction withdraws a version from resolution without deleting it —
	// the same shape as a crates.io yank, and settled the same way: retracted
	// versions stay out of the pace metric, and only a package with nothing
	// left to resolve is deprecation-shaped. They are still indexed, because a
	// caller pinning a retracted version is the case that most needs an answer.
	byVersion := map[string]pubVersion{}
	for _, published := range doc.Versions {
		byVersion[published.Version] = published
		if published.Retracted {
			continue
		}
		ref := model.ReleaseRef{Version: published.Version}
		if when, err := time.Parse(time.RFC3339, published.Published); err == nil {
			when := when.UTC()
			ref.ReleaseDate = &when
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	switch {
	case !doc.Latest.Retracted && doc.Latest.Version != "":
		facts.LatestVersion = doc.Latest.Version
	case len(facts.Releases) > 0:
		facts.LatestVersion = facts.Releases[0].Version
	default:
		facts.Deprecated = true
		facts.DeprecationMessage = "every published version has been retracted from pub.dev"
	}

	facts.License = pubLicense(escaped)
	// MaintainerCount stays nil: pub.dev names a publisher, which is one
	// identity however many people stand behind it, so a bus-factor read out
	// of it would be a guess.

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = pubVersionFacts(facts, escaped, version, byVersion)
	}
	return facts, nil
}

// pubVersionFacts answers about one requested release out of the document
// already fetched: pub.dev returns every version, retracted ones included, with
// a date for each.
//
// The per-version license is left empty. pub.dev publishes no license field at
// all — the package-level one is read out of the analysis tags, which describe
// the latest release only, and there is no per-version equivalent short of
// unpacking the archive. An absent per-version license is scored as our gap
// rather than the package's, which is the honest reading here.
func pubVersionFacts(facts *model.PackageFacts, escapedName, version string, byVersion map[string]pubVersion) *model.VersionFacts {
	resolved := ""
	// pub.dev requires canonical semver, so the only spelling that varies is a
	// leading "v" people paste out of a tag name.
	for _, candidate := range []string{version, strings.TrimPrefix(version, "v")} {
		if _, ok := byVersion[candidate]; ok {
			resolved = candidate
			break
		}
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		// The version list is complete, so this only ever runs on a spelling
		// we failed to match. Asking the endpoint that is authoritative about
		// one version is still cheaper than the cost of the wrong answer.
		var one pubVersion
		if err := getJSON(pubRegistry+"/"+escapedName+"/versions/"+url.PathEscape(version), &one); err != nil {
			return "", err
		}
		byVersion[firstNonEmpty(one.Version, version)] = one
		return firstNonEmpty(one.Version, version), nil
	})
	entry, known := byVersion[vf.Resolved]
	if !known {
		return vf
	}
	if entry.Retracted {
		// A retracted version is not in facts.Releases, so requestedVersion
		// found neither its date nor its status there.
		vf.Yanked = true
		vf.DeprecationMessage = "retracted from pub.dev"
	}
	if vf.ReleaseDate == nil {
		if when, err := time.Parse(time.RFC3339, entry.Published); err == nil {
			when := when.UTC()
			vf.ReleaseDate = &when
		}
	}
	return vf
}

// pubLicense reads the license out of the analysis tags, e.g. "license:mit".
// The namespace also carries classifiers ("license:osi-approved") that say
// something about the license without naming one.
func pubLicense(escapedName string) string {
	var score pubScore
	if err := getJSON(pubRegistry+"/"+escapedName+"/score", &score); err != nil {
		return ""
	}
	for _, tag := range score.Tags {
		license, ok := strings.CutPrefix(tag, "license:")
		if !ok || pubLicenseClassifiers[license] {
			continue
		}
		return strings.ToUpper(license)
	}
	return ""
}
