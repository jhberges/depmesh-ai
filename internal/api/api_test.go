package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/upstream"
)

func TestHealthz(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}, ""))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/healthz")
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, response)
	}
}

func TestUnknownEcosystemIs400(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}, ""))
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

func TestVetRequiresTheKeyWhenOneIsSet(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}, "dmk_secret"))
	defer server.Close()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", 401},
		{"wrong key", "Bearer dmk_wrong", 401},
		{"bare wrong key", "dmk_wrong", 401},
		{"empty bearer", "Bearer ", 401},
		// 400 rather than 401: authenticated, and cargo is not an ecosystem.
		// It is the difference between "who are you" and "what did you ask".
		{"correct key", "Bearer dmk_secret", 400},
		{"correct key, bare", "dmk_secret", 400},
		{"correct key, lowercase scheme", "bearer dmk_secret", 400},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := http.NewRequest("GET", server.URL+"/v1/vet/cargo/serde", nil)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != testCase.want {
				t.Fatalf("got %d, want %d", response.StatusCode, testCase.want)
			}
			if testCase.want == 401 && response.Header.Get("WWW-Authenticate") == "" {
				t.Fatal("401 without a WWW-Authenticate header")
			}
		})
	}
}

func TestHealthzStaysOpen(t *testing.T) {
	server := httptest.NewServer(Handler(&gate.Gate{}, "dmk_secret"))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/healthz")
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("probes must not need the key: %v %v", err, response)
	}
}

// The whole point of the key: an unauthenticated flood must not be able to
// grow the audit log, because a full disk fails every vet closed.
func TestRejectedRequestWritesNoAuditRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	g := &gate.Gate{Audit: audit.Log{Path: path}}
	server := httptest.NewServer(Handler(g, "dmk_secret"))
	defer server.Close()

	for range 50 {
		response, err := server.Client().Get(server.URL + "/v1/vet/npm/express")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("got %d, want 401", response.StatusCode)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		body, _ := os.ReadFile(path)
		t.Fatalf("unauthenticated requests wrote %d bytes of audit log", len(body))
	}
}
