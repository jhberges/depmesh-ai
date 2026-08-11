package sources

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// goRegistry stands in for proxy.golang.org and counts what was asked of it,
// because the whole design of this adapter is about how many requests it is
// willing to spend.
type goRegistry struct {
	mu       sync.Mutex
	requests []string
	routes   map[string]string
}

func goServer(t *testing.T, routes map[string]string) *goRegistry {
	t.Helper()
	proxy := &goRegistry{routes: routes}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.mu.Lock()
		proxy.requests = append(proxy.requests, r.URL.Path)
		proxy.mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if code, rest, isStatus := cutStatusPrefix(body); isStatus {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(rest))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	previous := goProxy
	goProxy = server.URL
	t.Cleanup(func() { goProxy = previous })
	return proxy
}

func (p *goRegistry) count(substring string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, path := range p.requests {
		if strings.Contains(path, substring) {
			n++
		}
	}
	return n
}

func TestGoReadsAModule(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":        "v1.0.0\nv1.2.0\nv1.1.0\n",
		"/example.invalid/mod/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2023-01-04T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.1.0.info": `{"Version":"v1.1.0","Time":"2023-08-02T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.2.0.mod":  "module example.invalid/mod\n\ngo 1.21\n",
	})

	facts, err := fetchGo("example.invalid/mod", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.LatestVersion != "v1.2.0" {
		t.Errorf("LatestVersion = %q, want v1.2.0", facts.LatestVersion)
	}
	// @v/list arrives unordered; Releases must not be.
	if got := versions(facts); len(got) != 3 || got[0] != "v1.2.0" || got[2] != "v1.0.0" {
		t.Errorf("versions = %v, want v1.2.0, v1.1.0, v1.0.0", got)
	}
	// @latest already carried the newest date, so it is not bought twice.
	if n := facts.Releases[0].ReleaseDate; n == nil {
		t.Error("the latest version should be dated from @latest")
	}
	if got := day(t, release(t, facts, "v1.0.0")); got != "2023-01-04" {
		t.Errorf("v1.0.0 released %s, want 2023-01-04", got)
	}
	if facts.Deprecated {
		t.Error("a plain go.mod is not a deprecation")
	}
}

// github.com/BurntSushi/toml answers 404 unescaped. Getting this wrong reports
// a real, widely used module as a hallucinated name — the worst answer this
// tool can give.
func TestGoEscapesUppercaseInModulePaths(t *testing.T) {
	proxy := goServer(t, map[string]string{
		"/github.com/!burnt!sushi/toml/@latest": `{"Version":"v1.6.0","Time":"2025-12-19T10:00:00Z"}`,
		"/github.com/!burnt!sushi/toml/@v/list": "v1.6.0\n",
	})

	facts, err := fetchGo("github.com/BurntSushi/toml", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if proxy.count("/BurntSushi/") != 0 {
		t.Errorf("asked for the unescaped path: %v", proxy.requests)
	}
	// The name is reported as the user typed it, not as the proxy wants it.
	if facts.Name != "github.com/BurntSushi/toml" {
		t.Errorf("Name = %q", facts.Name)
	}
}

func TestGoBuysABoundedNumberOfDates(t *testing.T) {
	routes := map[string]string{
		"/example.invalid/mod/@latest": `{"Version":"v1.40.0","Time":"2024-01-15T10:00:00Z"}`,
	}
	var list []string
	for i := 1; i <= 40; i++ {
		version := fmt.Sprintf("v1.%d.0", i)
		list = append(list, version)
		routes["/example.invalid/mod/@v/"+version+".info"] =
			fmt.Sprintf(`{"Version":%q,"Time":"20%02d-01-04T10:00:00Z"}`, version, i)
	}
	routes["/example.invalid/mod/@v/list"] = strings.Join(list, "\n")
	proxy := goServer(t, routes)

	facts, err := fetchGo("example.invalid/mod", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := proxy.count(".info"); got > maxGoVersionInfos {
		t.Errorf("bought %d dates, budget is %d", got, maxGoVersionInfos)
	}
	// Every version is still reported, dated or not, so the release count and
	// the ordering stay honest.
	if len(facts.Releases) != 40 {
		t.Errorf("Releases = %d, want all 40 versions", len(facts.Releases))
	}
	if facts.Releases[0].Version != "v1.40.0" {
		t.Errorf("newest = %q, want v1.40.0", facts.Releases[0].Version)
	}
	// The oldest is where BuildReleasePace looks for the first publication, so
	// it is one of the dates the budget reserves.
	oldest := facts.Releases[len(facts.Releases)-1]
	if oldest.Version != "v1.1.0" || oldest.ReleaseDate == nil {
		t.Errorf("oldest = %+v, want a dated v1.1.0", oldest)
	}
}

func TestGoReadsDeprecationFromGoMod(t *testing.T) {
	goServer(t, map[string]string{
		"/github.com/golang/protobuf/@latest": `{"Version":"v1.5.4","Time":"2024-03-06T06:45:40Z"}`,
		"/github.com/golang/protobuf/@v/list": "v1.5.4\n",
		"/github.com/golang/protobuf/@v/v1.5.4.mod": `// Deprecated: Use the "google.golang.org/protobuf" module instead.
module github.com/golang/protobuf

go 1.17
`,
	})

	facts, err := fetchGo("github.com/golang/protobuf", "")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated {
		t.Fatal("Deprecated = false, want true")
	}
	if !strings.Contains(facts.DeprecationMessage, "google.golang.org/protobuf") {
		t.Errorf("DeprecationMessage = %q", facts.DeprecationMessage)
	}
}

// The marker only counts on the block directly above the module directive; a
// deprecated *dependency* further down the file says nothing about this
// module.
func TestGoIgnoresDeprecationElsewhereInGoMod(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest": `{"Version":"v1.0.0","Time":"2024-03-06T06:45:40Z"}`,
		"/example.invalid/mod/@v/list": "v1.0.0\n",
		"/example.invalid/mod/@v/v1.0.0.mod": `module example.invalid/mod

go 1.21

// Deprecated: this dependency is going away
require example.invalid/other v1.0.0
`,
	})

	facts, err := fetchGo("example.invalid/mod", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Deprecated {
		t.Errorf("Deprecated = true (%q), want false", facts.DeprecationMessage)
	}
}

func TestGoReportsAbsence(t *testing.T) {
	goServer(t, map[string]string{})

	facts, err := fetchGo("example.invalid/nope", "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Exists == nil || *facts.Exists {
		t.Fatalf("Exists = %v, want false", facts.Exists)
	}
}

// A module the proxy knows by tag but cannot answer @latest for still exists.
// Concluding absence from one 404 would be a false slopsquat alarm.
func TestGoDoesNotConcludeAbsenceFromLatestAlone(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@v/list":        "v1.0.0\n",
		"/example.invalid/mod/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2023-01-04T10:00:00Z"}`,
	})

	facts, err := fetchGo("example.invalid/mod", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExist(t, facts)
	if facts.LatestVersion != "v1.0.0" {
		t.Errorf("LatestVersion = %q, want v1.0.0", facts.LatestVersion)
	}
}

func TestGoOutageIsNotAbsence(t *testing.T) {
	goServer(t, map[string]string{"/example.invalid/mod/@latest": "STATUS:503:down"})

	facts, err := fetchGo("example.invalid/mod", "")
	if facts != nil {
		t.Fatalf("facts = %+v, want nil", facts)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want UnavailableError", err)
	}
}

// Lexical order is wrong in exactly the way that matters, so the ordering is
// pinned rather than assumed.
func TestCompareGoVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.10.0", "v0.9.0", 1}, // the one lexical sorting gets backwards
		{"v1.2.0", "v1.2.0", 0},
		{"v2.0.0", "v10.0.0", -1},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.0.0", "v1.0.0-rc1", 1}, // a release outranks its pre-releases
		{"v1.0.0-rc1", "v1.0.0-rc2", -1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1}, // numeric identifiers compare numerically
		// "rc10" is one alphanumeric identifier, not a number, so SemVer
		// compares it lexically and rc10 sorts *below* rc2. That is the spec,
		// and what golang.org/x/mod/semver does; tags meaning otherwise write
		// "rc.10".
		{"v1.0.0-rc10", "v1.0.0-rc2", -1},
		{"v1.0.0-alpha", "v1.0.0-1", 1}, // alphanumeric outranks numeric
		{"v1.0.0-rc1", "v1.0.0-rc1.1", -1},
		{"v2.0.0+incompatible", "v2.0.0", 0}, // build metadata is not precedence
		{"v0.0.0-20200101120000-abcdef123456", "v0.0.0-20210101120000-abcdef123456", -1},
	}
	for _, testCase := range cases {
		if got := compareGoVersions(testCase.a, testCase.b); got != testCase.want {
			t.Errorf("compare(%q, %q) = %d, want %d", testCase.a, testCase.b, got, testCase.want)
		}
		if got := compareGoVersions(testCase.b, testCase.a); got != -testCase.want {
			t.Errorf("compare(%q, %q) = %d, want %d (not antisymmetric)",
				testCase.b, testCase.a, got, -testCase.want)
		}
	}
}

func TestGoReadsTheRequestedVersionsOwnFacts(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":        "v1.0.0\nv1.2.0\nv1.1.0\n",
		"/example.invalid/mod/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2023-01-04T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.1.0.info": `{"Version":"v1.1.0","Time":"2023-08-02T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.1.0.mod":  "module example.invalid/mod\n\ngo 1.21\n",
		"/example.invalid/mod/@v/v1.2.0.mod":  "module example.invalid/mod\n\ngo 1.21\n",
	})

	facts, err := fetchGo("example.invalid/mod", "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested == nil || requested.Exists == nil || !*requested.Exists {
		t.Fatalf("requested = %+v, want an existing version", requested)
	}
	if requested.IsLatest {
		t.Error("v1.1.0 reported as the latest release")
	}
	if requested.ReleaseDate == nil || requested.ReleaseDate.Format("2006-01-02") != "2023-08-02" {
		t.Errorf("release date = %v, want 2023-08-02", requested.ReleaseDate)
	}
}

// Module versions are canonically "v"-prefixed, but lockfiles and humans drop it.
func TestGoToleratesAMissingV(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":       `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":       "v1.0.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.2.0.mod": "module example.invalid/mod\n",
	})

	facts, err := fetchGo("example.invalid/mod", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Resolved != "v1.2.0" {
		t.Errorf("resolved = %q, want v1.2.0", facts.Requested.Resolved)
	}
	if !facts.Requested.IsLatest {
		t.Error("v1.2.0 is the latest and should be reported as such")
	}
}

// @v/list carries tagged versions. A go.mod pinning an untagged commit holds a
// pseudo-version that is never in it and resolves perfectly well, so a miss
// there is not grounds for calling a working pin a hallucination.
func TestGoPseudoVersionIsConfirmedRatherThanDenied(t *testing.T) {
	const pseudo = "v0.0.0-20240115120000-abcdef123456"
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":                `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":                "v1.0.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.2.0.mod":          "module example.invalid/mod\n",
		"/example.invalid/mod/@v/" + pseudo + ".info": `{"Version":"` + pseudo + `","Time":"2024-01-15T12:00:00Z"}`,
		"/example.invalid/mod/@v/" + pseudo + ".mod":  "module example.invalid/mod\n",
	})

	facts, err := fetchGo("example.invalid/mod", pseudo)
	if err != nil {
		t.Fatal(err)
	}
	requested := facts.Requested
	if requested.Exists == nil || !*requested.Exists {
		t.Fatalf("exists = %v, want true", requested.Exists)
	}
	if requested.ReleaseDate == nil {
		t.Error("no release date for a confirmed pseudo-version")
	}
}

func TestGoUnknownVersionIsDeniedOnlyByItsOwnInfo(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":       `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":       "v1.0.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.2.0.mod": "module example.invalid/mod\n",
	})

	facts, err := fetchGo("example.invalid/mod", "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists == nil || *facts.Requested.Exists {
		t.Fatalf("exists = %v, want false once .info 404s", facts.Requested.Exists)
	}
}

func TestGoVersionOutageLeavesExistenceUnknown(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":        "v1.0.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.2.0.mod":  "module example.invalid/mod\n",
		"/example.invalid/mod/@v/v9.9.9.info": "STATUS:503:service unavailable",
	})

	facts, err := fetchGo("example.invalid/mod", "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Exists != nil {
		t.Errorf("exists = %v, want unknown after a 503", *facts.Requested.Exists)
	}
}

// Retractions are Go's yank: the version stays downloadable — the proxy is
// immutable, so it must — and is announced as unusable from the latest go.mod,
// which is already fetched for the deprecation marker.
func TestGoReadsRetractionsFromTheLatestGoMod(t *testing.T) {
	const mod = `module example.invalid/mod

go 1.21

retract v1.1.0 // published by mistake

retract (
	[v0.9.0, v1.0.0] // broken on Windows
)
`
	routes := map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":        "v0.9.0\nv1.0.0\nv1.1.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.2.0.mod":  mod,
		"/example.invalid/mod/@v/v1.1.0.mod":  "module example.invalid/mod\n",
		"/example.invalid/mod/@v/v1.0.0.mod":  "module example.invalid/mod\n",
		"/example.invalid/mod/@v/v1.1.0.info": `{"Version":"v1.1.0","Time":"2023-08-02T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2023-01-04T10:00:00Z"}`,
		"/example.invalid/mod/@v/v0.9.0.info": `{"Version":"v0.9.0","Time":"2022-06-04T10:00:00Z"}`,
	}
	goServer(t, routes)

	facts, err := fetchGo("example.invalid/mod", "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.Yanked {
		t.Error("Yanked = false for a version named in a retract directive")
	}
	if !strings.Contains(facts.Requested.DeprecationMessage, "published by mistake") {
		t.Errorf("message = %q, want the directive's comment", facts.Requested.DeprecationMessage)
	}

	// The interval form is why this compares versions rather than strings:
	// v1.0.0 is retracted without being written down anywhere.
	facts, err = fetchGo("example.invalid/mod", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.Yanked {
		t.Error("a version inside a retracted interval was not reported as withdrawn")
	}

	// The module itself is not deprecated, and a retraction must not read as
	// though it were.
	if facts.Deprecated {
		t.Error("retract directives read as a module-level deprecation")
	}
	facts, err = fetchGo("example.invalid/mod", "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.Yanked {
		t.Error("v1.2.0 is outside every retract directive")
	}
}

// Deprecation is declared in each version's own go.mod, so the answer depends
// on which version was pinned — a pin from before the announcement does not
// carry it, and one from after does.
func TestGoReadsDeprecationAtThePinnedVersion(t *testing.T) {
	goServer(t, map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.2.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/list":        "v1.0.0\nv1.2.0\n",
		"/example.invalid/mod/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2023-01-04T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.0.0.mod":  "module example.invalid/mod\n\ngo 1.21\n",
		"/example.invalid/mod/@v/v1.2.0.mod": `// Deprecated: use example.invalid/mod/v2 instead.
module example.invalid/mod

go 1.21
`,
	})

	facts, err := fetchGo("example.invalid/mod", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Deprecated {
		t.Error("the module's latest go.mod deprecates it; the package fact should say so")
	}
	if facts.Requested.Deprecated {
		t.Error("v1.0.0 predates the deprecation and its own go.mod does not carry it")
	}

	facts, err = fetchGo("example.invalid/mod", "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Requested.Deprecated ||
		!strings.Contains(facts.Requested.DeprecationMessage, "mod/v2") {
		t.Errorf("requested = %+v, want the deprecation the pinned go.mod declares", facts.Requested)
	}
}

// The budget skips middle history, and the pinned version is exactly where a
// hole in it would matter: its date is what "how far behind is this pin" is
// measured from.
func TestGoBuysThePinnedVersionsDateOutsideTheBudget(t *testing.T) {
	routes := map[string]string{
		"/example.invalid/mod/@latest":        `{"Version":"v1.40.0","Time":"2024-01-15T10:00:00Z"}`,
		"/example.invalid/mod/@v/v1.40.0.mod": "module example.invalid/mod\n",
		"/example.invalid/mod/@v/v1.20.0.mod": "module example.invalid/mod\n",
	}
	var list []string
	for i := 1; i <= 40; i++ {
		version := fmt.Sprintf("v1.%d.0", i)
		list = append(list, version)
		routes["/example.invalid/mod/@v/"+version+".info"] =
			fmt.Sprintf(`{"Version":%q,"Time":"20%02d-01-04T10:00:00Z"}`, version, i)
	}
	routes["/example.invalid/mod/@v/list"] = strings.Join(list, "\n")
	proxy := goServer(t, routes)

	facts, err := fetchGo("example.invalid/mod", "v1.20.0")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Requested.ReleaseDate == nil {
		t.Error("no date for the pinned version, which the budget alone would have skipped")
	}
	if got := proxy.count(".info"); got > maxGoVersionInfos+1 {
		t.Errorf("bought %d dates, budget is %d plus the pinned version", got, maxGoVersionInfos)
	}
}
