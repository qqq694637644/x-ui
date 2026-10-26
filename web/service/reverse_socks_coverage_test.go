package service

import (
	"math"
	"testing"
	"time"

	"x-ui/rsxdiag"
)

func TestDiagnosticsFiveSecondCoverageSurvivesRollup(t *testing.T) {
	up, down := 1.0, 0.0
	first := mergeDiagnosticPoints([]rsxdiag.Point{
		{Seconds: 1, Values: map[string]*float64{"xtcp_online": &up}},
		{Seconds: 4, Values: map[string]*float64{"xtcp_online": nil}},
	})
	if first.Seconds != 5 || first.SampleSeconds("xtcp_online") != 1 {
		t.Fatalf("partial five-second sample lost coverage: %+v", first)
	}
	combined := mergeDiagnosticPoints([]rsxdiag.Point{
		first,
		{Seconds: 5, Values: map[string]*float64{"xtcp_online": &down}},
	})
	summary := rsxdiag.Summarize([]rsxdiag.Point{combined}, 10*time.Second)
	value := summary.Availability["xtcp_online"]
	if value == nil || math.Abs(*value-100.0/6) > 1e-9 {
		t.Fatalf("expected one online second over six known seconds: %+v", summary)
	}
	if summary.MetricCoverage["xtcp_online"] != 60 {
		t.Fatalf("four missing seconds were filled in: %+v", summary)
	}
}
