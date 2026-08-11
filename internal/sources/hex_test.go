package sources

import (
	"errors"
	"strings"
	"testing"
)

// hex.pm's shape, with a retirement on an old version and a healthy release
// on top — plug_cowboy's real situation.
const hexPackage = `{
  "html_url": "https://hex.pm/packages/widgets",
  "latest_version": "2.9.0",
  "latest_stable_version": "2.9.0",
  "meta": {
    "description": "Widgets.",
    "licenses": ["Apache-2.0"],
    "links": {"GitHub": "https://github.com/acme/widgets", "Website": "https://example.invalid"}
  },
  "owners": [{"username": "a"}, {"username": "b"}],
  "releases": [
    {"version": "2.9.0", "inserted_at": "2026-06-26T10:00:00.000000Z"},
    {"version": "2.2.1", "inserted_at": "2020-03-02T10:00:00.000000Z"},
    {"version": "2.2.0", "inserted_at": "2020-02-14T10:00:00.000000Z"}
  ],
  "retirements": {
    "2.2.0": {"message": "Broken telemetry support", "reason": "invalid"},
    "2.2.1": {"message": "Broken telemetry support", "reason": "invalid"}
  }
}`

func hexServer(t *testing.T, routes map[string]string) {
	t.Helper()
	server := registry(t, routes)
	previous := hexRegistry
	hexRegistry = server.URL
	t.Cleanup(func() { hexRegistry = previous })
}

func TestHexReadsAPackage(t *testing.T) {
	hexServer(t, map[string]string{"/widgets": hexPackage})

	facts, err := fetchHex("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.LatestVersion != "2.9.0" {
		t.Errorf("LatestVersion = %q, want 2.9.0", facts.LatestVersion)
	}
	if facts.License != "Apache-2.0" {
		t.Errorf("License = %q", facts.License)
	}
	// meta.links keys are whatever the maintainer typed.
	if facts.RepositoryURL != "https://github.com/acme/widgets" {
		t.Errorf("RepositoryURL = %q; links should match case-insensitively", facts.RepositoryURL)
	}
	if facts.Homepage != "https://example.invalid" {
		t.Errorf("Homepage = %q", facts.Homepage)
	}
	if got := day(t, release(t, facts, "2.2.0")); got != "2020-02-14" {
		t.Errorf("2.2.0 released %s, want 2020-02-14", got)
	}
	// Hex is the second ecosystem after npm to publish an owner list, so this
	// is the bus-factor signal being lit rather than skipped.
	if facts.MaintainerCount == nil || *facts.MaintainerCount != 2 {
		t.Errorf("MaintainerCount = %v, want 2", facts.MaintainerCount)
	}
}

// A retirement does not withdraw a version from resolution — Mix installs it
// and warns — so it stays in the history it really is part of.
func TestHexKeepsRetiredVersionsInTheHistory(t *testing.T) {
	hexServer(t, map[string]string{"/widgets": hexPackage})

	facts, err := fetchHex("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Releases) != 3 {
		t.Errorf("Releases = %v, want all 3", versions(facts))
	}
	if facts.Deprecated {
		t.Errorf("a retired old version with a healthy release on top is not a deprecation (%q)",
			facts.DeprecationMessage)
	}
}

// The current release carrying the marker is the maintainers saying not to use
// what they are shipping, which is what Deprecated is for.
func TestHexTreatsARetiredLatestAsDeprecated(t *testing.T) {
	hexServer(t, map[string]string{"/widgets": `{
	  "latest_stable_version": "1.0.0",
	  "meta": {"licenses": ["MIT"]},
	  "releases": [{"version": "1.0.0", "inserted_at": "2020-02-14T10:00:00Z"}],
	  "retirements": {"1.0.0": {"message": "Use acme_widgets instead", "reason": "renamed"}}
	}`})

	facts, err := fetchHex("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated {
		t.Fatal("Deprecated = false, want true")
	}
	for _, want := range []string{"acme_widgets", "renamed"} {
		if !strings.Contains(facts.DeprecationMessage, want) {
			t.Errorf("DeprecationMessage = %q, want it to mention %q", facts.DeprecationMessage, want)
		}
	}
}

func TestHexReportsAbsence(t *testing.T) {
	hexServer(t, map[string]string{})

	facts, err := fetchHex("no_such_package", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestHexOutageIsNotAbsence(t *testing.T) {
	hexServer(t, map[string]string{"/widgets": "STATUS:500:oops"})

	facts, err := fetchHex("widgets", "")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

func TestHexReadsTheRequestedVersionsOwnLicense(t *testing.T) {
	// 2.2.0 was released under a different license from the current one, which
	// is exactly what a per-version license answer is for.
	hexServer(t, map[string]string{
		"/widgets":                hexPackage,
		"/widgets/releases/2.2.0": `{"version": "2.2.0", "meta": {"licenses": ["MIT"]}}`,
	})

	facts, err := fetchHex("widgets", "2.2.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested == nil || requested.Exists == nil || !*requested.Exists {
		t.Fatalf("requested = %+v, want an existing version", requested)
	}
	if requested.License != "MIT" {
		t.Errorf("license = %q, want the release's own MIT, not the package's", requested.License)
	}
	if requested.IsLatest {
		t.Error("2.2.0 reported as the latest release")
	}
	if requested.ReleaseDate == nil {
		t.Error("release date not carried over from the release list")
	}
}

// A retirement does not withdraw a release from resolution — Mix installs it
// with a warning — so it reads as this version being deprecated, and only for
// the versions that carry one.
func TestHexReadsPerVersionRetirement(t *testing.T) {
	hexServer(t, map[string]string{
		"/widgets":                hexPackage,
		"/widgets/releases/2.2.0": `{"version": "2.2.0", "meta": {"licenses": ["Apache-2.0"]}}`,
		"/widgets/releases/2.9.0": `{"version": "2.9.0", "meta": {"licenses": ["Apache-2.0"]}}`,
	})

	facts, err := fetchHex("widgets", "2.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.Deprecated {
		t.Error("Deprecated = false for a retired release")
	}
	if !strings.Contains(facts.Requested.DeprecationMessage, "Broken telemetry") {
		t.Errorf("message = %q, want the retirement's", facts.Requested.DeprecationMessage)
	}

	facts, err = fetchHex("widgets", "2.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Deprecated {
		t.Error("2.9.0 inherited a retirement recorded against older releases")
	}
	if !facts.Requested.IsLatest {
		t.Error("2.9.0 is the latest stable and should be reported as such")
	}
}

// The release document is worth one request for the license, but it is not
// worth the verdict: an unreachable one leaves the license empty, exactly as
// if it had never been asked.
func TestHexUnreachableReleaseDocumentStillAnswers(t *testing.T) {
	hexServer(t, map[string]string{
		"/widgets":                hexPackage,
		"/widgets/releases/2.9.0": "STATUS:503:service unavailable",
	})

	facts, err := fetchHex("widgets", "2.9.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true — the release list already answered that", requested.Exists)
	}
	if requested.License != "" {
		t.Errorf("license = %q, want empty", requested.License)
	}
}

func TestHexUnknownVersionIsDeniedOnlyByTheReleaseDocument(t *testing.T) {
	hexServer(t, map[string]string{"/widgets": hexPackage})

	facts, err := fetchHex("widgets", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists == nil || *facts.Requested.Exists {
		t.Fatalf("exists = %v, want false once the release document 404s", facts.Requested.Exists)
	}
}

func TestHexVersionOutageLeavesExistenceUnknown(t *testing.T) {
	hexServer(t, map[string]string{
		"/widgets":                hexPackage,
		"/widgets/releases/0.9.0": "STATUS:503:service unavailable",
	})

	facts, err := fetchHex("widgets", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists != nil {
		t.Errorf("exists = %v, want unknown after a 503", *facts.Requested.Exists)
	}
}
