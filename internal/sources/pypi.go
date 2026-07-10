package sources

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

const pypiRegistry = "https://pypi.org/pypi"

type pypiDoc struct {
	Info struct {
		Summary           string            `json:"summary"`
		Version           string            `json:"version"`
		License           string            `json:"license"`
		LicenseExpression string            `json:"license_expression"`
		Classifiers       []string          `json:"classifiers"`
		HomePage          string            `json:"home_page"`
		ProjectURLs       map[string]string `json:"project_urls"`
	} `json:"info"`
	Releases map[string][]pypiFile `json:"releases"`
}

type pypiFile struct {
	UploadTimeISO string `json:"upload_time_iso_8601"`
	UploadTime    string `json:"upload_time"`
	Yanked        bool   `json:"yanked"`
}

func fetchPyPI(name string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.PyPI, Name: name}
	var doc pypiDoc
	err := getJSON(pypiRegistry+"/"+url.PathEscape(name)+"/json", &doc)
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	info := doc.Info
	facts.Description = info.Summary
	facts.LatestVersion = info.Version
	facts.Homepage = firstNonEmpty(info.HomePage, info.ProjectURLs["Homepage"])
	facts.RepositoryURL = firstNonEmpty(info.ProjectURLs["Source"], info.ProjectURLs["Repository"])

	license := firstNonEmpty(info.LicenseExpression, info.License)
	if license == "" {
		for _, classifier := range info.Classifiers {
			if strings.HasPrefix(classifier, "License ::") {
				parts := strings.Split(classifier, "::")
				license = strings.TrimSpace(parts[len(parts)-1])
				break
			}
		}
	}
	// Some projects paste the entire license text into the license field.
	if idx := strings.IndexByte(license, '\n'); idx >= 0 {
		license = license[:idx]
	}
	if len(license) > 120 {
		license = license[:120]
	}
	facts.License = license

	for version, files := range doc.Releases {
		ref := model.ReleaseRef{Version: version}
		if earliest := earliestUpload(files); earliest != nil {
			ref.ReleaseDate = earliest
		}
		facts.Releases = append(facts.Releases, ref)
		if version == facts.LatestVersion && len(files) > 0 && allYanked(files) {
			facts.Deprecated = true
			facts.DeprecationMessage = "latest release is yanked on PyPI"
		}
	}
	sortNewestFirst(facts.Releases)
	return facts, nil
}

func earliestUpload(files []pypiFile) *time.Time {
	var earliest *time.Time
	for _, f := range files {
		stamp := firstNonEmpty(f.UploadTimeISO, f.UploadTime)
		if stamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			// upload_time comes without a zone, e.g. "2021-03-01T07:38:00"
			t, err = time.Parse("2006-01-02T15:04:05", stamp)
			if err != nil {
				continue
			}
		}
		t = t.UTC()
		if earliest == nil || t.Before(*earliest) {
			earliest = &t
		}
	}
	return earliest
}

func allYanked(files []pypiFile) bool {
	for _, f := range files {
		if !f.Yanked {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
