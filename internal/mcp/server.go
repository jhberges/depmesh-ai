// Package mcp implements a minimal MCP (Model Context Protocol) server over
// stdio.
//
// It implements just enough of the protocol — initialize, tools/list,
// tools/call, ping — as plain JSON-RPC 2.0, with no SDK dependency. This
// keeps the whole project at zero runtime dependencies, which is fitting for
// a dependency-vetting tool.
//
// Wire it into a coding agent (e.g. Claude Code) so the agent can call
// vet_dependency *before* installing a package it is about to add:
//
//	{"mcpServers": {"depmesh": {"command": "depmesh-ai", "args": ["serve"]}}}
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/sources"
)

const protocolVersion = "2025-06-18"

var toolList = []map[string]any{
	{
		"name": "vet_dependency",
		"description": "Vet an open-source package before adopting or installing it. " +
			"Verifies the package actually exists on its registry (guards against " +
			"hallucinated/slopsquatted names) and scores its health: release cadence, " +
			"staleness, deprecation, maintainer bus-factor, license, and known " +
			"advisories. Call this before adding any new dependency to a project. " +
			"Returns ADOPT, CAUTION, or REJECT with per-signal reasons, plus the " +
			"organization's policy decision when a policy is configured — a policy " +
			"BLOCK means the dependency must not be added.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ecosystem": map[string]any{
					"type":        "string",
					"enum":        []string{"npm", "pypi", "maven"},
					"description": "Package ecosystem/registry",
				},
				"package": map[string]any{
					"type": "string",
					"description": "Package name. For maven use groupId:artifactId, " +
						"e.g. org.apache.commons:commons-lang3",
				},
			},
			"required":             []string{"ecosystem", "package"},
			"additionalProperties": false,
		},
	},
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func result(id json.RawMessage, payload any) *response {
	return &response{JSONRPC: "2.0", ID: id, Result: payload}
}

func rpcErr(id json.RawMessage, code int, message string) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}}
}

func handle(g *gate.Gate, req *request) *response {
	// Notifications (no id) get no response.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil
	}
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = protocolVersion
		}
		return result(req.ID, map[string]any{
			"protocolVersion": params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "depmesh-ai", "version": gate.Version},
		})
	case "ping":
		return result(req.ID, map[string]any{})
	case "tools/list":
		return result(req.ID, map[string]any{"tools": toolList})
	case "tools/call":
		return callTool(g, req)
	default:
		return rpcErr(req.ID, -32601, "method not found: "+req.Method)
	}
}

func callTool(g *gate.Gate, req *request) *response {
	var params struct {
		Name      string `json:"name"`
		Arguments struct {
			Ecosystem string `json:"ecosystem"`
			Package   string `json:"package"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcErr(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.Name != "vet_dependency" {
		return rpcErr(req.ID, -32602, "unknown tool: "+params.Name)
	}

	text, isError := runVet(g, params.Arguments.Ecosystem, params.Arguments.Package)
	return result(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	})
}

func runVet(g *gate.Gate, ecosystem, name string) (text string, isError bool) {
	if ecosystem == "" || name == "" {
		return "Invalid arguments: both 'ecosystem' and 'package' are required", true
	}
	outcome, err := g.Vet("mcp", ecosystem, name, true)
	var unavailable *sources.UnavailableError
	switch {
	case errors.As(err, &unavailable):
		return fmt.Sprintf(
			"Registry unreachable — could NOT vet this package. Do not treat this "+
				"as approval or as proof of non-existence. Detail: %v", err), true
	case err != nil && outcome == nil:
		return "Invalid arguments: " + err.Error(), true
	case err != nil:
		// Verdict exists but auditing failed: fail closed on compliance paths.
		return "Audit log write failed — decision withheld: " + err.Error(), true
	}
	return outcome.JSON(), false
}

// Serve reads newline-delimited JSON-RPC requests from r and writes responses
// to w until EOF.
func Serve(g *gate.Gate, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(w)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		var resp *response
		if err := json.Unmarshal(line, &req); err != nil {
			resp = rpcErr(nil, -32700, "parse error")
		} else {
			resp = handle(g, &req)
		}
		if resp != nil {
			if err := encoder.Encode(resp); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
