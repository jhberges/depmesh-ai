package api

import (
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
	response, err := server.Client().Get(server.URL + "/v1/vet/cargo/serde")
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
