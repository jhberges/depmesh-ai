package sources

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
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

func fetchNPM(name string) (*model.PackageFacts, error) {
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

	for version, stamp := range doc.Time {
		if version == "created" || version == "modified" {
			continue
		}
		ref := model.ReleaseRef{Version: version}
		if t, err := time.Parse(time.RFC3339, stamp); err == nil {
			t := t.UTC()
			ref.ReleaseDate = &t
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	latest := doc.Versions[facts.LatestVersion]
	if license := flexibleString(latest.License, "type"); license != "" {
		facts.License = license
	} else {
		facts.License = flexibleString(doc.License, "type")
	}
	if deprecation := flexibleString(latest.Deprecated, ""); deprecation != "" {
		facts.Deprecated = true
		facts.DeprecationMessage = deprecation
	} else if len(latest.Deprecated) > 0 && string(latest.Deprecated) == "true" {
		facts.Deprecated = true
	}
	if doc.Maintainers != nil {
		facts.MaintainerCount = model.Int(len(doc.Maintainers))
	}
	return facts, nil
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
