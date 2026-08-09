package model

import (
	"strings"
	"testing"
)

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
	if got := EcosystemList(); got != "npm, pypi, maven, nuget, cargo or go" {
		t.Errorf("EcosystemList() = %q", got)
	}
}
