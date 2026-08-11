package model

import "testing"

func TestSplitCoordinate(t *testing.T) {
	cases := []struct {
		ecosystem  Ecosystem
		coordinate string
		name       string
		version    string
	}{
		// The form a Gradle or Maven error message prints.
		{Maven, "org.springframework.boot:spring-boot-starter-parent:3.2.2",
			"org.springframework.boot:spring-boot-starter-parent", "3.2.2"},
		{Maven, "org.apache.commons:commons-lang3", "org.apache.commons:commons-lang3", ""},
		// A longer coordinate is `g:a:packaging:classifier:version`; reading
		// its third segment as a version would name the packaging instead.
		{Maven, "org.example:widget:jar:sources:1.0", "org.example:widget:jar:sources:1.0", ""},

		{NPM, "express@4.18.2", "express", "4.18.2"},
		// Scoped names start with the same character the version marker uses.
		{NPM, "@types/node", "@types/node", ""},
		{NPM, "@types/node@20.1.0", "@types/node", "20.1.0"},
		{NPM, "express", "express", ""},

		{PyPI, "requests==2.31.0", "requests", "2.31.0"},
		{PyPI, "requests", "requests", ""},

		{Go, "github.com/BurntSushi/toml@v1.3.2", "github.com/BurntSushi/toml", "v1.3.2"},
		{Cargo, "serde@1.0.197", "serde", "1.0.197"},
		{NuGet, "Newtonsoft.Json@13.0.3", "Newtonsoft.Json", "13.0.3"},
		{Hex, "phoenix@1.7.10", "phoenix", "1.7.10"},

		// Composer and pub both spell it with a colon.
		{Packagist, "monolog/monolog:2.0.0", "monolog/monolog", "2.0.0"},
		{Packagist, "monolog/monolog", "monolog/monolog", ""},
		{Pub, "http:1.2.0", "http", "1.2.0"},

		// A trailing marker with nothing after it is not a version.
		{NPM, "express@", "express@", ""},
		{Pub, "http:", "http:", ""},
	}
	for _, c := range cases {
		name, version := SplitCoordinate(c.ecosystem, c.coordinate)
		if name != c.name || version != c.version {
			t.Errorf("SplitCoordinate(%s, %q) = (%q, %q), want (%q, %q)",
				c.ecosystem, c.coordinate, name, version, c.name, c.version)
		}
	}
}

// Ranges survive intact, so they are refused once — by sources.Gather, with an
// error that says to resolve them first — rather than being second-guessed
// here into a name that was never asked about.
func TestSplitCoordinateLeavesRangesToBeRefused(t *testing.T) {
	name, version := SplitCoordinate(NPM, "express@^4.18.0")
	if name != "express" || version != "^4.18.0" {
		t.Errorf("got (%q, %q), want the range preserved", name, version)
	}
	if !IsConstraint(version) {
		t.Errorf("%q should read as a constraint", version)
	}

	// PyPI's other operators are ranges, and "==" is the only pin. Splitting
	// on a bare "=" would turn ">=2.31.0" into the version "2.31.0", which is
	// a different and much more permissive question than the one asked.
	if name, version := SplitCoordinate(PyPI, "requests>=2.31.0"); version != "" {
		t.Errorf("got (%q, %q), want the whole string left alone", name, version)
	}
}
