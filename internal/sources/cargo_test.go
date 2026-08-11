package sources

import (
	"errors"
	"strings"
	"testing"
)

// crates.io's shape: the crate, then every version with a date. This one has
// a pre-release newer than max_stable_version and a yanked version in the
// middle of otherwise healthy history.
const cargoCrate = `{
  "crate": {
    "max_stable_version": "1.2.0",
    "newest_version": "2.0.0-rc1",
    "description": "A crate.",
    "homepage": "https://example.invalid",
    "repository": "https://github.com/example/somecrate",
    "yanked": false
  },
  "versions": [
    {"num": "2.0.0-rc1", "created_at": "2024-05-01T10:00:00.123456Z",
     "license": "MIT", "yanked": false},
    {"num": "1.2.0", "created_at": "2024-01-15T10:00:00.123456Z",
     "license": "MIT OR Apache-2.0", "yanked": false},
    {"num": "1.1.0", "created_at": "2023-08-02T10:00:00.123456Z",
     "license": "MIT", "yanked": true, "yank_message": "botched release"},
    {"num": "1.0.0", "created_at": "2023-01-04T10:00:00.123456Z",
     "license": "MIT", "yanked": false}
  ]
}`

func cargoServer(t *testing.T, routes map[string]string) {
	t.Helper()
	server := registry(t, routes)
	previous := cratesRegistry
	cratesRegistry = server.URL
	t.Cleanup(func() { cratesRegistry = previous })
}

func TestCargoReadsACrate(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	facts, err := fetchCargo("somecrate", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	// max_stable_version, not the newer release candidate.
	if facts.LatestVersion != "1.2.0" {
		t.Errorf("LatestVersion = %q, want 1.2.0", facts.LatestVersion)
	}
	if facts.License != "MIT OR Apache-2.0" {
		t.Errorf("License = %q, want the latest stable's", facts.License)
	}
	if facts.RepositoryURL != "https://github.com/example/somecrate" {
		t.Errorf("RepositoryURL = %q", facts.RepositoryURL)
	}
	if got := day(t, release(t, facts, "1.0.0")); got != "2023-01-04" {
		t.Errorf("1.0.0 released %s, want 2023-01-04", got)
	}
	if facts.MaintainerCount != nil {
		t.Errorf("MaintainerCount = %d, want nil (owners are a second request)", *facts.MaintainerCount)
	}
}

// A yank withdraws a version from resolution. Counting it would credit the
// crate for a release nobody can install — but one yank among healthy
// releases is a retraction, not a deprecation.
func TestCargoExcludesYankedVersionsWithoutDeprecating(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	facts, err := fetchCargo("somecrate", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range facts.Releases {
		if ref.Version == "1.1.0" {
			t.Fatalf("yanked 1.1.0 is in %v", versions(facts))
		}
	}
	if facts.Deprecated {
		t.Error("one yanked version among healthy ones is not a deprecation")
	}
}

// A crate with nothing left to resolve is deprecation-shaped, and that is the
// only yank state that earns the penalty.
func TestCargoTreatsAFullyYankedCrateAsDeprecated(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": `{
	  "crate": {"max_stable_version": "1.0.0", "newest_version": "1.0.0", "yanked": true},
	  "versions": [{"num": "1.0.0", "created_at": "2023-01-04T10:00:00Z",
	                "license": "MIT", "yanked": true, "yank_message": "name squat, sorry"}]
	}`})

	facts, err := fetchCargo("somecrate", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if !facts.Deprecated {
		t.Fatal("a fully yanked crate should be deprecated")
	}
	if !strings.Contains(facts.DeprecationMessage, "name squat") {
		t.Errorf("DeprecationMessage = %q, want the yank message", facts.DeprecationMessage)
	}
	if len(facts.Releases) != 0 {
		t.Errorf("Releases = %v, want none resolvable", versions(facts))
	}
}

// max_stable_version can itself be yanked, and then it is not what anyone
// would add to a Cargo.toml.
func TestCargoFallsBackWhenTheStableVersionIsYanked(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": `{
	  "crate": {"max_stable_version": "1.2.0", "newest_version": "1.2.0", "yanked": false},
	  "versions": [
	    {"num": "1.2.0", "created_at": "2024-01-15T10:00:00Z", "license": "MIT", "yanked": true},
	    {"num": "1.1.0", "created_at": "2023-08-02T10:00:00Z", "license": "MIT", "yanked": false}
	  ]
	}`})

	facts, err := fetchCargo("somecrate", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.LatestVersion != "1.1.0" {
		t.Errorf("LatestVersion = %q, want the newest resolvable 1.1.0", facts.LatestVersion)
	}
	if facts.Deprecated {
		t.Error("a yanked head with a usable predecessor is not a deprecation")
	}
}

func TestCargoReportsAbsence(t *testing.T) {
	cargoServer(t, map[string]string{})

	facts, err := fetchCargo("no-such-crate", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestCargoOutageIsNotAbsence(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": "STATUS:429:slow down"})

	facts, err := fetchCargo("somecrate", "")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

func TestCargoReadsTheRequestedVersionsOwnFacts(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	// 1.0.0's license differs from the latest release's, which is the whole
	// point of asking per version: relicensing changes what a pin permits.
	facts, err := fetchCargo("somecrate", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested == nil || requested.Exists == nil || !*requested.Exists {
		t.Fatalf("requested = %+v, want an existing version", requested)
	}
	if requested.License != "MIT" {
		t.Errorf("license = %q, want MIT", requested.License)
	}
	if requested.IsLatest {
		t.Error("1.0.0 reported as the latest release")
	}
	if requested.ReleaseDate == nil || requested.ReleaseDate.Format("2006-01-02") != "2023-01-04" {
		t.Errorf("release date = %v, want 2023-01-04", requested.ReleaseDate)
	}

	facts, err = fetchCargo("somecrate", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.IsLatest {
		t.Error("1.2.0 is max_stable_version and should be reported as latest")
	}
}

// A yank keeps a version out of the release history on purpose. It must not
// keep it out of the answer: pinning a yanked version is the case that most
// needs one.
func TestCargoAnswersAboutAYankedVersion(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	facts, err := fetchCargo("somecrate", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true", requested.Exists)
	}
	if !requested.Yanked {
		t.Error("Yanked = false for a version yanked from crates.io")
	}
	if !strings.Contains(requested.DeprecationMessage, "botched release") {
		t.Errorf("message = %q, want the yank message", requested.DeprecationMessage)
	}
	// The date is not in facts.Releases either, and the currency signal needs it.
	if requested.ReleaseDate == nil {
		t.Error("no release date for a yanked version")
	}
}

func TestCargoToleratesALeadingV(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	facts, err := fetchCargo("somecrate", "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Resolved != "1.2.0" {
		t.Errorf("resolved = %q, want the registry's spelling", facts.Requested.Resolved)
	}
}

func TestCargoUnknownVersionIsDeniedOnlyByThePerVersionEndpoint(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": cargoCrate})

	facts, err := fetchCargo("somecrate", "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists == nil || *facts.Requested.Exists {
		t.Fatalf("exists = %v, want false once the version endpoint 404s", facts.Requested.Exists)
	}
}

// crates.io has been moving toward paginating the version list, so a version
// missing from the crate response may simply not be in the page we were given.
func TestCargoVersionMissingFromTheListIsRecoveredByItsOwnEndpoint(t *testing.T) {
	cargoServer(t, map[string]string{
		"/somecrate": cargoCrate,
		"/somecrate/0.9.0": `{"version": {"num": "0.9.0", "license": "MIT",
		  "created_at": "2022-01-04T10:00:00.000000Z", "yanked": false}}`,
	})

	facts, err := fetchCargo("somecrate", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true", requested.Exists)
	}
	if requested.License != "MIT" || requested.ReleaseDate == nil {
		t.Errorf("requested = %+v, want the endpoint's license and date", requested)
	}
}

func TestCargoVersionOutageLeavesExistenceUnknown(t *testing.T) {
	cargoServer(t, map[string]string{
		"/somecrate":       cargoCrate,
		"/somecrate/0.9.0": "STATUS:503:service unavailable",
	})

	facts, err := fetchCargo("somecrate", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists != nil {
		t.Errorf("exists = %v, want unknown after a 503", *facts.Requested.Exists)
	}
	if len(facts.Degraded) == 0 {
		t.Error("an unconfirmed version should be reported in Degraded")
	}
}
