package sources

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

func factsWithHistory() *model.PackageFacts {
	released := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	yanked := model.ReleaseRef{Version: "1.1.0", ReleaseDate: &released, Yanked: true}
	return &model.PackageFacts{
		Ecosystem:     model.PyPI,
		Name:          "example",
		Exists:        model.Bool(true),
		LatestVersion: "2.0.0",
		Releases: []model.ReleaseRef{
			{Version: "2.0.0", ReleaseDate: &released},
			yanked,
			{Version: "1.0.0", ReleaseDate: &released},
		},
	}
}

func TestRequestedVersionCopiesReleaseFacts(t *testing.T) {
	facts := factsWithHistory()
	vf := requestedVersion(facts, "1.1.0", "1.1.0", nil)
	if vf.Exists == nil || !*vf.Exists {
		t.Fatalf("exists = %v, want true", vf.Exists)
	}
	if vf.IsLatest {
		t.Error("1.1.0 reported as the latest release")
	}
	if !vf.Yanked {
		t.Error("yank status not carried over from the release history")
	}
	if vf.ReleaseDate == nil {
		t.Error("release date not carried over")
	}
	if latest := requestedVersion(facts, "2.0.0", "2.0.0", nil); !latest.IsLatest {
		t.Error("2.0.0 not reported as the latest release")
	}
}

func TestRequestedVersionSnapshotLeavesExistenceUnknown(t *testing.T) {
	facts := factsWithHistory()
	// Release metadata legitimately omits snapshots, so its silence is not
	// evidence of anything. Confirming must not even be attempted.
	vf := requestedVersion(facts, "1.0.0-SNAPSHOT", "", func() (string, error) {
		t.Fatal("confirmed existence of a snapshot")
		return "", nil
	})
	if !vf.Snapshot {
		t.Error("snapshot not flagged")
	}
	if vf.Exists != nil {
		t.Errorf("exists = %v, want unknown", *vf.Exists)
	}
}

func TestRequestedVersionMissWithAuthoritativeEnumeration(t *testing.T) {
	// confirm == nil says the list in hand is complete, as npm's packument is.
	facts := factsWithHistory()
	vf := requestedVersion(facts, "9.9.9", "", nil)
	if vf.Exists == nil || *vf.Exists {
		t.Fatalf("exists = %v, want false", vf.Exists)
	}
}

func TestRequestedVersionMissIsConfirmedBeforeBeingDenied(t *testing.T) {
	facts := factsWithHistory()
	calls := 0
	vf := requestedVersion(facts, "9.9.9", "", func() (string, error) {
		calls++
		return "", fmt.Errorf("pypi: %w", ErrNotFound)
	})
	if calls != 1 {
		t.Fatalf("confirm called %d times, want 1", calls)
	}
	if vf.Exists == nil || *vf.Exists {
		t.Fatalf("exists = %v, want false after an authoritative 404", vf.Exists)
	}
}

func TestRequestedVersionMissRecoveredByConfirm(t *testing.T) {
	// The list was stale or our normalization missed. The registry knows the
	// version, and its own spelling is what everything downstream compares.
	facts := factsWithHistory()
	vf := requestedVersion(facts, "1.0", "", func() (string, error) { return "1.0.0", nil })
	if vf.Exists == nil || !*vf.Exists {
		t.Fatalf("exists = %v, want true", vf.Exists)
	}
	if vf.Resolved != "1.0.0" {
		t.Fatalf("resolved = %q, want the registry's spelling 1.0.0", vf.Resolved)
	}
	if vf.Version != "1.0" {
		t.Fatalf("version = %q, want the spelling as asked", vf.Version)
	}
	if vf.ReleaseDate == nil {
		t.Error("release facts not picked up under the resolved spelling")
	}
}

func TestRequestedVersionUnreachableConfirmLeavesExistenceUnknown(t *testing.T) {
	// A failed lookup is not proof of absence. Reporting one as "this version
	// does not exist" would tell a developer their working pin is a
	// hallucination.
	facts := factsWithHistory()
	vf := requestedVersion(facts, "9.9.9", "", func() (string, error) {
		return "", errors.New("dial tcp: connection refused")
	})
	if vf.Exists != nil {
		t.Fatalf("exists = %v, want unknown", *vf.Exists)
	}
	if len(facts.Degraded) != 1 || !strings.Contains(facts.Degraded[0], "9.9.9") {
		t.Fatalf("degraded sources = %v, want the unconfirmed lookup recorded", facts.Degraded)
	}
}

func TestGatherRejectsConstraints(t *testing.T) {
	for _, constraint := range []string{"^4.18.0", "[1.0,2.0)", "1.+", "latest.release"} {
		_, err := Gather(model.NPM, "express", constraint, false)
		if err == nil {
			t.Errorf("%q accepted as a version", constraint)
			continue
		}
		if !strings.Contains(err.Error(), "resolve it first") {
			t.Errorf("%q: error does not say what to do: %v", constraint, err)
		}
	}
}

func TestPyPIKeyNormalization(t *testing.T) {
	same := [][2]string{
		{"1.0", "1.0.0"},
		{"1", "1.0.0"},
		{"v1.2.3", "1.2.3"},
		{"2.0rc1", "2.0-rc1"},
		{"2.0rc1", "2.0.rc1"},
		{"2.0rc1", "2.0_rc1"},
		{"1.0A1", "1.0a1"},
		{"1.0.0-alpha1", "1.0a1"},
		{"3.0beta2", "3.0-b2"},
	}
	for _, pair := range same {
		if pypiKey(pair[0]) != pypiKey(pair[1]) {
			t.Errorf("%q and %q normalize differently: %q vs %q",
				pair[0], pair[1], pypiKey(pair[0]), pypiKey(pair[1]))
		}
	}
	differ := [][2]string{
		{"1.0.1", "1.0.0"},
		{"2.0", "2.1"},
		{"1.0rc1", "1.0rc2"},
		{"1.0a1", "1.0b1"},
		{"1.0", "10.0"},
	}
	for _, pair := range differ {
		if pypiKey(pair[0]) == pypiKey(pair[1]) {
			t.Errorf("%q and %q collapse to the same key %q", pair[0], pair[1], pypiKey(pair[0]))
		}
	}
}
