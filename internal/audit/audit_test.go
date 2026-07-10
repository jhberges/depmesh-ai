package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jhberges/depmesh-ai/internal/policy"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func TestWriteAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	verdict := &vet.Verdict{Ecosystem: "npm", Package: "left-pad", Advice: vet.Adopt, Score: 88}
	result := &policy.Result{Allowed: true}

	for range 2 {
		if err := Write(path, "cli", "0.2.0", verdict, result); err != nil {
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
	if err := Write("", "cli", "0.2.0", &vet.Verdict{}, nil); err != nil {
		t.Fatalf("noop write errored: %v", err)
	}
}
