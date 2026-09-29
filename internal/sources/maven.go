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

// Maven source. Package names use the "groupId:artifactId" form, as in the
// original DepMesh Maven prototype. This talks to the repository directly
// instead of spawning a local Maven process the way depmesh-resolver did —
// version lists come from maven-metadata.xml, release dates from the
// directory listing, and the license from the POM.
//
// Maven Central is the default and is never the whole story: see registry.go
// for the internal and supplementary repositories a policy file can add.
// var rather than const so tests can point it at a stand-in, as the Go
// module source does with its proxy.
var mavenCentral = "https://repo1.maven.org/maven2"

// Directory listing rows look like:
//
//	<a href="3.12.0/" title="3.12.0/">3.12.0/</a>  2021-03-01 07:38  -
var listingRow = regexp.MustCompile(`href="([^"/]+)/"[^>]*>[^<]+</a>\s+(\d{4}-\d{2}-\d{2})`)

// parentDepth caps how far a POM's <parent> chain is followed when looking
// for a license. Deep chains are real (Spring, Apache), cycles are not, and
// the cap costs nothing on a well-formed chain.
const parentDepth = 8

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
	Parent struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	} `xml:"parent"`
}

// coordinate is an artifact located in one repository: everything the fetch
// helpers need to build a URL, kept together so the parent-POM walk can cross
// groupIds without rebuilding paths by hand.
type coordinate struct {
	repo     string
	group    string
	artifact string
}

func (c coordinate) base() string {
	return strings.TrimSuffix(c.repo, "/") + "/" +
		strings.ReplaceAll(c.group, ".", "/") + "/" + c.artifact
}

func (c coordinate) pom(version string) string {
	return fmt.Sprintf("%s/%s/%s-%s.pom", c.base(), version, c.artifact, version)
}

func fetchMaven(name, version string) (*model.PackageFacts, error) {
	group, artifact, ok := strings.Cut(name, ":")
	if !ok {
		return nil, fmt.Errorf("maven package names must be 'groupId:artifactId', got %q", name)
	}
	facts := &model.PackageFacts{Ecosystem: model.Maven, Name: name}
	repos, internal := mavenSearchPath(group)
	facts.Internal = internal

	// Search in order and take the first repository that has the artifact.
	// A 404 moves on; anything else is remembered, because it is the
	// difference between "nowhere has it" and "we could not ask".
	var unreachable error
	for _, repo := range repos {
		at := coordinate{repo: repo.URL, group: group, artifact: artifact}
		body, err := getText(at.base() + "/maven-metadata.xml")
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			unreachable = err
			continue
		}
		return mavenFacts(facts, at, version, body)
	}

	// Absence is absence only when every repository authoritatively said so.
	// If one of them could not be reached, the package may well live there,
	// and reporting "does not exist" would turn an outage — or a VPN that is
	// not up — into a hallucination alarm against a real internal library.
	if unreachable != nil {
		return nil, unreachable
	}
	facts.Exists = model.Bool(false)
	return facts, nil
}

func mavenFacts(facts *model.PackageFacts, at coordinate, version, metadataXML string) (*model.PackageFacts, error) {
	facts.Exists = model.Bool(true)
	var metadata mavenMetadata
	if err := xml.Unmarshal([]byte(metadataXML), &metadata); err != nil {
		return nil, &UnavailableError{at.base(), fmt.Errorf("unparseable maven-metadata.xml: %w", err)}
	}
	facts.LatestVersion = mavenLatest(metadata)

	dates := listingDates(at.base())
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
		facts.License, facts.LicenseUnknown = readLicense(facts, at, facts.LatestVersion)
	}

	if version != "" {
		facts.Requested = mavenVersionFacts(facts, at, version, metadata.Versioning.Versions)
	}
	return facts, nil
}

// mavenLatest picks the version to treat as the project's latest release.
//
// Two things make this more than reading a field. Maven's <release> excludes
// snapshots but *not* prereleases, so a project mid-beta declares a beta as
// its release — Kotlin's metadata says 2.5.0-Beta1 — and taking that at face
// value reports every stable pin as out of date against a version nobody
// should ship. And plenty of old artifacts have metadata with neither
// <release> nor <latest>, only a version list; the field is simply absent,
// which is not the same as the project having no releases.
//
// So: the declared field when it names a stable release, otherwise the newest
// stable in the list. The list is oldest-first, which is Maven's ordering and
// why this reads it here rather than from the date-sorted Releases — a
// project whose directory listing gave us no dates has no meaningful date
// order to rely on.
func mavenLatest(metadata mavenMetadata) string {
	declared := []string{metadata.Versioning.Release, metadata.Versioning.Latest}
	for _, version := range declared {
		if version != "" && !model.IsPrerelease(version) {
			return version
		}
	}
	versions := metadata.Versioning.Versions
	for i := len(versions) - 1; i >= 0; i-- {
		if !model.IsPrerelease(versions[i]) {
			return versions[i]
		}
	}
	// Nothing stable has ever been published. Naming the newest prerelease is
	// more useful than naming nothing — it is what the license is read from,
	// and the prerelease signals judge it on its own terms.
	if latest := firstNonEmpty(declared...); latest != "" {
		return latest
	}
	if len(versions) > 0 {
		return versions[len(versions)-1]
	}
	return ""
}

// mavenVersionFacts resolves a requested version against maven-metadata.xml,
// and on a miss confirms against the POM for that exact coordinate — metadata
// can lag behind what is actually published, so its silence is not proof.
//
// The requested version's license is free: readLicense already takes a
// version, and this is simply the same lookup pointed at a different
// coordinate instead of always at the latest one.
func mavenVersionFacts(facts *model.PackageFacts, at coordinate, version string, published []string) *model.VersionFacts {
	resolved := ""
	for _, candidate := range published {
		if candidate == version {
			resolved = candidate
			break
		}
	}
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		if _, err := getText(at.pom(version)); err != nil {
			return "", err
		}
		return version, nil
	})
	if vf.Resolved != "" {
		vf.License, _ = readLicense(facts, at, vf.Resolved)
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

// readLicense resolves the license for one release, and reports whether the
// answer is "none declared" or "we could not tell".
//
// That distinction is the point. A POM we could not fetch — a rate limit, a
// repository behind a VPN that is not up — used to read as an empty license,
// which the verdict scores as a legal risk and a policy can be configured to
// fail outright. A transient network fault must not be reported as a missing
// license, so the unknown case is returned as unknown and recorded as a
// degraded source.
func readLicense(facts *model.PackageFacts, at coordinate, version string) (license string, unknown bool) {
	license, err := pomLicense(at, version)
	if err != nil {
		facts.Degraded = append(facts.Degraded,
			fmt.Sprintf("maven POM for %s (license undetermined)", version))
		return "", true
	}
	return license, false
}

// pomLicense reads a license from a POM, following <parent> when the
// artifact's own POM declares none.
//
// Maven projects routinely declare the license once in a parent and inherit
// it everywhere — slf4j-api's POM has no <licenses> element at all, the
// license lives in slf4j-parent — so reading only the artifact's own POM
// reports MIT and Apache projects as unlicensed. A missing parent is not an
// error: it means the chain ended without a license, which is a real answer.
func pomLicense(at coordinate, version string) (string, error) {
	for range parentDepth {
		body, err := getText(at.pom(version))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// The chain is broken above us, not unreachable: whatever we
				// have found so far (nothing) is the answer.
				return "", nil
			}
			return "", err
		}
		var pom mavenPOM
		if err := xml.Unmarshal([]byte(body), &pom); err != nil {
			return "", nil // a POM we cannot parse declares nothing we can read
		}
		if len(pom.Licenses) > 0 {
			return strings.TrimSpace(pom.Licenses[0].Name), nil
		}
		if pom.Parent.ArtifactID == "" || pom.Parent.Version == "" {
			return "", nil
		}
		group := pom.Parent.GroupID
		if group == "" {
			group = at.group
		}
		at = coordinate{repo: at.repo, group: group, artifact: pom.Parent.ArtifactID}
		version = pom.Parent.Version
	}
	return "", nil
}
