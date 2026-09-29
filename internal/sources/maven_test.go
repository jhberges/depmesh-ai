package sources

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mavenServer is a stand-in Maven repository. Routes are exact paths; anything
// else answers 404, and every request is recorded so a test can assert which
// repository was asked — which is the whole question for namespace routing.
type mavenServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func (m *mavenServer) asked(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.requests {
		if r == path {
			return true
		}
	}
	return false
}

func (m *mavenServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func mavenRepoServer(t *testing.T, routes map[string]string) *mavenServer {
	t.Helper()
	m := &mavenServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests = append(m.requests, r.URL.Path)
		m.mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if code, rest, isStatus := cutStatusPrefix(body); isStatus {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(rest))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(m.Close)
	return m
}

// useCentral points the default repository at a stand-in.
func useCentral(t *testing.T, url string) {
	t.Helper()
	previous := mavenCentral
	mavenCentral = url
	t.Cleanup(func() { mavenCentral = previous })
}

func configure(t *testing.T, c Config) {
	t.Helper()
	configMu.RLock()
	previous := configured
	configMu.RUnlock()
	Configure(c)
	t.Cleanup(func() { Configure(previous) })
}

func metadataXML(release string, versions ...string) string {
	var b strings.Builder
	b.WriteString("<metadata><versioning>")
	if release != "" {
		fmt.Fprintf(&b, "<release>%s</release><latest>%s</latest>", release, release)
	}
	b.WriteString("<versions>")
	for _, v := range versions {
		fmt.Fprintf(&b, "<version>%s</version>", v)
	}
	b.WriteString("</versions></versioning></metadata>")
	return b.String()
}

func pomXML(license string) string {
	if license == "" {
		return "<project><modelVersion>4.0.0</modelVersion></project>"
	}
	return "<project><licenses><license><name>" + license + "</name></license></licenses></project>"
}

// Plenty of long-frozen artifacts — aopalliance:1.0, javax.inject:1 — have
// metadata with a version list and no <release> or <latest> at all. Treating
// that as "no latest version" skipped the license lookup entirely, and the
// missing license then scored as a legal risk on a POM that declares one.
func TestLatestFallsBackToVersionListWhenMetadataDeclaresNone(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/aopalliance/aopalliance/maven-metadata.xml":      metadataXML("", "1.0"),
		"/aopalliance/aopalliance/1.0/aopalliance-1.0.pom": pomXML("Public Domain"),
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("aopalliance:aopalliance", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.LatestVersion != "1.0" {
		t.Errorf("latest = %q, wanted 1.0 from the version list", facts.LatestVersion)
	}
	if facts.License != "Public Domain" {
		t.Errorf("license = %q, wanted the one the POM declares", facts.License)
	}
	if facts.LicenseUnknown {
		t.Error("a license we read fine was marked unknown")
	}
}

// Maven's <release> excludes snapshots but not prereleases, so a project
// mid-beta declares a beta. Kotlin's metadata really does say 2.5.0-Beta1,
// which made every stable pin look out of date against a version nobody
// should ship.
func TestLatestSkipsAPrereleaseRelease(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/org/example/lib/maven-metadata.xml":    metadataXML("2.5.0-Beta1", "2.4.0", "2.4.20", "2.5.0-Beta1"),
		"/org/example/lib/2.4.20/lib-2.4.20.pom": pomXML("Apache-2.0"),
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.example:lib", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.LatestVersion != "2.4.20" {
		t.Errorf("latest = %q, wanted the newest stable 2.4.20", facts.LatestVersion)
	}
}

func TestLatestKeepsAPrereleaseWhenNothingStableExists(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/org/example/lib/maven-metadata.xml": metadataXML("1.0.0-alpha1", "1.0.0-alpha1"),
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.example:lib", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.LatestVersion != "1.0.0-alpha1" {
		t.Errorf("latest = %q; a project with only prereleases should still name one", facts.LatestVersion)
	}
}

// slf4j-api's own POM has no <licenses> element; the license is declared once
// in slf4j-parent and inherited. Reading only the artifact's POM reported MIT
// and Apache projects as unlicensed.
func TestLicenseComesFromTheParentPOM(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/org/slf4j/slf4j-api/maven-metadata.xml": metadataXML("2.0.17", "2.0.17"),
		"/org/slf4j/slf4j-api/2.0.17/slf4j-api-2.0.17.pom": `<project>
			<parent><groupId>org.slf4j</groupId><artifactId>slf4j-parent</artifactId>
			<version>2.0.17</version></parent></project>`,
		"/org/slf4j/slf4j-parent/2.0.17/slf4j-parent-2.0.17.pom": pomXML("MIT"),
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.slf4j:slf4j-api", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.License != "MIT" {
		t.Errorf("license = %q, wanted MIT inherited from the parent", facts.License)
	}
}

func TestParentWalkStopsAtAMissingParent(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/org/example/lib/maven-metadata.xml": metadataXML("1.0", "1.0"),
		"/org/example/lib/1.0/lib-1.0.pom": `<project>
			<parent><groupId>org.example</groupId><artifactId>missing</artifactId>
			<version>1.0</version></parent></project>`,
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.example:lib", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The chain ended without a license. That is a real answer, not a fault.
	if facts.License != "" || facts.LicenseUnknown {
		t.Errorf("license=%q unknown=%v; wanted a declared-nothing answer", facts.License, facts.LicenseUnknown)
	}
}

// A POM we could not fetch is not a POM that declares no license. Scoring the
// two the same turned a rate limit into a legal-risk finding.
func TestUnreadablePOMLeavesTheLicenseUnknown(t *testing.T) {
	fastRetries(t)
	server := mavenRepoServer(t, map[string]string{
		"/org/example/lib/maven-metadata.xml": metadataXML("1.0", "1.0"),
		"/org/example/lib/1.0/lib-1.0.pom":    "STATUS:429:slow down",
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.example:lib", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !facts.LicenseUnknown {
		t.Error("a rate-limited POM was reported as declaring no license")
	}
	if len(facts.Degraded) == 0 {
		t.Error("an undetermined license recorded no degraded source")
	}
}

// Namespace routing is the dependency-confusion defence: a declared internal
// namespace is resolved against the internal repository and nowhere else, so
// a public registry can never shadow it.
func TestInternalNamespaceIsResolvedOnlyInternally(t *testing.T) {
	central := mavenRepoServer(t, map[string]string{
		"/com/acme/platform/audit-log/maven-metadata.xml": metadataXML("9.9.9", "9.9.9"),
	})
	nexus := mavenRepoServer(t, map[string]string{
		"/com/acme/platform/audit-log/maven-metadata.xml":        metadataXML("2.5.0", "2.5.0"),
		"/com/acme/platform/audit-log/2.5.0/audit-log-2.5.0.pom": pomXML("Proprietary"),
	})
	useCentral(t, central.URL)
	configure(t, Config{Maven: []MavenRepo{{URL: nexus.URL, Namespaces: []string{"com.acme"}}}})

	facts, err := fetchMaven("com.acme.platform:audit-log", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !facts.Internal {
		t.Error("a package under a declared namespace was not marked internal")
	}
	if facts.LatestVersion != "2.5.0" {
		t.Errorf("latest = %q, wanted the internal repository's 2.5.0", facts.LatestVersion)
	}
	if central.count() != 0 {
		t.Errorf("an internal coordinate was looked up on the public registry: %v", central.requests)
	}
}

// A name that does not exist inside its own namespace is still a REJECT. The
// namespace says where to look, not that everything under it is fine.
func TestTypoInsideAnInternalNamespaceStillFails(t *testing.T) {
	central := mavenRepoServer(t, map[string]string{})
	nexus := mavenRepoServer(t, map[string]string{})
	useCentral(t, central.URL)
	configure(t, Config{Maven: []MavenRepo{{URL: nexus.URL, Namespaces: []string{"com.acme"}}}})

	facts, err := fetchMaven("com.acme.platform:nonexistent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Error("a name absent from its own namespace was not reported absent")
	}
}

func TestNamespaceMatchingIsOnDotBoundaries(t *testing.T) {
	for _, tc := range []struct {
		prefix, group string
		want          bool
	}{
		{"com.acme", "com.acme", true},
		{"com.acme", "com.acme.platform", true},
		{"com.acme", "com.acmecorp", false},
		{"com.acme", "com.acme.platform.internal", true},
		{"COM.ACME", "com.acme.platform", true},
		{"com.acme", "org.other", false},
	} {
		if got := ownsNamespace(tc.prefix, tc.group); got != tc.want {
			t.Errorf("ownsNamespace(%q, %q) = %v, wanted %v", tc.prefix, tc.group, got, tc.want)
		}
	}
}

// A repository with no namespaces joins the public chain instead — the shape
// for a public repository Central does not mirror, such as the Gradle Plugin
// Portal and its plugin marker artifacts.
func TestFallbackRepositoryIsSearchedAfterCentral(t *testing.T) {
	central := mavenRepoServer(t, map[string]string{})
	portal := mavenRepoServer(t, map[string]string{
		"/com/example/com.example.gradle.plugin/maven-metadata.xml":                        metadataXML("1.2.3", "1.2.3"),
		"/com/example/com.example.gradle.plugin/1.2.3/com.example.gradle.plugin-1.2.3.pom": pomXML("Apache-2.0"),
	})
	useCentral(t, central.URL)
	configure(t, Config{Maven: []MavenRepo{{URL: portal.URL}}})

	facts, err := fetchMaven("com.example:com.example.gradle.plugin", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.Exists == nil || !*facts.Exists {
		t.Fatal("an artifact on the fallback repository was reported absent")
	}
	if facts.Internal {
		t.Error("a repository with no namespaces marked its artifacts internal")
	}
	if central.count() == 0 {
		t.Error("the public registry was skipped; it should be tried first")
	}
}

// Absence is only absence when every repository said so. One that could not
// be reached leaves existence unknown — otherwise a VPN that is not up
// reports the whole internal namespace as hallucinated.
func TestUnreachableRepositoryIsNotAbsence(t *testing.T) {
	fastRetries(t)
	central := mavenRepoServer(t, map[string]string{})
	nexus := mavenRepoServer(t, map[string]string{
		"/com/acme/platform/audit-log/maven-metadata.xml": "STATUS:503:down for maintenance",
	})
	useCentral(t, central.URL)
	configure(t, Config{Maven: []MavenRepo{{URL: nexus.URL}}})

	_, err := fetchMaven("com.acme.platform:audit-log", "")
	if err == nil {
		t.Fatal("an unreachable repository was reported as a definite absence")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("an unreachable repository was reported as not found")
	}
}

// Credentials belong to the repository they were configured for. Sending an
// internal token to Maven Central would be a leak, not a convenience.
func TestCredentialsGoOnlyToTheirOwnRepository(t *testing.T) {
	t.Setenv("TEST_NEXUS_TOKEN", "s3cret")
	nexus := mavenRepoServer(t, map[string]string{})
	configure(t, Config{Maven: []MavenRepo{{URL: nexus.URL, TokenEnv: "TEST_NEXUS_TOKEN"}}})

	if _, ok := credentialsFor(nexus.URL + "/com/acme/x/maven-metadata.xml"); !ok {
		t.Error("configured repository got no credentials")
	}
	if _, ok := credentialsFor("https://repo1.maven.org/maven2/com/acme/x/maven-metadata.xml"); ok {
		t.Error("credentials were offered to a repository they were not configured for")
	}
}

func TestBasicAuthCredentials(t *testing.T) {
	t.Setenv("TEST_NEXUS_USER", "build")
	t.Setenv("TEST_NEXUS_PASSWORD", "hunter2")
	var gotUser, gotPassword string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, _ = r.BasicAuth()
		http.NotFound(w, r)
	}))
	defer server.Close()
	configure(t, Config{Maven: []MavenRepo{{
		URL:         server.URL,
		Namespaces:  []string{"com.acme"},
		UsernameEnv: "TEST_NEXUS_USER",
		PasswordEnv: "TEST_NEXUS_PASSWORD",
	}}})

	_, _ = fetchMaven("com.acme.platform:audit-log", "")
	if gotUser != "build" || gotPassword != "hunter2" {
		t.Errorf("basic auth was %q/%q, wanted build/hunter2", gotUser, gotPassword)
	}
}

func TestLatestFallsBackToAPrereleaseWhenMetadataDeclaresNothing(t *testing.T) {
	server := mavenRepoServer(t, map[string]string{
		"/org/example/lib/maven-metadata.xml":                metadataXML("", "1.0.0-alpha1", "1.0.0-alpha2"),
		"/org/example/lib/1.0.0-alpha2/lib-1.0.0-alpha2.pom": pomXML("MIT"),
	})
	useCentral(t, server.URL)
	configure(t, Config{})

	facts, err := fetchMaven("org.example:lib", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.LatestVersion != "1.0.0-alpha2" {
		t.Errorf("latest = %q, wanted the newest prerelease when nothing else exists", facts.LatestVersion)
	}
	if facts.License != "MIT" {
		t.Errorf("license = %q; naming a latest is what makes the license readable", facts.License)
	}
}

func TestCredentialsRespectPathBoundaries(t *testing.T) {
	t.Setenv("TEST_TOKEN", "s3cret")
	configure(t, Config{Maven: []MavenRepo{{
		URL: "https://nexus.example.com/repository/maven-releases", TokenEnv: "TEST_TOKEN",
	}}})

	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://nexus.example.com/repository/maven-releases/com/acme/x.pom", true},
		{"https://nexus.example.com/repository/maven-releases", true},
		{"https://nexus.example.com/repository/maven-releases-staging/x.pom", false},
		{"https://repo1.maven.org/maven2/com/acme/x.pom", false},
	} {
		if _, got := credentialsFor(tc.url); got != tc.want {
			t.Errorf("credentialsFor(%q) = %v, wanted %v", tc.url, got, tc.want)
		}
	}
}
