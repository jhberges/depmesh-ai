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

	facts, err := fetchPackagist("acme/widgets")
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
		facts, err := fetchPackagist(name)
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

	facts, err := fetchPackagist("acme/old")
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

	facts, err := fetchPackagist("acme/old")
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

	facts, err := fetchPackagist("Acme/Widgets")
	if err != nil {
		t.Fatal(err)
	}
	if facts.LatestVersion != "1.0.0" {
		t.Errorf("LatestVersion = %q; the response key did not match the request", facts.LatestVersion)
	}
}

func TestPackagistReportsAbsence(t *testing.T) {
	packagistServer(t, map[string]string{})

	facts, err := fetchPackagist("acme/nope")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestPackagistOutageIsNotAbsence(t *testing.T) {
	packagistServer(t, map[string]string{"/acme/widgets.json": "STATUS:502:bad gateway"})

	facts, err := fetchPackagist("acme/widgets")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}
