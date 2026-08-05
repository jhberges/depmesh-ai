// Package bytesize parses human-written sizes ("100MB") from policy files
// and command lines, so a size limit can be read at a glance in review
// rather than counted in digits.
//
// Units are binary: KB is 1024 bytes, not 1000. The KiB/MiB/GiB spellings
// are accepted too and mean the same thing — being pedantic about it would
// only trip people up in a config file whose point is legibility.
package bytesize

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type Size int64

const (
	KB Size = 1 << (10 * (iota + 1))
	MB
	GB
	TB
)

var units = []struct {
	suffix string
	scale  Size
}{
	// Longest first: "KIB" must match before "KB" would, and both before "B".
	{"KIB", KB}, {"MIB", MB}, {"GIB", GB}, {"TIB", TB},
	{"KB", KB}, {"MB", MB}, {"GB", GB}, {"TB", TB},
	{"B", 1},
}

// Parse reads "100MB", "1 GiB", "0", or a plain byte count. Zero is valid
// and means "no limit" to every caller here.
func Parse(text string) (Size, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(text))
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	for _, unit := range units {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		value, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q", text)
		}
		if value < 0 {
			return 0, fmt.Errorf("invalid size %q: negative", text)
		}
		return Size(value * float64(unit.scale)), nil
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: want a byte count or a unit like 100MB", text)
	}
	if value < 0 {
		return 0, fmt.Errorf("invalid size %q: negative", text)
	}
	return Size(value), nil
}

func (s Size) String() string {
	for _, unit := range []struct {
		suffix string
		scale  Size
	}{{"GB", GB}, {"MB", MB}, {"KB", KB}} {
		if s >= unit.scale && s%unit.scale == 0 {
			return strconv.FormatInt(int64(s/unit.scale), 10) + unit.suffix
		}
	}
	return strconv.FormatInt(int64(s), 10) + "B"
}

// UnmarshalJSON accepts both spellings a policy file might use:
// "audit_max_size": 104857600 and "audit_max_size": "100MB".
func (s *Size) UnmarshalJSON(data []byte) error {
	var asNumber int64
	if err := json.Unmarshal(data, &asNumber); err == nil {
		if asNumber < 0 {
			return fmt.Errorf("invalid size %d: negative", asNumber)
		}
		*s = Size(asNumber)
		return nil
	}
	var asText string
	if err := json.Unmarshal(data, &asText); err != nil {
		return fmt.Errorf("size must be a number of bytes or a string like \"100MB\"")
	}
	parsed, err := Parse(asText)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

func (s Size) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Value implements flag.Value so a CLI flag takes the same spellings.
func (s *Size) Set(text string) error {
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
