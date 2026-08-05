package apikey

import (
	"strings"
	"testing"
)

func TestGenerateIsUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		key, err := Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !strings.HasPrefix(key, Prefix) {
			t.Fatalf("key %q lacks the %q prefix", key, Prefix)
		}
		if len(key) < 40 {
			t.Fatalf("key %q is too short to be worth guessing against", key)
		}
		if seen[key] {
			t.Fatalf("generated a duplicate key: %q", key)
		}
		seen[key] = true
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		name                  string
		configured, presented string
		want                  bool
	}{
		{"no key configured allows anything", "", "whatever", true},
		{"no key configured allows nothing presented", "", "", true},
		{"exact match", "dmk_abc", "dmk_abc", true},
		{"wrong key", "dmk_abc", "dmk_xyz", false},
		{"nothing presented", "dmk_abc", "", false},
		{"prefix of the real key", "dmk_abcdef", "dmk_abc", false},
		{"real key plus trailing junk", "dmk_abc", "dmk_abcd", false},
		{"case matters", "dmk_abc", "DMK_ABC", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Matches(testCase.configured, testCase.presented); got != testCase.want {
				t.Fatalf("Matches(%q, %q) = %v, want %v",
					testCase.configured, testCase.presented, got, testCase.want)
			}
		})
	}
}

func TestFromHeader(t *testing.T) {
	cases := map[string]string{
		"Bearer dmk_abc":   "dmk_abc",
		"bearer dmk_abc":   "dmk_abc", // the scheme is case-insensitive
		"BEARER  dmk_abc ": "dmk_abc",
		"dmk_abc":          "dmk_abc", // bare, for a hand-run curl
		"  dmk_abc  ":      "dmk_abc",
		"Bearer":           "",          // the scheme with no key is no key
		"bearerish":        "bearerish", // not the scheme, just a key that rhymes
		"Bearer ":          "",
		"":                 "",
	}
	for header, want := range cases {
		if got := FromHeader(header); got != want {
			t.Fatalf("FromHeader(%q) = %q, want %q", header, got, want)
		}
	}
}
