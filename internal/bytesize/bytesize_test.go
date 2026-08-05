package bytesize

import (
	"encoding/json"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Size
		ok   bool
	}{
		{"0", 0, true},
		{"1024", 1024, true},
		{"100MB", 100 * MB, true},
		{"100mb", 100 * MB, true},
		{" 1 GiB ", GB, true},
		{"512KB", 512 * KB, true},
		{"1.5MB", Size(1.5 * float64(MB)), true},
		{"2TB", 2 * TB, true},
		{"100B", 100, true},
		{"", 0, false},
		{"-5MB", 0, false},
		{"-1", 0, false},
		{"lots", 0, false},
		{"100PB", 0, false}, // not a unit we accept
	}
	for _, testCase := range cases {
		got, err := Parse(testCase.in)
		if testCase.ok != (err == nil) {
			t.Fatalf("Parse(%q) error = %v, want ok=%v", testCase.in, err, testCase.ok)
		}
		if got != testCase.want {
			t.Fatalf("Parse(%q) = %d, want %d", testCase.in, got, testCase.want)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Size]string{
		0: "0B", 512: "512B", KB: "1KB", 100 * MB: "100MB", 2 * GB: "2GB",
		MB + 1: "1048577B", // not a round unit, so say it exactly
	}
	for size, want := range cases {
		if got := size.String(); got != want {
			t.Fatalf("Size(%d).String() = %q, want %q", int64(size), got, want)
		}
	}
}

// A policy file may write either spelling; both must land on the same value.
func TestUnmarshalAcceptsNumberAndString(t *testing.T) {
	var config struct {
		Max Size `json:"audit_max_size"`
	}
	for _, body := range []string{`{"audit_max_size": 104857600}`, `{"audit_max_size": "100MB"}`} {
		config.Max = 0
		if err := json.Unmarshal([]byte(body), &config); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if config.Max != 100*MB {
			t.Fatalf("%s parsed to %d, want %d", body, int64(config.Max), int64(100*MB))
		}
	}
}

func TestUnmarshalRejectsNonsense(t *testing.T) {
	for _, body := range []string{`{"audit_max_size": "huge"}`, `{"audit_max_size": -1}`,
		`{"audit_max_size": true}`, `{"audit_max_size": "-5MB"}`} {
		var config struct {
			Max Size `json:"audit_max_size"`
		}
		if err := json.Unmarshal([]byte(body), &config); err == nil {
			t.Fatalf("%s was accepted, giving %d", body, int64(config.Max))
		}
	}
}

func TestRoundTripsThroughJSON(t *testing.T) {
	original := 100 * MB
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Size
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("%s: %v", encoded, err)
	}
	if decoded != original {
		t.Fatalf("round trip gave %d, want %d", int64(decoded), int64(original))
	}
}
