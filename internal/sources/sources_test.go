package sources

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// registry starts a stand-in for a package registry. Routes are exact paths;
// anything else answers 404, which is what makes the "does not exist" path
// testable without inventing a name and hoping nobody registers it.
//
// A body may be prefixed with "STATUS:<code>:" to answer with that status
// instead of 200 — the abnormal answers are the interesting ones, since a
// non-404 failure must never be read as absence.
func registry(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func cutStatusPrefix(body string) (int, string, bool) {
	const prefix = "STATUS:"
	if len(body) < len(prefix)+4 || body[:len(prefix)] != prefix {
		return 0, body, false
	}
	code := 0
	i := len(prefix)
	for ; i < len(body) && body[i] >= '0' && body[i] <= '9'; i++ {
		code = code*10 + int(body[i]-'0')
	}
	if i >= len(body) || body[i] != ':' {
		return 0, body, false
	}
	return code, body[i+1:], true
}

// release looks up a version in facts.Releases, so assertions do not depend on
// the order the adapter happened to build the slice in.
func release(t *testing.T, facts *model.PackageFacts, version string) model.ReleaseRef {
	t.Helper()
	for _, ref := range facts.Releases {
		if ref.Version == version {
			return ref
		}
	}
	t.Fatalf("no release %q in %v", version, versions(facts))
	return model.ReleaseRef{}
}

func versions(facts *model.PackageFacts) []string {
	out := make([]string, len(facts.Releases))
	for i, ref := range facts.Releases {
		out[i] = ref.Version
	}
	return out
}

func mustExist(t *testing.T, facts *model.PackageFacts) {
	t.Helper()
	if facts.Exists == nil || !*facts.Exists {
		t.Fatalf("Exists = %v, want true", facts.Exists)
	}
}

func day(t *testing.T, ref model.ReleaseRef) string {
	t.Helper()
	if ref.ReleaseDate == nil {
		t.Fatalf("release %q has no date", ref.Version)
	}
	return ref.ReleaseDate.Format(time.DateOnly)
}
