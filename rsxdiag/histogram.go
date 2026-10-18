package rsxdiag

import "math"

// Histograms use explicit millisecond upper bounds. Merging adds bucket counts,
// never averages percentiles. A quantile is reported as a bucket upper bound.
var MillisecondBounds = []float64{1, 2, 5, 10, 20, 50, 100, 150, 200, 250, 300, 400, 500, 600, 800, 1000, 1500, 2000, 3000, 5000, 10000, 30000, 60000}

func NewHistogram() []uint64 { return make([]uint64, len(MillisecondBounds)+1) }
func ObserveHistogram(h []uint64, ms float64) {
	if len(h) != len(MillisecondBounds)+1 || ms < 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return
	}
	for i, b := range MillisecondBounds {
		if ms <= b {
			h[i]++
			return
		}
	}
	h[len(h)-1]++
}
func MergeHistogram(dst, src []uint64) {
	if len(dst) != len(src) {
		return
	}
	for i, n := range src {
		dst[i] += n
	}
}
func HistogramQuantiles(h []uint64) map[string]interface{} {
	out := map[string]interface{}{"samples": uint64(0), "p50_upper_ms": nil, "p95_upper_ms": nil, "p99_upper_ms": nil, "method": "merged fixed-bucket upper bound"}
	if len(h) != len(MillisecondBounds)+1 {
		return out
	}
	var total uint64
	for _, n := range h {
		total += n
	}
	out["samples"] = total
	if total == 0 {
		return out
	}
	for _, q := range []struct {
		name    string
		percent uint64
	}{{"p50_upper_ms", 50}, {"p95_upper_ms", 95}, {"p99_upper_ms", 99}} {
		wanted := (total*q.percent + 99) / 100
		var count uint64
		for i, n := range h {
			count += n
			if count >= wanted {
				if i < len(MillisecondBounds) {
					out[q.name] = MillisecondBounds[i]
				} else {
					out[q.name] = "greater than 60000ms"
				}
				break
			}
		}
	}
	return out
}
