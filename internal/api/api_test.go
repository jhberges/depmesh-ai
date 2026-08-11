package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/upstream"
)

func TestHealthz(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/healthz")
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, response)
	}
}

func TestUnknownEcosystemIs400(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}))
	defer server.Close()
	// Not "cargo": that became a real ecosystem, and the request then reaches
	// crates.io for real. The name here has to be one nothing will ever vet.
	response, err := server.Client().Get(server.URL + "/v1/vet/cpan/Some::Module")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 400 {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
}

// A delegating client asserts who it is; the gate records that instead of its
// own identity. The surface is mapped through a fixed vocabulary and the
// free-text fields are bounded, so a client cannot write whatever it likes
// into the audit log.
func TestCallerFromHeaders(t *testing.T) {
	cases := []struct {
		name                   string
		surface, actor, host   string
		wantSurface, wantActor string
	}{
		{"direct caller sends nothing", "", "", "", "api", ""},
		{"mcp delegation", "mcp", "alice", "laptop", "mcp", "alice"},
		{"cli delegation, case-insensitive", "CLI", "bob", "vm", "cli", "bob"},
		{"unknown surface falls back", "kubernetes", "eve", "", "api", "eve"},
		{"control characters stripped", "mcp", "al\x00ice\x1b[31m", "", "mcp", "alice[31m"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/v1/vet/npm/left-pad", nil)
			for header, value := range map[string]string{
				upstream.SurfaceHeader:  testCase.surface,
				upstream.ActorHeader:    testCase.actor,
				upstream.HostnameHeader: testCase.host,
			} {
				if value != "" {
					request.Header.Set(header, value)
				}
			}
			got := caller(request)
			if got.Surface != testCase.wantSurface || got.Actor != testCase.wantActor {
				t.Fatalf("caller = %+v, want surface %q actor %q",
					got, testCase.wantSurface, testCase.wantActor)
			}
		})
	}
}

func TestOverlongIdentityIsTruncated(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/vet/npm/left-pad", nil)
	request.Header.Set(upstream.ActorHeader, strings.Repeat("é", 500))
	if got := caller(request).Actor; len([]rune(got)) != maxIdentityLength {
		t.Fatalf("actor kept %d runes, want %d", len([]rune(got)), maxIdentityLength)
	}
}

// The version travels as a query parameter and must arrive at the gate. The
// gate under test delegates onward, so nothing here reaches a registry.
func TestVersionQueryReachesTheGate(t *testing.T) {
	var query string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"verdict":{"ecosystem":"npm","package":"express",
		  "version":"4.18.2","advice":"ADOPT","score":90}}`))
	}))
	defer origin.Close()

	server := httptest.NewServer(Handler(&gate.Gate{Upstream: origin.URL}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/v1/vet/npm/express?version=4.18.2")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("got %d, want 200", response.StatusCode)
	}
	if !strings.Contains(query, "version=4.18.2") {
		t.Errorf("gate saw query %q, want the version", query)
	}
}

// A range is refused rather than resolved, and the refusal happens before any
// registry is contacted — which is what makes this test safe to run offline.
func TestVersionRangeIs400(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/v1/vet/npm/express?version=%5E4.18.0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "constraint") {
		t.Errorf("body = %s, want it to say a constraint was passed", body)
	}
}
