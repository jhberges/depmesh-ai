package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func rpc(t *testing.T, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
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
