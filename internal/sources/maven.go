package sources

import (
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Maven Central source (repo1.maven.org). Package names use the
// "groupId:artifactId" form, as in the original DepMesh Maven prototype.
// This talks to the repository directly instead of spawning a local Maven
// process the way depmesh-resolver did — version lists come from
// maven-metadata.xml, release dates from the directory listing, and the
// license from the latest POM.
const mavenRepo = "https://repo1.maven.org/maven2"

// Directory listing rows look like:
//
//	<a href="3.12.0/" title="3.12.0/">3.12.0/</a>  2021-03-01 07:38  -
var listingRow = regexp.MustCompile(`href="([^"/]+)/"[^>]*>[^<]+</a>\s+(\d{4}-\d{2}-\d{2})`)

type mavenMetadata struct {
	Versioning struct {
		Latest   string   `xml:"latest"`
		Release  string   `xml:"release"`
		Versions []string `xml:"versions>version"`
	} `xml:"versioning"`
}

type mavenPOM struct {
	Licenses []struct {
		Name string `xml:"name"`
	} `xml:"licenses>license"`
}

func fetchMaven(name, version string) (*model.PackageFacts, error) {
	group, artifact, ok := strings.Cut(name, ":")
	if !ok {
		return nil, fmt.Errorf("maven package names must be 'groupId:artifactId', got %q", name)
	}
	facts := &model.PackageFacts{Ecosystem: model.Maven, Name: name}
	baseURL := mavenRepo + "/" + strings.ReplaceAll(group, ".", "/") + "/" + artifact

	body, err := getText(baseURL + "/maven-metadata.xml")
	if errors.Is(err, ErrNotFound) {
		facts.Exists = model.Bool(false)
		return facts, nil
	}
	if err != nil {
		return nil, err
	}

	facts.Exists = model.Bool(true)
	var metadata mavenMetadata
	if err := xml.Unmarshal([]byte(body), &metadata); err != nil {
		return nil, &UnavailableError{baseURL, fmt.Errorf("unparseable maven-metadata.xml: %w", err)}
	}
	facts.LatestVersion = firstNonEmpty(metadata.Versioning.Release, metadata.Versioning.Latest)

	dates := listingDates(baseURL)
	if len(dates) == 0 {
		facts.Degraded = append(facts.Degraded, "maven directory listing (release dates unavailable)")
	}
	for _, version := range metadata.Versioning.Versions {
		ref := model.ReleaseRef{Version: version}
		if date, ok := dates[version]; ok {
			date := date
			ref.ReleaseDate = &date
		}
		facts.Releases = append(facts.Releases, ref)
	}
	sortNewestFirst(facts.Releases)

	if facts.LatestVersion != "" {
		facts.License = pomLicense(baseURL, artifact, facts.LatestVersion)
	}

	if version != "" {
		facts.Requested = mavenVersionFacts(facts, baseURL, artifact, version, metadata.Versioning.Versions)
	}
	return facts, nil
}

// mavenVersionFacts resolves a requested version against maven-metadata.xml,
// and on a miss confirms against the POM for that exact coordinate — metadata
// can lag behind what is actually published, so its silence is not proof.
//
// The requested version's license is free: pomLicense already takes a version,
// and this is simply the same single request pointed at a different coordinate
// instead of always at the latest one.
func mavenVersionFacts(facts *model.PackageFacts, baseURL, artifact, version string, published []string) *model.VersionFacts {
	resolved := ""
	for _, candidate := range published {
		if candidate == version {
			resolved = candidate
			break
		}
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		if _, err := getText(pomURL(baseURL, artifact, version)); err != nil {
			return "", err
		}
		return version, nil
	})
	if vf.Resolved != "" {
		vf.License = pomLicense(baseURL, artifact, vf.Resolved)
	}
	return vf
}

func listingDates(baseURL string) map[string]time.Time {
	listing, err := getText(baseURL + "/")
	if err != nil {
		return nil
	}
	dates := map[string]time.Time{}
	for _, match := range listingRow.FindAllStringSubmatch(listing, -1) {
		if t, err := time.Parse("2006-01-02", match[2]); err == nil {
			dates[match[1]] = t
		}
	}
	return dates
}

func pomURL(baseURL, artifact, version string) string {
	return fmt.Sprintf("%s/%s/%s-%s.pom", baseURL, version, artifact, version)
}

func pomLicense(baseURL, artifact, version string) string {
	body, err := getText(pomURL(baseURL, artifact, version))
	if err != nil {
		return ""
	}
	var pom mavenPOM
	if err := xml.Unmarshal([]byte(body), &pom); err != nil {
		return ""
	}
	if len(pom.Licenses) > 0 {
		return strings.TrimSpace(pom.Licenses[0].Name)
	}
	return ""
}
