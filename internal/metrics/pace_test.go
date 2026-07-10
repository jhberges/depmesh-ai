// Ports of com.depmesh.backend.model.ArtifactReleasePaceMetricsBuilderTest
// plus coverage for the state machine. The one-week and two-day fixtures are
// the original 2015 test data, unchanged.
package metrics

import (
	"testing"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

func ref(version, date string) model.ReleaseRef {
	r := model.ReleaseRef{Version: version}
	if date != "" {
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			panic(err)
		}
		r.ReleaseDate = &t
	}
	return r
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestAveragePaceOneWeekAverage(t *testing.T) {
	// Legacy fixture: expected PT168H (7 days)
	ordered := []model.ReleaseRef{
		ref("3", "2015-01-15"),
		ref("2", "2015-01-08"),
		ref("1", "2015-01-01"),
	}
	if got := CalculateAveragePace(ordered, 0, len(ordered)-1); got != days(7) {
		t.Fatalf("got %v, want %v", got, days(7))
	}
}

func TestAveragePaceTwoDaysAverage(t *testing.T) {
	// Legacy fixture: expected PT48H (2 days)
	ordered := []model.ReleaseRef{
		ref("6", "2015-01-09"),
		ref("5", "2015-01-07"),
		ref("3", "2015-01-05"),
		ref("2", "2015-01-03"),
		ref("1", "2015-01-01"),
	}
	if got := CalculateAveragePace(ordered, 0, len(ordered)-1); got != days(2) {
		t.Fatalf("got %v, want %v", got, days(2))
	}
}

func TestAveragePaceSkipsUndatedReleases(t *testing.T) {
	ordered := []model.ReleaseRef{
		ref("3", "2015-01-15"),
		ref("2", ""),
		ref("1", "2015-01-01"),
	}
	if got := CalculateAveragePace(ordered, 0, len(ordered)-1); got != days(14) {
		t.Fatalf("got %v, want %v", got, days(14))
	}
}

func TestNoData(t *testing.T) {
	if got := BuildReleasePace(nil).State; got != NoData {
		t.Fatalf("got %v, want NO_DATA", got)
	}
}

func TestNotEnoughData(t *testing.T) {
	m := BuildReleasePace([]model.ReleaseRef{ref("2", "2020-02-01"), ref("1", "2020-01-01")})
	if m.State != NotEnoughData {
		t.Fatalf("got state %v", m.State)
	}
	if m.LatestRelease.Version != "2" || m.FirstRelease.Version != "1" {
		t.Fatalf("first/latest mixed up: first=%v latest=%v", m.FirstRelease, m.LatestRelease)
	}
}

func TestOKComputesBothPaces(t *testing.T) {
	m := BuildReleasePace([]model.ReleaseRef{
		ref("4", "2020-04-01"),
		ref("3", "2020-03-01"),
		ref("2", "2020-02-01"),
		ref("1", "2020-01-01"),
	})
	if m.State != OK {
		t.Fatalf("got state %v", m.State)
	}
	if m.FirstRelease.Version != "1" || m.LatestRelease.Version != "4" {
		t.Fatalf("first/latest mixed up")
	}
	if want := days(91) / 3; m.OverallPace != want {
		t.Fatalf("overall pace %v, want %v", m.OverallPace, want)
	}
	if want := days(60) / 2; m.Last3Pace != want {
		t.Fatalf("last3 pace %v, want %v", m.Last3Pace, want)
	}
}
