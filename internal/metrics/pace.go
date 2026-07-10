// Package metrics ports the release-pace metrics from the legacy DepMesh
// backend.
//
// Java original: com.depmesh.backend.model.ArtifactReleasePaceMetrics.
// The semantics are preserved (including the newest-at-index-0 list contract
// and the NO_DATA / NOT_ENOUGH_DATA / OK states) so the original unit-test
// fixtures carry over — see pace_test.go.
package metrics

import (
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

type PaceState string

const (
	NoData        PaceState = "NO_DATA"
	NotEnoughData PaceState = "NOT_ENOUGH_DATA"
	OK            PaceState = "OK"
)

type ReleasePaceMetrics struct {
	State         PaceState
	FirstRelease  *model.ReleaseRef
	LatestRelease *model.ReleaseRef
	OverallPace   time.Duration // zero unless State == OK
	Last3Pace     time.Duration // zero unless State == OK
}

// CalculateAveragePace returns the average gap between consecutive releases
// in refs[start..toInclusive]. refs is newest-first. Releases without a date
// are skipped and excluded from the divisor, mirroring the legacy
// invalidDataPoints handling.
func CalculateAveragePace(refs []model.ReleaseRef, start, toInclusive int) time.Duration {
	var total time.Duration
	invalid := 0
	var previous *time.Time
	for i := start; i <= toInclusive; i++ {
		date := refs[i].ReleaseDate
		if date == nil {
			invalid++
			continue
		}
		if previous != nil {
			total += previous.Sub(*date)
		}
		previous = date
	}
	divisor := toInclusive - start - invalid
	if divisor < 1 {
		divisor = 1
	}
	return total / time.Duration(divisor)
}

func BuildReleasePace(refs []model.ReleaseRef) ReleasePaceMetrics {
	switch {
	case len(refs) == 0:
		return ReleasePaceMetrics{State: NoData}
	case len(refs) < 3:
		return ReleasePaceMetrics{
			State:         NotEnoughData,
			FirstRelease:  &refs[len(refs)-1],
			LatestRelease: &refs[0],
		}
	default:
		return ReleasePaceMetrics{
			State:         OK,
			FirstRelease:  &refs[len(refs)-1],
			LatestRelease: &refs[0],
			OverallPace:   CalculateAveragePace(refs, 0, len(refs)-1),
			Last3Pace:     CalculateAveragePace(refs, 0, 2),
		}
	}
}
