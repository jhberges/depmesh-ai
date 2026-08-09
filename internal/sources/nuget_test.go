package sources

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Shapes taken from api.nuget.org: an index that inlines its pages (what
// Newtonsoft.Json returns), with an unlisted version, a pre-release newer than
// the newest stable, and a deprecation record on the latest stable.
const nugetInlineIndex = `{
  "count": 1,
  "items": [
    {
      "@id": "https://example.invalid/inline#page/1.0.0/3.0.0-beta1",
      "items": [
        {"catalogEntry": {
          "version": "1.0.0", "published": "2019-03-04T10:00:00.12+00:00",
          "licenseExpression": "MIT", "authors": "A. Hacker",
          "description": "A library.", "projectUrl": "https://example.invalid/proj",
          "listed": true}},
        {"catalogEntry": {
          "version": "1.5.0", "published": "1900-01-01T00:00:00+00:00",
          "licenseExpression": "MIT", "listed": false}},
        {"catalogEntry": {
          "version": "2.0.0", "published": "2021-06-01T10:00:00+00:00",
          "licenseExpression": "MIT", "description": "A library.",
          "projectUrl": "https://example.invalid/proj",
          "deprecation": {"message": "Use Other.Package instead.",
                          "reasons": ["Legacy", "Other"]}}},
        {"catalogEntry": {
          "version": "3.0.0-beta1", "published": "2021-09-09T10:00:00+00:00",
          "licenseExpression": "MIT"}}
      ]
    }
  ]
}`

func nugetServer(t *testing.T, routes map[string]string) {
	t.Helper()
	server := registry(t, routes)
	previous := nugetRegistration
	nugetRegistration = server.URL
	t.Cleanup(func() { nugetRegistration = previous })
}

func TestNuGetReadsAnInlinedIndex(t *testing.T) {
	nugetServer(t, map[string]string{"/somelib/index.json": nugetInlineIndex})

	facts, err := fetchNuGet("SomeLib")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)

	// The newest stable, not the newer pre-release: LatestVersion is what a
	// reader would install and what advisories are looked up against.
	if facts.LatestVersion != "2.0.0" {
		t.Errorf("LatestVersion = %q, want 2.0.0", facts.LatestVersion)
	}
	if facts.License != "MIT" {
		t.Errorf("License = %q, want MIT", facts.License)
	}
	if facts.Homepage != "https://example.invalid/proj" {
		t.Errorf("Homepage = %q", facts.Homepage)
	}
	if !facts.Deprecated || !strings.Contains(facts.DeprecationMessage, "Other.Package") {
		t.Errorf("Deprecated = %v (%q), want the latest stable's deprecation",
			facts.Deprecated, facts.DeprecationMessage)
	}
	// Releases is newest-first, and the pre-release stays in it: staleness
	// asks whether anyone has touched the package at all.
	if got := versions(facts); len(got) != 3 || got[0] != "3.0.0-beta1" {
		t.Errorf("versions = %v, want 3 newest-first starting at 3.0.0-beta1", got)
	}
	if got := day(t, release(t, facts, "1.0.0")); got != "2019-03-04" {
		t.Errorf("1.0.0 released %s, want 2019-03-04", got)
	}
	// authors is a display string, so a bus-factor number would be invented.
	if facts.MaintainerCount != nil {
		t.Errorf("MaintainerCount = %d, want nil", *facts.MaintainerCount)
	}
}

// An unlisted version is hidden from search and resolution; counting it would
// credit the package for a release nobody can install.
func TestNuGetSkipsUnlistedVersions(t *testing.T) {
	nugetServer(t, map[string]string{"/somelib/index.json": nugetInlineIndex})

	facts, err := fetchNuGet("SomeLib")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range facts.Releases {
		if ref.Version == "1.5.0" {
			t.Fatalf("unlisted 1.5.0 is in %v", versions(facts))
		}
	}
}

// Serilog's shape: ten pages, each its own document. The budget is four, so
// the adapter has to choose which history it can afford.
func TestNuGetFetchesPagesThatAreNotInlined(t *testing.T) {
	const pages = 6
	// The index has to name the server's own address, which is only known
	// once it is listening — so it is built per request from r.Host.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/somelib/index.json" {
			var refs []string
			for i := 1; i <= pages; i++ {
				refs = append(refs, fmt.Sprintf(`{"@id": "http://%s/somelib/page/%d.json"}`, r.Host, i))
			}
			fmt.Fprintf(w, `{"items": [%s]}`, strings.Join(refs, ","))
			return
		}
		var page int
		if _, err := fmt.Sscanf(r.URL.Path, "/somelib/page/%d.json", &page); err != nil {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"items": [{"catalogEntry": {"version": "%d.0.0",
			"published": "20%02d-01-01T00:00:00+00:00", "licenseExpression": "MIT"}}]}`,
			page, page)
	}))
	t.Cleanup(server.Close)

	previous := nugetRegistration
	nugetRegistration = server.URL
	t.Cleanup(func() { nugetRegistration = previous })

	facts, err := fetchNuGet("SomeLib")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	// The budget buys the oldest page — the first-publication date the
	// young-package signal reads — and the three newest, which decide the
	// latest version, staleness and recent cadence.
	got := versions(facts)
	if len(got) != maxNuGetPages {
		t.Fatalf("fetched %v, want %d pages' worth", got, maxNuGetPages)
	}
	if got[0] != "6.0.0" || got[len(got)-1] != "1.0.0" {
		t.Errorf("versions = %v, want newest 6.0.0 and oldest 1.0.0", got)
	}
	if facts.LatestVersion != "6.0.0" {
		t.Errorf("LatestVersion = %q, want 6.0.0", facts.LatestVersion)
	}
	if len(facts.Degraded) == 0 {
		t.Error("skipping history should be reported in Degraded")
	}
}

func TestNuGetReportsAbsence(t *testing.T) {
	nugetServer(t, map[string]string{})

	facts, err := fetchNuGet("no-such-package")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

// The rule the whole tool rests on: an unreachable or rate-limited registry is
// unknown, never absent. Reading a 503 as "does not exist" would turn an
// outage into a slopsquatting false alarm.
func TestNuGetOutageIsNotAbsence(t *testing.T) {
	nugetServer(t, map[string]string{"/somelib/index.json": "STATUS:503:busy"})

	facts, err := fetchNuGet("SomeLib")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

// A page that will not load costs history, not the verdict.
func TestNuGetSurvivesAnUnreadablePage(t *testing.T) {
	nugetServer(t, map[string]string{
		"/somelib/index.json": `{"items": [{"@id": "http://127.0.0.1:1/page.json"}]}`,
	})

	facts, err := fetchNuGet("SomeLib")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if len(facts.Degraded) == 0 {
		t.Error("an unread page should be reported in Degraded")
	}
}

func TestSelectNuGetPages(t *testing.T) {
	inlined := nugetPage{Items: []nugetLeaf{{}}}
	remote := nugetPage{ID: "x"}

	cases := []struct {
		name  string
		pages []nugetPage
		want  []int
	}{
		{"all inlined costs nothing", []nugetPage{inlined, inlined}, nil},
		{"under budget takes everything", []nugetPage{remote, inlined, remote}, []int{0, 2}},
		{"over budget keeps the oldest and the newest",
			[]nugetPage{remote, remote, remote, remote, remote, remote}, []int{0, 3, 4, 5}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := selectNuGetPages(testCase.pages, 4)
			if len(got) != len(testCase.want) {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("got %v, want %v", got, testCase.want)
				}
			}
		})
	}
}
