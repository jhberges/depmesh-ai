// Package audit appends one JSONL record per vet decision — the compliance
// evidence trail: who asked about what, what the verdict was, and what policy
// decided. Append-only JSON Lines so it can be shipped to any log pipeline
// (Splunk, ELK, ...) without parsing gymnastics.
package audit

import (
	"encoding/json"
	"os"
	"os/user"
	"time"

	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

type Record struct {
	Time      string         `json:"time"`
	Actor     string         `json:"actor"`
	Hostname  string         `json:"hostname,omitempty"`
	Surface   string         `json:"surface"` // cli | mcp | api
	Ecosystem string         `json:"ecosystem"`
	Package   string         `json:"package"`
	Advice    string         `json:"advice"`
	Score     int            `json:"score"`
	Policy    *policy.Result `json:"policy,omitempty"`
	Degraded  []string       `json:"degraded_sources,omitempty"`
	Version   string         `json:"tool_version"`
}

// Caller identifies who asked. Actor and Hostname empty means "this process,
// this machine", which is the answer on the CLI and the MCP server. A gate
// serving delegated requests fills them from what the client sent, so the
// record names the developer who asked rather than the server that answered
// — client-asserted, and therefore only as trustworthy as the network the
// gate listens on.
type Caller struct {
	Surface  string // cli | mcp | api
	Actor    string
	Hostname string
}

// Local describes this process as the caller.
func Local(surface string) Caller { return Caller{Surface: surface} }

// Resolve fills in whatever the caller left empty from this machine.
func (c Caller) Resolve() Caller {
	if c.Actor == "" {
		c.Actor = actor()
	}
	if c.Hostname == "" {
		if hostname, err := os.Hostname(); err == nil {
			c.Hostname = hostname
		}
	}
	return c
}

// Write appends a record for the decision to path. Auditing failures are
// returned so callers on compliance-critical paths can decide to fail closed.
func Write(path string, caller Caller, version string, v *vet.Verdict, policyResult *policy.Result) error {
	if path == "" {
		return nil
	}
	caller = caller.Resolve()
	record := Record{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Actor:     caller.Actor,
		Hostname:  caller.Hostname,
		Surface:   caller.Surface,
		Ecosystem: v.Ecosystem,
		Package:   v.Package,
		Advice:    string(v.Advice),
		Score:     v.Score,
		Policy:    policyResult,
		Degraded:  v.Degraded,
		Version:   version,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

func actor() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
