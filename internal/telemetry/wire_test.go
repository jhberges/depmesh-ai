package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The wire format is a contract with depmesh-cloud, which lives in a separate
// repository and releases on its own schedule. testdata/telemetry_event.json is
// committed identically to both sides: here we assert the producer emits that
// shape, and depmesh-cloud asserts its consumer accepts it. If this test and
// its twin do not fail together, the format has drifted.
//
// See docs/TELEMETRY-PROTOCOL.md.
func TestReportMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "telemetry_event.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}

	encoded, err := json.Marshal(report{
		Kind:        "nonexistent_package",
		Ecosystem:   "npm",
		Package:     "some-hallucinated-name",
		Time:        "2026-08-02T09:41:00Z",
		ToolVersion: "v0.3.0",
	})
	if err != nil {
		t.Fatalf("marshalling report: %v", err)
	}

	var produced map[string]any
	if err := json.Unmarshal(encoded, &produced); err != nil {
		t.Fatalf("unmarshalling produced report: %v", err)
	}

	for key, want := range fixture {
		got, ok := produced[key]
		if !ok {
			t.Errorf("field %q is in the shared fixture but not in the report", key)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v, fixture has %v", key, got, want)
		}
	}
	// Extra fields are additive and allowed by the contract, but a new one
	// should be a deliberate act — surface it rather than let it ship silently.
	for key := range produced {
		if _, ok := fixture[key]; !ok {
			t.Errorf("report emits field %q that the shared fixture does not describe; "+
				"add it to testdata/telemetry_event.json in BOTH repositories", key)
		}
	}
}

// The tenant must never be derivable from the payload — depmesh-cloud takes it
// from the ingest key alone. Guard against someone "helpfully" adding it.
func TestReportCarriesNoTenant(t *testing.T) {
	encoded, err := json.Marshal(report{Kind: "nonexistent_package"})
	if err != nil {
		t.Fatalf("marshalling report: %v", err)
	}
	var produced map[string]any
	if err := json.Unmarshal(encoded, &produced); err != nil {
		t.Fatalf("unmarshalling produced report: %v", err)
	}
	if _, ok := produced["tenant"]; ok {
		t.Error("report carries a tenant field; the tenant must come from the ingest key only")
	}
}
