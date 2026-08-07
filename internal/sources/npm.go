package sources

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

const npmRegistry = "https://registry.npmjs.org"

// npm metadata is loosely typed in the wild: license and repository can be
// either a string or an object, so those fields decode via json.RawMessage.
type npmDoc struct {
	Description string                `json:"description"`
	DistTags    map[string]string     `json:"dist-tags"`
	Time        map[string]string     `json:"time"`
	Versions    map[string]npmVersion `json:"versions"`
	Maintainers []json.RawMessage     `json:"maintainers"`
	License     json.RawMessage       `json:"license"`
	Repository  json.RawMessage       `json:"repository"`
	Homepage    string                `json:"homepage"`
}

type npmVersion struct {
	License    json.RawMessage `json:"license"`
	Deprecated json.RawMessage `json:"deprecated"`
}

func flexibleString(raw json.RawMessage, objectKey string) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		if v, ok := obj[objectKey].(string); ok {
			return v
		}
	}
	return ""
}

func fetchNPM(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.NPM, Name: name}
	var doc npmDoc
	err := getJSON(npmRegistry+"/"+url.PathEscape(name), &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	facts.Description = doc.Description
	facts.LatestVersion = doc.DistTags["latest"]
	facts.Homepage = doc.Homepage
	facts.RepositoryURL = flexibleString(doc.Repository, "url")

	for published, stamp := range doc.Time {
		if published == "created" || published == "modified" {
			continue
		}
		ref := model.ReleaseRef{Version: published}
		if t, err := time.Parse(time.RFC3339, stamp); err == nil {
			t := t.UTC()
			ref.ReleaseDate = &t
		}
		// The packument carries deprecation per version, so a release that has
		// been withdrawn can be excluded from the gap between a pinned version
		// and the latest one. Previously only the latest version's flag was
		// read.
		ref.Yanked = isDeprecated(doc.Versions[published])
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	latest := doc.Versions[facts.LatestVersion]
	if license := flexibleString(latest.License, "type"); license != "" {
		facts.License = license
	} else {
		facts.License = flexibleString(doc.License, "type")
	}
	facts.Deprecated, facts.DeprecationMessage = deprecation(latest)
	if doc.Maintainers != nil {
		facts.MaintainerCount = model.Int(len(doc.Maintainers))
	}

	if version != "" {
		facts.Requested = npmVersionFacts(facts, doc, version)
	}
	return facts, nil
}

// npmVersionFacts reads the requested version's own license and deprecation
// out of the packument. Both are already there for every version, so this costs
// no additional request — the packument's `versions` map was simply only ever
// read at `dist-tags.latest` before.
//
// The packument enumerates every published version, and npm publishes only
// canonical semver, so a miss here is authoritative and needs no confirming
// request. A leading "v" is tolerated because people paste it.
func npmVersionFacts(facts *model.PackageFacts, doc npmDoc, version string) *model.VersionFacts {
	resolved := ""
	for _, candidate := range []string{version, strings.TrimPrefix(version, "v")} {
		if _, ok := doc.Versions[candidate]; ok {
			resolved = candidate
			break
		}
	}
	vf := requestedVersion(facts, version, resolved, nil)
	if vf.Resolved == "" {
		return vf
	}
	entry := doc.Versions[vf.Resolved]
	vf.License = flexibleString(entry.License, "type")
	vf.Deprecated, vf.DeprecationMessage = deprecation(entry)
	return vf
}

// deprecation reads npm's per-version `deprecated` field, which is a message
// string when there is one and a bare `true` when there is not.
func deprecation(entry npmVersion) (bool, string) {
	if message := flexibleString(entry.Deprecated, ""); message != "" {
		return true, message
	}
	if len(entry.Deprecated) > 0 && string(entry.Deprecated) == "true" {
		return true, ""
	}
	return false, ""
}

func isDeprecated(entry npmVersion) bool {
	yanked, _ := deprecation(entry)
	return yanked
}

func sortNewestFirst(refs []model.ReleaseRef) {
	sort.SliceStable(refs, func(i, j int) bool {
		di, dj := refs[i].ReleaseDate, refs[j].ReleaseDate
		switch {
		case di == nil:
			return false
		case dj == nil:
			return true
		default:
			return di.After(*dj)
		}
	})
}
