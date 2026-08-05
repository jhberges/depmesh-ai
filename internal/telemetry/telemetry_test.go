package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

// writeConsent lays down a consent file in an isolated XDG config home, the
// way install.sh does after the user opts in.
func writeConsent(t *testing.T, config Config) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "depmesh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "telemetry.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDisabledWithoutAnyConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvVar, "")
	t.Setenv(TokenEnvVar, "")

	if config := Resolve(""); config.Enabled() {
		t.Fatalf("telemetry must be off by default, got %+v", config)
	}
}

func TestResolveUsesConsentFile(t *testing.T) {
	writeConsent(t, Config{URL: DefaultEndpoint, Token: "dmk_secret"})
	t.Setenv(EnvVar, "")
	t.Setenv(TokenEnvVar, "")

	config := Resolve("")
	if config.URL != DefaultEndpoint || config.Token != "dmk_secret" {
		t.Fatalf("consent file not honoured: %+v", config)
	}
}

func TestResolvePrecedence(t *testing.T) {
	writeConsent(t, Config{URL: DefaultEndpoint})
	t.Setenv(EnvVar, "https://env.example/v1/telemetry")
	t.Setenv(TokenEnvVar, "")

	if got := Resolve("").URL; got != "https://env.example/v1/telemetry" {
		t.Fatalf("environment must beat the consent file, got %q", got)
	}
	if got := Resolve("https://policy.example/v1/telemetry").URL; got != "https://policy.example/v1/telemetry" {
		t.Fatalf("policy must beat both, got %q", got)
	}
}

// The stored ingest key is scoped to the stored endpoint: a policy or
// environment override pointing elsewhere must not carry it along.
func TestStoredTokenDoesNotFollowARedirectedEndpoint(t *testing.T) {
	writeConsent(t, Config{URL: DefaultEndpoint, Token: "dmk_secret"})
	t.Setenv(EnvVar, "")
	t.Setenv(TokenEnvVar, "")

	config := Resolve("https://self-hosted.example/v1/telemetry")
	if config.Token != "" {
		t.Fatalf("stored token leaked to %q", config.URL)
	}
}

func TestEnvironmentTokenWins(t *testing.T) {
	writeConsent(t, Config{URL: DefaultEndpoint, Token: "dmk_stored"})
	t.Setenv(EnvVar, "")
	t.Setenv(TokenEnvVar, "dmk_env")

	if got := Resolve("").Token; got != "dmk_env" {
		t.Fatalf("environment token must win, got %q", got)
	}
}

func TestMalformedConsentFileIsIgnored(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(EnvVar, "")
	t.Setenv(TokenEnvVar, "")
	if err := os.MkdirAll(filepath.Join(root, "depmesh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "depmesh", "telemetry.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if config := Resolve(""); config.Enabled() {
		t.Fatalf("a broken consent file must mean disabled, got %+v", config)
	}
}

var today = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

func nonexistentVerdict() *vet.Verdict {
	return vet.Evaluate(&model.PackageFacts{
		Ecosystem: model.NPM,
		Name:      "hallucinated-lib",
		Exists:    model.Bool(false),
	}, today)
}

func TestReportNonexistentSendsOnlyTheAgreedFields(t *testing.T) {
	type received struct {
		body    map[string]any
		auth    string
		reached bool
	}
	var got received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.reached = true
		got.auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	ReportNonexistent(Config{URL: server.URL, Token: "dmk_secret"}, "v1.2.3", nonexistentVerdict())

	if !got.reached {
		t.Fatal("no report sent for a nonexistent-package REJECT")
	}
	if got.auth != "Bearer dmk_secret" {
		t.Fatalf("ingest key not sent as a bearer header: %q", got.auth)
	}
	want := map[string]any{
		"kind": "nonexistent_package", "ecosystem": "npm",
		"package": "hallucinated-lib", "tool_version": "v1.2.3",
	}
	for key, value := range want {
		if got.body[key] != value {
			t.Errorf("payload[%s] = %v, want %v", key, got.body[key], value)
		}
	}
	// Anything beyond the documented five fields would be a privacy regression.
	for key := range got.body {
		if _, ok := want[key]; !ok && key != "time" {
			t.Errorf("unexpected field %q in telemetry payload", key)
		}
	}
}

func TestReportNonexistentStaysSilentWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("telemetry sent with no endpoint configured")
	}))
	defer server.Close()

	ReportNonexistent(Config{}, "v1.2.3", nonexistentVerdict())
}

func TestReportIsLimitedToNonexistenceRejects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("telemetry sent for a package that exists")
	}))
	defer server.Close()

	healthy := vet.Evaluate(&model.PackageFacts{
		Ecosystem: model.NPM,
		Name:      "express",
		Exists:    model.Bool(true),
		License:   "MIT",
	}, today)
	ReportNonexistent(Config{URL: server.URL}, "v1.2.3", healthy)
}

// A receiver that refuses the key must say so somewhere. Silence here is how
// a misconfigured install looks identical to a working one, forever.
func TestRejectedKeyIsReported(t *testing.T) {
	cases := []struct {
		status   int
		wantWarn bool
	}{
		{202, false},
		{401, true},
		{403, true},
		{500, false}, // transient: not worth a line on every vet
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(testCase.status)
		}))

		var warnings bytes.Buffer
		original := warnTo
		warnTo = &warnings
		ReportNonexistent(Config{URL: server.URL, Token: "k"}, "0.2.0", nonexistentVerdict())
		warnTo = original
		server.Close()

		if got := warnings.Len() > 0; got != testCase.wantWarn {
			t.Fatalf("HTTP %d warned=%v, want %v (%q)",
				testCase.status, got, testCase.wantWarn, warnings.String())
		}
		if testCase.wantWarn && !strings.Contains(warnings.String(), TokenEnvVar) {
			t.Fatalf("warning does not say what to check: %q", warnings.String())
		}
	}
}
