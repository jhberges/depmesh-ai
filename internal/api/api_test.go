package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

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

// Timeouts are easy to add and just as easy to lose in a later refactor, and
// losing them is invisible until something holds a connection open. The
// write budget gets its own assertion because it is the one with a floor
// underneath it: a vet spends up to 20s on the registry and another 20s on
// deps.dev enrichment, so anything below that turns a slow answer into a
// truncated one.
func TestServerTimeouts(t *testing.T) {
	server := newServer(":0", &gate.Gate{})
	if server.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout unset: the server can be held open by a partial request")
	}
	if server.ReadTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Errorf("ReadTimeout=%v IdleTimeout=%v, both must be set", server.ReadTimeout, server.IdleTimeout)
	}
	if floor := 40 * time.Second; server.WriteTimeout <= floor {
		t.Errorf("WriteTimeout=%v, must exceed %v — the registry and enrichment calls alone can take that long",
			server.WriteTimeout, floor)
	}
}

// A container runtime stops the gate with SIGTERM. Without the drain that is an
// abrupt kill, and a request cut between the audit rotate and the append is a
// decision neither served nor recorded.
func TestListenAndServeDrainsOnSIGTERM(t *testing.T) {
	addr := freeAddr(t)
	returned := make(chan error, 1)
	go func() { returned <- ListenAndServe(addr, &gate.Gate{}) }()

	waitUntilServing(t, addr)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("could not signal self: %v", err)
	}

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("shutdown returned %v, want a clean stop", err)
		}
	case <-time.After(shutdownGrace + 5*time.Second):
		t.Fatal("ListenAndServe did not return after SIGTERM — the signal is not being handled")
	}
}

// freeAddr borrows a port and hands it back. Racy in principle; the alternative
// is an injectable listener, which is more API surface than one test is worth.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitUntilServing also orders the test: the signal handler is registered
// before the listener opens, so a connection that succeeds proves the SIGTERM
// about to be sent will be caught rather than killing the test binary.
func waitUntilServing(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never came up on %s", addr)
}
