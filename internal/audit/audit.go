// Package audit appends one JSONL record per vet decision — the compliance
// evidence trail: who asked about what, what the verdict was, and what policy
// decided. Append-only JSON Lines so it can be shipped to any log pipeline
// (Splunk, ELK, ...) without parsing gymnastics.
package audit

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"time"

	"github.com/jhberges/depmesh-ai/internal/bytesize"
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

// DefaultMaxSize and DefaultKeep bound the log unless configured otherwise.
// A decision log grows with traffic and an exposed API has no say in how much
// traffic it gets, so "unbounded until the disk fills" is not a safe default —
// and a full disk fails every vet closed, turning a flood into an outage.
const (
	DefaultMaxSize = 100 * bytesize.MB
	DefaultKeep    = 5
)

// Log is a size-bounded audit destination.
type Log struct {
	// Path is the active file. Empty disables auditing entirely.
	Path string
	// MaxSize rotates the active file once a record would carry it past this
	// many bytes. Zero means never rotate.
	MaxSize bytesize.Size
	// Keep is how many rotated files to retain; older ones are deleted.
	Keep int
}

// Write appends a record for the decision, rotating first if this record
// would push the file past MaxSize. Auditing failures are returned so callers
// on compliance-critical paths can decide to fail closed — including failures
// to rotate, because silently growing past a configured bound is exactly the
// outcome the bound exists to prevent.
func (l Log) Write(caller Caller, version string, v *vet.Verdict, policyResult *policy.Result) error {
	if l.Path == "" {
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
	line = append(line, '\n')
	if err := l.rotateIfFull(int64(len(line))); err != nil {
		return err
	}
	file, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(line)
	return err
}

// rotateIfFull moves the active file aside when incoming would not fit. A
// file that does not exist yet, or is already empty, is never rotated — a
// single record larger than the whole budget still gets written rather than
// triggering an endless rotate-and-still-too-big loop.
func (l Log) rotateIfFull(incoming int64) error {
	if l.MaxSize <= 0 {
		return nil
	}
	info, err := os.Stat(l.Path)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	case info.Size() == 0 || info.Size()+incoming <= int64(l.MaxSize):
		return nil
	}
	return l.rotate()
}

// rotate renames the active file to a timestamped sibling, then prunes.
//
// Timestamps rather than the usual .1/.2/.3 shuffle because several
// processes can share one audit file — an MCP server and a CLI on the same
// machine, or a restarted gate. Numbered rotation has them renaming onto
// each other's destinations and losing a file; a nanosecond-stamped name
// collides with nothing, so a concurrent double rotation costs an extra
// small file and no records.
func (l Log) rotate() error {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(l.Path, l.Path+"."+stamp); err != nil {
		// Another process may have rotated the same file microseconds ago,
		// leaving nothing here to move. That is success, not failure.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return l.prune()
}

// prune deletes the oldest rotated files beyond Keep. The stamp format sorts
// lexicographically in time order, so sorting the names sorts by age.
func (l Log) prune() error {
	if l.Keep < 0 {
		return nil
	}
	rotated, err := filepath.Glob(l.Path + ".*")
	if err != nil {
		return err
	}
	if len(rotated) <= l.Keep {
		return nil
	}
	sort.Strings(rotated)
	for _, stale := range rotated[:len(rotated)-l.Keep] {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func actor() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
