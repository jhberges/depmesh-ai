package sources

import (
	"errors"
	"testing"
)

const pubPackage = `{
  "name": "widgets",
  "latest": {"version": "1.6.0", "published": "2025-11-10T18:27:56.434747Z",
             "pubspec": {"description": "Widgets.",
                         "repository": "https://github.com/acme/widgets"}},
  "versions": [
    {"version": "1.0.0", "published": "2023-01-04T10:00:00.000000Z"},
    {"version": "1.5.0", "published": "2024-06-01T10:00:00.000000Z", "retracted": true},
    {"version": "1.6.0", "published": "2025-11-10T18:27:56.434747Z"}
  ]
}`

// pub.dev publishes the license only through the analysis endpoint, and that
// namespace mixes the license in with classifiers that describe it.
const pubScoreBody = `{"grantedPoints": 160, "tags":
  ["publisher:dart.dev", "license:osi-approved", "license:bsd-3-clause", "license:fsf-libre"]}`

func pubServer(t *testing.T, routes map[string]string) {
	t.Helper()
	server := registry(t, routes)
	previous := pubRegistry
	pubRegistry = server.URL
	t.Cleanup(func() { pubRegistry = previous })
}

func TestPubReadsAPackage(t *testing.T) {
	pubServer(t, map[string]string{
		"/widgets":       pubPackage,
		"/widgets/score": pubScoreBody,
	})

	facts, err := fetchPub("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.LatestVersion != "1.6.0" {
		t.Errorf("LatestVersion = %q, want 1.6.0", facts.LatestVersion)
	}
	if facts.RepositoryURL != "https://github.com/acme/widgets" {
		t.Errorf("RepositoryURL = %q", facts.RepositoryURL)
	}
	// "osi-approved" and "fsf-libre" describe the license without naming one.
	if facts.License != "BSD-3-CLAUSE" {
		t.Errorf("License = %q, want the named license, not a classifier", facts.License)
	}
	if got := day(t, release(t, facts, "1.0.0")); got != "2023-01-04" {
		t.Errorf("1.0.0 published %s, want 2023-01-04", got)
	}
	if facts.MaintainerCount != nil {
		t.Errorf("MaintainerCount = %d; a publisher is one identity, not a count",
			*facts.MaintainerCount)
	}
}

// Retraction is crates.io's yank by another name, and it is settled the same
// way: out of the pace metric, but not on its own a deprecation.
func TestPubExcludesRetractedVersionsWithoutDeprecating(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage, "/widgets/score": pubScoreBody})

	facts, err := fetchPub("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range facts.Releases {
		if ref.Version == "1.5.0" {
			t.Fatalf("retracted 1.5.0 is in %v", versions(facts))
		}
	}
	if facts.Deprecated {
		t.Error("one retracted version among healthy ones is not a deprecation")
	}
}

func TestPubTreatsAFullyRetractedPackageAsDeprecated(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": `{
	  "latest": {"version": "1.0.0", "published": "2023-01-04T10:00:00Z", "retracted": true},
	  "versions": [{"version": "1.0.0", "published": "2023-01-04T10:00:00Z", "retracted": true}]
	}`})

	facts, err := fetchPub("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated {
		t.Fatal("a package with nothing resolvable left should be deprecated")
	}
	if len(facts.Releases) != 0 {
		t.Errorf("Releases = %v, want none resolvable", versions(facts))
	}
}

// The score endpoint is a convenience, not a dependency: losing it costs the
// license and nothing else.
func TestPubSurvivesAnUnreachableScore(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage})

	facts, err := fetchPub("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.License != "" {
		t.Errorf("License = %q, want empty", facts.License)
	}
	if facts.LatestVersion != "1.6.0" {
		t.Errorf("LatestVersion = %q; the score endpoint should not affect it", facts.LatestVersion)
	}
}

func TestPubReportsAbsence(t *testing.T) {
	pubServer(t, map[string]string{})

	facts, err := fetchPub("no_such_package", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestPubOutageIsNotAbsence(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": "STATUS:503:down"})

	facts, err := fetchPub("widgets", "")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

// deps.dev covers neither packagist nor pub. That is a signal that is absent,
// not a source that is down — saying otherwise sends the reader hunting for an
// egress problem that does not exist.
func TestUncoveredEcosystemsDoNotReportDepsDevAsUnreachable(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage, "/widgets/score": pubScoreBody})

	facts, err := fetchPub("widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	enrichDepsDev(facts)
	if len(facts.Degraded) != 0 {
		t.Errorf("Degraded = %v, want none", facts.Degraded)
	}
	if facts.AdvisoryCount != nil {
		t.Errorf("AdvisoryCount = %d, want nil", *facts.AdvisoryCount)
	}
}

func TestPubReadsTheRequestedVersionsOwnFacts(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage, "/widgets/score": pubScoreBody})

	facts, err := fetchPub("widgets", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested == nil || requested.Exists == nil || !*requested.Exists {
		t.Fatalf("requested = %+v, want an existing version", requested)
	}
	if requested.IsLatest {
		t.Error("1.0.0 reported as the latest release")
	}
	if requested.ReleaseDate == nil || requested.ReleaseDate.Format("2006-01-02") != "2023-01-04" {
		t.Errorf("release date = %v, want 2023-01-04", requested.ReleaseDate)
	}

	facts, err = fetchPub("widgets", "1.6.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.IsLatest {
		t.Error("1.6.0 is the latest and should be reported as such")
	}
}

// A retraction keeps a version out of the release history, the same way a
// crates.io yank does — and must not keep it out of the answer.
func TestPubAnswersAboutARetractedVersion(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage, "/widgets/score": pubScoreBody})

	facts, err := fetchPub("widgets", "1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true", requested.Exists)
	}
	if !requested.Yanked || requested.DeprecationMessage == "" {
		t.Errorf("requested = %+v, want a retracted version with a reason", requested)
	}
	if requested.ReleaseDate == nil {
		t.Error("no release date for a retracted version")
	}
}

func TestPubUnknownVersionIsDeniedOnlyByItsOwnEndpoint(t *testing.T) {
	pubServer(t, map[string]string{"/widgets": pubPackage, "/widgets/score": pubScoreBody})

	facts, err := fetchPub("widgets", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists == nil || *facts.Requested.Exists {
		t.Fatalf("exists = %v, want false once the version endpoint 404s", facts.Requested.Exists)
	}
}

func TestPubVersionOutageLeavesExistenceUnknown(t *testing.T) {
	pubServer(t, map[string]string{
		"/widgets":                pubPackage,
		"/widgets/score":          pubScoreBody,
		"/widgets/versions/0.9.0": "STATUS:503:service unavailable",
	})

	facts, err := fetchPub("widgets", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists != nil {
		t.Errorf("exists = %v, want unknown after a 503", *facts.Requested.Exists)
	}
}
