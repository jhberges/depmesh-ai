package sources

import (
	"errors"
	"strings"
	"testing"
)

// Packagist's p2 shape, including the delta encoding it announces as
// "minified": after the first entry a field is omitted when unchanged, which
// is why only the newest version here carries a license or an author list.
const packagistPackage = `{
  "minified": "composer/2.0",
  "packages": {
    "acme/widgets": [
      {"version": "3.1.0", "time": "2025-06-01T08:00:00+00:00",
       "license": ["MIT"], "description": "Widgets.",
       "homepage": "https://example.invalid",
       "authors": [{"name": "A"}, {"name": "B"}],
       "source": {"url": "https://github.com/acme/widgets.git"}},
      {"version": "3.0.0", "time": "2024-11-02T08:00:00+00:00"},
      {"version": "2.0.0", "time": "2023-02-14T08:00:00+00:00"}
    ]
  }
}`

func packagistServer(t *testing.T, routes map[string]string) {
	t.Helper()
	server := registry(t, routes)
	previous := packagistRepo
	packagistRepo = server.URL
	t.Cleanup(func() { packagistRepo = previous })
}

func TestPackagistReadsAPackage(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/widgets.json": packagistPackage})

	facts, err := fetchPackagist("acme/widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.LatestVersion != "3.1.0" {
		t.Errorf("LatestVersion = %q, want 3.1.0", facts.LatestVersion)
	}
	if facts.License != "MIT" {
		t.Errorf("License = %q, want MIT", facts.License)
	}
	if facts.RepositoryURL != "https://github.com/acme/widgets.git" {
		t.Errorf("RepositoryURL = %q", facts.RepositoryURL)
	}
	if len(facts.Releases) != 3 {
		t.Errorf("Releases = %v, want 3", versions(facts))
	}
	// Every entry carries its own time even under the delta encoding.
	if got := day(t, release(t, facts, "2.0.0")); got != "2023-02-14" {
		t.Errorf("2.0.0 released %s, want 2023-02-14", got)
	}
	// authors is a real list here, unlike NuGet's display string, so the
	// bus-factor signal has something to work with.
	if facts.MaintainerCount == nil || *facts.MaintainerCount != 2 {
		t.Errorf("MaintainerCount = %v, want 2", facts.MaintainerCount)
	}
}

// A bare name is a mistake to report, not a package to look up: answering
// "does not exist" would be a slopsquat alarm for a typo.
func TestPackagistRequiresAVendor(t *testing.T) {
	packagistServer(t, map[string]string{})

	for _, name := range []string{"monolog", "", "a/b/c"} {
		facts, err := fetchPackagist(name, "")
		if err == nil {
			t.Errorf("fetchPackagist(%q) = %+v, want an error", name, facts)
		}
		if facts != nil {
			t.Errorf("fetchPackagist(%q) returned facts alongside the error", name)
		}
	}
}

func TestPackagistReadsAbandoned(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/old.json": `{
	  "packages": {"acme/old": [
	    {"version": "v6.3.0", "time": "2023-05-29T08:00:00+00:00",
	     "license": ["MIT"], "abandoned": "acme/new"}
	  ]}
	}`})

	facts, err := fetchPackagist("acme/old", "")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated {
		t.Fatal("Deprecated = false, want true")
	}
	if !strings.Contains(facts.DeprecationMessage, "acme/new") {
		t.Errorf("DeprecationMessage = %q, want the named replacement", facts.DeprecationMessage)
	}
}

// `abandoned` is either a replacement name or a bare true; both are the same
// verdict, only the reason differs.
func TestPackagistReadsAbandonedWithoutAReplacement(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/old.json": `{
	  "packages": {"acme/old": [
	    {"version": "1.0.0", "time": "2023-05-29T08:00:00+00:00", "abandoned": true}
	  ]}
	}`})

	facts, err := fetchPackagist("acme/old", "")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated || facts.DeprecationMessage == "" {
		t.Errorf("Deprecated = %v (%q), want true with a reason",
			facts.Deprecated, facts.DeprecationMessage)
	}
}

// Packagist names are case-insensitive and the response is keyed by the
// canonical casing, which need not be what was typed.
func TestPackagistToleratesCanonicalCasing(t *testing.T) {
	packagistServer(t, map[string]string{"/Acme/Widgets.json": `{
	  "packages": {"acme/widgets": [{"version": "1.0.0", "time": "2023-01-01T00:00:00+00:00"}]}
	}`})

	facts, err := fetchPackagist("Acme/Widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.LatestVersion != "1.0.0" {
		t.Errorf("LatestVersion = %q; the response key did not match the request", facts.LatestVersion)
	}
}

func TestPackagistReportsAbsence(t *testing.T) {
	packagistServer(t, map[string]string{})

	facts, err := fetchPackagist("acme/nope", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestPackagistOutageIsNotAbsence(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/widgets.json": "STATUS:502:bad gateway"})

	facts, err := fetchPackagist("acme/widgets", "")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

// The delta encoding is why this needs expanding at all: 3.0.0's entry carries
// no license, which means "the same as the entry before it", not "none".
func TestPackagistExpandsDeltasForTheRequestedVersion(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/widgets.json": packagistPackage})

	facts, err := fetchPackagist("acme/widgets", "3.0.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested == nil || requested.Exists == nil || !*requested.Exists {
		t.Fatalf("requested = %+v, want an existing version", requested)
	}
	if requested.License != "MIT" {
		t.Errorf("license = %q, want the MIT it inherited from 3.1.0", requested.License)
	}
	if requested.IsLatest {
		t.Error("3.0.0 reported as the latest release")
	}
	if requested.ReleaseDate == nil {
		t.Error("release date not carried over from the release history")
	}
}

// Packagist reports the tag verbatim, so the same release is "1.2.0" in one
// project and "v1.2.0" in the next.
func TestPackagistToleratesTagSpelling(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/widgets.json": packagistPackage})

	facts, err := fetchPackagist("acme/widgets", "v3.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Resolved != "3.1.0" {
		t.Errorf("resolved = %q, want the published spelling", facts.Requested.Resolved)
	}
	if !facts.Requested.IsLatest {
		t.Error("3.1.0 is the latest and should be reported as such")
	}
}

func TestPackagistReadsAbandonmentForAnOlderVersion(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/old.json": `{
	  "minified": "composer/2.0",
	  "packages": {"acme/old": [
	    {"version": "2.0.0", "time": "2023-05-29T08:00:00+00:00",
	     "license": ["MIT"], "abandoned": "acme/new"},
	    {"version": "1.0.0", "time": "2021-01-04T08:00:00+00:00"}
	  ]}
	}`})

	// Abandonment is a property of the package, which Packagist writes onto
	// every version and the delta encoding then collapses onto the newest
	// entry. Pinning an older release does not escape it, and expanding the
	// deltas in the order Composer does is what makes that come out right.
	facts, err := fetchPackagist("acme/old", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.Deprecated ||
		!strings.Contains(facts.Requested.DeprecationMessage, "acme/new") {
		t.Errorf("requested = %+v, want abandonment naming the replacement", facts.Requested)
	}
	if facts.Requested.License != "MIT" {
		t.Errorf("license = %q, want the MIT it inherited", facts.Requested.License)
	}
}

// Branch versions live in a separate document, and a Composer file may well
// pin one. The tagged releases alone are not grounds for denying it.
func TestPackagistFindsBranchVersionsInTheDevDocument(t *testing.T) {
	packagistServer(t, map[string]string{
		"/acme/widgets.json": packagistPackage,
		"/acme/widgets~dev.json": `{
		  "minified": "composer/2.0",
		  "packages": {"acme/widgets": [
		    {"version": "dev-main", "time": "2025-07-01T08:00:00+00:00", "license": ["MIT"]}
		  ]}
		}`,
	})

	facts, err := fetchPackagist("acme/widgets", "dev-main")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true", requested.Exists)
	}
	if requested.License != "MIT" || requested.ReleaseDate == nil {
		t.Errorf("requested = %+v, want the branch's license and date", requested)
	}
}

func TestPackagistUnknownVersionIsDeniedOnlyAfterTheDevDocument(t *testing.T) {
	packagistServer(t, map[string]string{
		"/acme/widgets.json":     packagistPackage,
		"/acme/widgets~dev.json": `{"packages": {"acme/widgets": []}}`,
	})

	facts, err := fetchPackagist("acme/widgets", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists == nil || *facts.Requested.Exists {
		t.Fatalf("exists = %v, want false once both documents were read", facts.Requested.Exists)
	}
}

func TestPackagistVersionOutageLeavesExistenceUnknown(t *testing.T) {
	packagistServer(t, map[string]string{
		"/acme/widgets.json":     packagistPackage,
		"/acme/widgets~dev.json": "STATUS:503:service unavailable",
	})

	facts, err := fetchPackagist("acme/widgets", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists != nil {
		t.Errorf("exists = %v, want unknown after a 503", *facts.Requested.Exists)
	}
}
