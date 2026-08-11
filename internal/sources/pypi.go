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

func fetchPyPI(name, version string) (*model.PackageFacts, error) {
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

	// PyPI normalizes versions (PEP 440), so the key it publishes need not be
	// the spelling a caller has in a lockfile. Index by normalized key as we go
	// and the requested-version lookup costs nothing.
	byKey := make(map[string]string, len(doc.Releases))
	for released, files := range doc.Releases {
		ref := model.ReleaseRef{Version: released}
		if earliest := earliestUpload(files); earliest != nil {
			ref.ReleaseDate = earliest
		}
		// Per-file yank status is already in this document; before, it was only
		// consulted for the latest release.
		ref.Yanked = len(files) > 0 && allYanked(files)
		facts.Releases = append(facts.Releases, ref)
		byKey[pypiKey(released)] = released
		if released == facts.LatestVersion && ref.Yanked {
			facts.Deprecated = true
			facts.DeprecationMessage = "latest release is yanked on PyPI"
		}
	}
	sortNewestFirst(facts.Releases)

	if version != "" {
		facts.Requested = pypiVersionFacts(facts, name, version, byKey)
	}
	return facts, nil
}

// pypiVersionFacts resolves a requested version against the release keys, and
// on a miss confirms with the per-version endpoint before saying it does not
// exist. That extra request only happens on the miss path, and it is what makes
// a "this version does not exist" answer authoritative rather than a guess
// about our own normalization.
func pypiVersionFacts(facts *model.PackageFacts, name, version string, byKey map[string]string) *model.VersionFacts {
	resolved := byKey[pypiKey(version)]
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		var one pypiDoc
		if err := getJSON(pypiRegistry+"/"+url.PathEscape(name)+"/"+url.PathEscape(version)+"/json", &one); err != nil {
			return "", err
		}
		return firstNonEmpty(one.Info.Version, version), nil
	})
	if vf.Yanked {
		vf.DeprecationMessage = "release is yanked on PyPI"
	}
	// Per-version license needs its own request, so leave it to deps.dev
	// enrichment, which is asked about this exact version anyway.
	return vf
}

// pypiKey is a loose PEP 440 normalization, enough to match a requested
// spelling against the keys PyPI returned: case, a leading "v", separators
// before prerelease markers, and trailing zero segments all vary in practice
// ("1.0", "1.0.0", "v1.0", "2.0rc1", "2.0-rc1", "2.0.rc1").
//
// It is only ever used to *find* a release. A key that fails to match falls
// through to the authoritative per-version request, so being incomplete here
// costs one HTTP call and never a wrong answer.
func pypiKey(version string) string {
	v := strings.ToLower(strings.TrimSpace(version))
	v = strings.TrimPrefix(v, "v")
	v = strings.NewReplacer("alpha", "a", "beta", "b", "preview", "rc", "pre", "rc", "-c", "-rc").Replace(v)

	// Drop a separator that sits immediately before a letter: PEP 440 treats
	// "1.0-rc1", "1.0.rc1", "1.0_rc1" and "1.0rc1" as the same version.
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c == '-' || c == '_' || c == '.') && i+1 < len(v) && v[i+1] >= 'a' && v[i+1] <= 'z' {
			continue
		}
		b.WriteByte(c)
	}
	v = b.String()

	// Trim trailing zero segments of the numeric release part: 1.0.0 == 1.0 == 1.
	end := 0
	for end < len(v) && (v[end] >= '0' && v[end] <= '9' || v[end] == '.') {
		end++
	}
	release, rest := v[:end], v[end:]
	for strings.HasSuffix(release, ".0") {
		release = strings.TrimSuffix(release, ".0")
	}
	return release + rest
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
