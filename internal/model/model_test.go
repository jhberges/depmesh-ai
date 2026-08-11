package model

import (
	"strings"
	"testing"
)

func TestIsPrerelease(t *testing.T) {
	for _, version := range []string{
		"1.0.0-rc1", "1.0.0-RC1", "2.0.0-alpha.3", "1.0.0-beta", "1.0-M3",
		"2.0.0.CR1", "1.0a1", "2.0b3", "0.1.0-dev", "1.0.0-canary.7",
		"3.0.0-next.2", "1.0-SNAPSHOT", "1.0.0-milestone1", "1.0.0-ea",
	} {
		if !IsPrerelease(version) {
			t.Errorf("%q not recognized as a prerelease", version)
		}
	}
	// Releases that look prerelease-ish and are not. Misreading one of these
	// would drop a real release out of a release count.
	for _, version := range []string{
		"1.0.0", "4.18.2", "1.0.0.Final", "2.31.0", "0.0.1", "1.0.0+build.7",
		"20240115", "1.2.3-4",
	} {
		if IsPrerelease(version) {
			t.Errorf("%q wrongly flagged as a prerelease", version)
		}
	}
}

func TestIsSnapshot(t *testing.T) {
	if !IsSnapshot("1.0.0-SNAPSHOT") || !IsSnapshot("2.0-snapshot") {
		t.Error("snapshot not recognized")
	}
	if IsSnapshot("1.0.0") {
		t.Error("release wrongly read as a snapshot")
	}
}

func TestIsConstraint(t *testing.T) {
	for _, version := range []string{
		"^4.18.0", "~2.31", ">=1.0.0", "[1.0,2.0)", "1.+", "1.2.+", "*",
		"latest.release", "latest", "1.0.0 || 2.0.0", "!=1.0", "1.0,2.0",
	} {
		if !IsConstraint(version) {
			t.Errorf("%q not recognized as a constraint", version)
		}
	}
	// Exact versions, including the awkward ones. A false positive here refuses
	// to vet a perfectly good coordinate.
	for _, version := range []string{
		"1.0.0", "1.0.0.Final", "2.0-20240115.RC3", "4.18.2", "1.0.0-SNAPSHOT",
		"1.0.0+build.7", "2.31.0.post1", "v1.2.3",
	} {
		if IsConstraint(version) {
			t.Errorf("%q wrongly flagged as a constraint", version)
		}
	}
}

func TestParseEcosystemIsCaseInsensitive(t *testing.T) {
	for _, value := range []string{"npm", "NPM", "NuGet", "nuget"} {
		if _, err := ParseEcosystem(value); err != nil {
			t.Errorf("ParseEcosystem(%q) = %v", value, err)
		}
	}
}

// The error is the only place a user learns what they could have typed, so it
// has to name the ecosystems this build actually supports rather than a list
// somebody forgot to extend.
func TestUnknownEcosystemNamesTheAlternatives(t *testing.T) {
	_, err := ParseEcosystem("cpan")
	if err == nil {
		t.Fatal("cpan is not supported; want an error")
	}
	for _, ecosystem := range EcosystemStrings() {
		if !strings.Contains(err.Error(), ecosystem) {
			t.Errorf("%q does not mention %q", err, ecosystem)
		}
	}
}

func TestEveryEcosystemParses(t *testing.T) {
	for _, ecosystem := range Ecosystems {
		got, err := ParseEcosystem(string(ecosystem))
		if err != nil || got != ecosystem {
			t.Errorf("ParseEcosystem(%q) = %q, %v", ecosystem, got, err)
		}
	}
}

func TestEcosystemList(t *testing.T) {
	if got := EcosystemList(); got != "npm, pypi, maven, nuget, cargo, go, packagist, pub or hex" {
		t.Errorf("EcosystemList() = %q", got)
	}
}
