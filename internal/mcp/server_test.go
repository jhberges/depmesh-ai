package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/gate"
)

func rpc(t *testing.T, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	g := &gate.Gate{} // no policy, no audit, no telemetry
	if err := Serve(g, strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

func TestInitializeAndListTools(t *testing.T) {
	responses := rpc(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(responses) != 2 { // notification gets no response
		t.Fatalf("got %d responses, want 2", len(responses))
	}
	info := responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "depmesh-ai" {
		t.Fatalf("serverInfo: %v", info)
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "vet_dependency" {
		t.Fatalf("tools: %v", tools)
	}
}

func TestUnknownMethodErrors(t *testing.T) {
	responses := rpc(t, `{"jsonrpc":"2.0","id":5,"method":"bogus/method"}`)
	code := responses[0]["error"].(map[string]any)["code"].(float64)
	if code != -32601 {
		t.Fatalf("got code %v, want -32601", code)
	}
}

func TestBadToolArgumentsReportIsError(t *testing.T) {
	responses := rpc(t,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"vet_dependency","arguments":{"ecosystem":"npm"}}}`,
	)
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected isError=true, got %v", result)
	}
}

func TestParseErrorDoesNotKillServer(t *testing.T) {
	responses := rpc(t,
		`{not json`,
		`{"jsonrpc":"2.0","id":9,"method":"ping"}`,
	)
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}
	if responses[0]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("first response should be parse error: %v", responses[0])
	}
	if responses[1]["id"].(float64) != 9 {
		t.Fatalf("server did not continue after parse error: %v", responses[1])
	}
}

// rpcTo is rpc against a gate that delegates, so a tool call can be observed
// without reaching a registry.
func rpcTo(t *testing.T, g *gate.Gate, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := Serve(g, strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

// The agent is the one caller that knows the version *before* it is written to
// a build file, so the schema has to offer it and the description has to say
// when to use it.
func TestToolSchemaOffersAVersion(t *testing.T) {
	responses := rpc(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tool := responses[0]["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if _, offered := properties["version"]; !offered {
		t.Fatalf("no version property: %v", properties)
	}
	// Optional: an agent that only knows a name must still be able to ask.
	for _, required := range tool["inputSchema"].(map[string]any)["required"].([]any) {
		if required == "version" {
			t.Error("version is required; asking about a package alone must stay possible")
		}
	}
	if !strings.Contains(tool["description"].(string), "version") {
		t.Error("the description does not tell the agent to pass a version")
	}
}

func TestVersionArgumentReachesTheGate(t *testing.T) {
	var path, query string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"verdict":{"ecosystem":"npm","package":"express",
		  "version":"4.18.2","advice":"ADOPT","score":90}}`))
	}))
	defer origin.Close()

	rpcTo(t, &gate.Gate{Upstream: origin.URL},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vet_dependency","arguments":{"ecosystem":"npm","package":"express","version":"4.18.2"}}}`)
	if path != "/v1/vet/npm/express" || !strings.Contains(query, "version=4.18.2") {
		t.Errorf("gate saw %q?%q, want express and its version", path, query)
	}
}

// Agents paste what they are about to write into a build file, and that is a
// coordinate as often as it is a bare name. Looking "express@4.18.2" up as a
// package name would report a real dependency as a hallucination.
func TestCoordinateInThePackageArgumentIsSplit(t *testing.T) {
	var path, query string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"verdict":{"ecosystem":"npm","package":"express",
		  "version":"4.18.2","advice":"ADOPT","score":90}}`))
	}))
	defer origin.Close()

	rpcTo(t, &gate.Gate{Upstream: origin.URL},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vet_dependency","arguments":{"ecosystem":"npm","package":"express@4.18.2"}}}`)
	if path != "/v1/vet/npm/express" || !strings.Contains(query, "version=4.18.2") {
		t.Errorf("gate saw %q?%q, want the coordinate split", path, query)
	}
}
