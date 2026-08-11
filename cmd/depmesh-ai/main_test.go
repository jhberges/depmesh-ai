package main

import "testing"

// The CLI's job here is small and easy to get subtly wrong: read the version
// off a pasted coordinate, let an explicit flag win, and never leave the
// suffix in the package name.
func TestCoordinate(t *testing.T) {
	cases := []struct {
		ecosystem, pkg, flag string
		name, version        string
	}{
		{"maven", "org.springframework.boot:spring-boot-starter-parent:3.2.2", "",
			"org.springframework.boot:spring-boot-starter-parent", "3.2.2"},
		{"npm", "express@4.18.2", "", "express", "4.18.2"},
		{"npm", "@types/node", "", "@types/node", ""},
		{"pypi", "requests==2.31.0", "", "requests", "2.31.0"},

		// The flag wins, and the suffix still comes off — otherwise the
		// lookup would be for a package literally called "express@4.18.2".
		{"npm", "express@4.18.2", "5.0.0", "express", "5.0.0"},
		{"npm", "express", "4.18.2", "express", "4.18.2"},

		// An ecosystem nothing can parse is left alone: the gate reports that,
		// with the list of the ones that exist, and a split guessed from a
		// typo would only change which error comes back.
		{"npmm", "express@4.18.2", "", "express@4.18.2", ""},
	}
	for _, c := range cases {
		name, version := coordinate(c.ecosystem, c.pkg, c.flag)
		if name != c.name || version != c.version {
			t.Errorf("coordinate(%q, %q, %q) = (%q, %q), want (%q, %q)",
				c.ecosystem, c.pkg, c.flag, name, version, c.name, c.version)
		}
	}
}
