package rsxdiag

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestDiagnosticsHistoryPreservesPartialMetricCoverage(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Minute)
	up, down := 1.0, 0.0
	first := newAccumulator(start)
	first.add(Point{Seconds: 1, Values: map[string]*float64{"warp_online": &up}})
	first.add(Point{Seconds: 59, Values: map[string]*float64{"warp_online": nil}})
	partial := first.point()
	if partial.Seconds != 60 || partial.SampleSeconds("warp_online") != 1 {
		t.Fatalf("missing samples counted as observed: %+v", partial)
	}

	// Verify the weights survive disk serialization, not just in-memory use.
	raw, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	var restored Point
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	second := newAccumulator(start.Add(time.Minute))
	second.add(Point{Seconds: 60, Values: map[string]*float64{"warp_online": &down}})
	combined := newAccumulator(start)
	combined.add(restored)
	combined.add(second.point())
	summary := Summarize([]Point{combined.point()}, 2*time.Minute)
	got := summary.Availability["warp_online"]
	if got == nil || math.Abs(*got-100.0/61) > 1e-9 {
		t.Fatalf("availability must use 1 online / 61 observed seconds: %+v", summary)
	}
	if math.Abs(summary.MetricCoverage["warp_online"]-61.0/120*100) > 1e-9 {
		t.Fatalf("per-metric coverage inflated: %+v", summary)
	}
	if summary.ObservedSeconds != 120 || summary.CoveragePct != 100 {
		t.Fatalf("collector coverage should remain independent: %+v", summary)
	}
}

func TestDiagnosticsInvalidOrMissingAggregateWeightsAreUnknown(t *testing.T) {
	up := 1.0
	for _, weight := range []float64{-1, 0, 11, math.NaN(), math.Inf(1)} {
		p := Point{Seconds: 10, Values: map[string]*float64{"xtcp_online": &up}, Weights: map[string]float64{"xtcp_online": weight}}
		if p.SampleSeconds("xtcp_online") != 0 {
			t.Fatalf("invalid weight accepted: %v", weight)
		}
	}
	p := Point{Seconds: 10, Values: map[string]*float64{"xtcp_online": &up}, Weights: map[string]float64{}}
	if p.SampleSeconds("xtcp_online") != 0 {
		t.Fatal("missing aggregate weight treated as a full observation")
	}
	if v := Summarize([]Point{p}, time.Minute).Availability["xtcp_online"]; v != nil {
		t.Fatal("unobserved connection given an availability percentage")
	}
}
