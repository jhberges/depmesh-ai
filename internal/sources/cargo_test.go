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

	facts, err := fetchCargo("somecrate")
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

	facts, err := fetchCargo("somecrate")
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

	facts, err := fetchCargo("somecrate")
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

	facts, err := fetchCargo("somecrate")
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

	facts, err := fetchCargo("no-such-crate")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

func TestCargoOutageIsNotAbsence(t *testing.T) {
	cargoServer(t, map[string]string{"/somecrate": "STATUS:429:slow down"})

	facts, err := fetchCargo("somecrate")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}
