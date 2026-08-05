package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/sources"
)

const outcomeJSON = `{"verdict":{"ecosystem":"npm","package":"left-pad","advice":"ADOPT","score":90}}`

// recorder keeps the last request the stub gate was asked, guarded because
// the handler runs on the server's goroutine.
type recorder struct {
	mu   sync.Mutex
	last *http.Request
}

func (rec *recorder) get(t *testing.T) *http.Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.last == nil {
		t.Fatal("gate was never called")
	}
	return rec.last
}

// stub records what the gate was asked and answers with status/body.
func stub(t *testing.T, status int, body string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.last = r.Clone(r.Context())
		rec.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, rec
}

func TestVetSendsPathAndIdentity(t *testing.T) {
	server, rec := stub(t, http.StatusOK, outcomeJSON)

	body, err := Vet(Request{
		Base: server.URL, Surface: "mcp", Actor: "dev", Hostname: "laptop",
		Ecosystem: "npm", Package: "@types/node", Enrich: true,
	})
	if err != nil {
		t.Fatalf("vet: %v", err)
	}
	if string(body) != outcomeJSON {
		t.Fatalf("body = %s", body)
	}
	// The scope slash must survive as a path separator: the gate's route ends
	// in {package...} and matches the remainder whole.
	if rec.get(t).URL.Path != "/v1/vet/npm/@types/node" {
		t.Fatalf("path = %q", rec.get(t).URL.Path)
	}
	if rec.get(t).URL.RawQuery != "" {
		t.Fatalf("enrich=true should add no query, got %q", rec.get(t).URL.RawQuery)
	}
	if got := rec.get(t).Header.Get(SurfaceHeader); got != "mcp" {
		t.Fatalf("surface = %q", got)
	}
	if got := rec.get(t).Header.Get(ActorHeader); got != "dev" {
		t.Fatalf("actor = %q", got)
	}
	if got := rec.get(t).Header.Get(HostnameHeader); got != "laptop" {
		t.Fatalf("hostname = %q", got)
	}
}

func TestVetMavenCoordinateAndNoEnrich(t *testing.T) {
	server, rec := stub(t, http.StatusOK, outcomeJSON)

	if _, err := Vet(Request{
		Base: server.URL, Ecosystem: "maven",
		Package: "org.apache.commons:commons-lang3", Enrich: false,
	}); err != nil {
		t.Fatalf("vet: %v", err)
	}
	if rec.get(t).URL.Path != "/v1/vet/maven/org.apache.commons:commons-lang3" {
		t.Fatalf("path = %q", rec.get(t).URL.Path)
	}
	if rec.get(t).URL.Query().Get("enrich") != "false" {
		t.Fatalf("query = %q", rec.get(t).URL.RawQuery)
	}
}

// A blocked package is a decision, not a failure: the body must come back.
func TestBlockedIsReturnedNotErrored(t *testing.T) {
	server, _ := stub(t, http.StatusConflict, outcomeJSON)

	body, err := Vet(Request{Base: server.URL, Ecosystem: "npm", Package: "left-pad"})
	if err != nil {
		t.Fatalf("409 should carry an outcome, got error: %v", err)
	}
	if string(body) != outcomeJSON {
		t.Fatalf("body = %s", body)
	}
}

// Everything that isn't a decision must be indistinguishable from an
// unreachable registry — never silently readable as approval.
func TestFailuresNeverLookLikeApproval(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		unavailable bool // true: must be a sources.UnavailableError
	}{
		{"registry down behind the gate", http.StatusBadGateway, `{"error":"registry unreachable"}`, true},
		{"gate cannot audit", http.StatusInternalServerError, `{"error":"audit log write failed"}`, false},
		{"bad request", http.StatusBadRequest, `{"error":"unknown ecosystem"}`, false},
		{"gate needs auth", http.StatusUnauthorized, ``, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := stub(t, testCase.status, testCase.body)
			body, err := Vet(Request{Base: server.URL, Ecosystem: "npm", Package: "left-pad"})
			if err == nil {
				t.Fatalf("status %d returned no error, body %s", testCase.status, body)
			}
			var unavailable *sources.UnavailableError
			if got := errors.As(err, &unavailable); got != testCase.unavailable {
				t.Fatalf("UnavailableError = %v, want %v (err: %v)", got, testCase.unavailable, err)
			}
		})
	}
}

func TestUnreachableGateIsUnavailable(t *testing.T) {
	server, _ := stub(t, http.StatusOK, outcomeJSON)
	dead := server.URL
	server.Close()

	_, err := Vet(Request{Base: dead, Ecosystem: "npm", Package: "left-pad"})
	var unavailable *sources.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("want UnavailableError, got %v", err)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://depmesh.lan:8385/", "https://depmesh.lan:8385", true},
		{"  http://10.0.0.5:8385  ", "http://10.0.0.5:8385", true},
		{"https://gate.example.com/depmesh/", "https://gate.example.com/depmesh", true},
		{"depmesh.lan:8385", "", false}, // no scheme: a typo, not an intent
		{"ftp://depmesh.lan", "", false},
		{"https://", "", false},
		{"", "", false},
	}
	for _, testCase := range cases {
		got, err := Normalize(testCase.in)
		if testCase.ok != (err == nil) {
			t.Fatalf("Normalize(%q) error = %v, want ok=%v", testCase.in, err, testCase.ok)
		}
		if got != testCase.want {
			t.Fatalf("Normalize(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}
