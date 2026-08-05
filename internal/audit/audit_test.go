package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func TestWriteAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	verdict := &vet.Verdict{Ecosystem: "npm", Package: "left-pad", Advice: vet.Adopt, Score: 88}
	result := &policy.Result{Allowed: true}

	for range 2 {
		if err := (Log{Path: path}).Write(Local("cli"), "0.2.0", verdict, result); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("line %d not JSON: %v", lines+1, err)
		}
		if record.Package != "left-pad" || record.Surface != "cli" || record.Policy == nil {
			t.Fatalf("bad record: %+v", record)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("got %d lines, want 2", lines)
	}
}

func TestEmptyPathIsNoop(t *testing.T) {
	if err := (Log{Path: ""}).Write(Local("cli"), "0.2.0", &vet.Verdict{}, nil); err != nil {
		t.Fatalf("noop write errored: %v", err)
	}
}

// records counts the JSONL lines across the active log and every rotated
// sibling — the whole point of rotation is that nothing is lost.
func records(t *testing.T, path string) (active int, total int, rotatedFiles int) {
	t.Helper()
	count := func(name string) int {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return bytes.Count(body, []byte("\n"))
	}
	active = count(path)
	total = active
	rotated, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range rotated {
		total += count(name)
	}
	return active, total, len(rotated)
}

func write(t *testing.T, log Log, n int) {
	t.Helper()
	verdict := &vet.Verdict{Ecosystem: "npm", Package: "left-pad", Advice: vet.Adopt, Score: 88}
	for i := range n {
		if err := log.Write(Local("cli"), "0.2.0", verdict, nil); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
}

func TestRotationKeepsEveryRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	// One record is a few hundred bytes; 400 bytes rotates every record or two.
	log := Log{Path: path, MaxSize: 400, Keep: 20}

	write(t, log, 30)

	active, total, rotatedFiles := records(t, path)
	if rotatedFiles == 0 {
		t.Fatal("nothing rotated — the size bound did nothing")
	}
	if total != 30 {
		t.Fatalf("kept %d records across %d files, want all 30", total, rotatedFiles+1)
	}
	if active == 30 {
		t.Fatal("everything is still in the active file")
	}
}

func TestKeepBoundsTheNumberOfFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := Log{Path: path, MaxSize: 300, Keep: 3}

	write(t, log, 40)

	_, _, rotatedFiles := records(t, path)
	if rotatedFiles != 3 {
		t.Fatalf("%d rotated files retained, want exactly Keep=3", rotatedFiles)
	}
}

func TestKeepZeroRetainsNothingButTheActiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := Log{Path: path, MaxSize: 300, Keep: 0}

	write(t, log, 20)

	if _, _, rotatedFiles := records(t, path); rotatedFiles != 0 {
		t.Fatalf("%d rotated files retained, want 0", rotatedFiles)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log missing: %v", err)
	}
}

func TestZeroMaxSizeNeverRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := Log{Path: path, MaxSize: 0, Keep: 5}

	write(t, log, 50)

	active, _, rotatedFiles := records(t, path)
	if rotatedFiles != 0 || active != 50 {
		t.Fatalf("MaxSize 0 rotated anyway: %d files, %d active records", rotatedFiles, active)
	}
}

// A record bigger than the whole budget must still be written once, not
// rotate forever against a limit it can never satisfy.
func TestOversizedRecordIsStillWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := Log{Path: path, MaxSize: 10, Keep: 2}

	write(t, log, 3)

	if _, total, _ := records(t, path); total != 3 {
		t.Fatalf("kept %d records, want 3", total)
	}
}

// Rotation failure must reach the caller: silently outgrowing a configured
// bound is the outcome the bound exists to prevent.
func TestRotationFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	log := Log{Path: path, MaxSize: 200, Keep: 2}
	write(t, log, 2)

	// A read-only directory cannot be renamed within.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make the directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	verdict := &vet.Verdict{Ecosystem: "npm", Package: "left-pad", Advice: vet.Adopt, Score: 88}
	if err := log.Write(Local("cli"), "0.2.0", verdict, nil); err == nil {
		t.Fatal("rotation into a read-only directory reported success")
	}
}

// Several writers share one audit file in practice — an MCP server and a CLI
// on the same machine, or a gate that was restarted. Timestamped rotation
// exists so they cannot rename onto each other's files and lose one.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	const writers, each = 8, 40

	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A separate Log value per writer, as separate processes would have.
			// Keep is set far above the number of rotations these writers can
			// cause, so anything missing at the end was lost, not pruned.
			log := Log{Path: path, MaxSize: 2000, Keep: 1000}
			verdict := &vet.Verdict{Ecosystem: "npm", Package: "left-pad", Advice: vet.Adopt, Score: 88}
			for range each {
				if err := log.Write(Local("cli"), "0.2.0", verdict, nil); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if _, total, rotatedFiles := records(t, path); total != writers*each {
		t.Fatalf("kept %d of %d records across %d rotated files",
			total, writers*each, rotatedFiles)
	}
}
