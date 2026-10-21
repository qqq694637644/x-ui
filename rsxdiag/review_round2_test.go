package rsxdiag

import (
	"archive/zip"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func attemptTotal(points []Point) float64 {
	sum := 0.0
	for _, p := range points {
		sum += p.Totals["attempts"]
	}
	return sum
}

func TestReviewSameMinuteRepeatedRestartsHaveNoDoubleCounting(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Now().UTC().Truncate(time.Minute)
	add := func(h *History, attempts float64, offset time.Duration) {
		h.Add(Point{Time: stamp.Add(offset), Seconds: 2, Values: map[string]*float64{},
			Totals: map[string]float64{"attempts": attempts}})
	}
	first, err := NewHistory(dir, 30, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	add(first, 3, time.Second)
	first.Close()
	second, err := NewHistory(dir, 30, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	add(second, 4, 3*time.Second)
	second.Close()
	third, err := NewHistory(dir, 30, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	add(third, 5, 5*time.Second)
	got, err := third.Read(time.Now().UTC().Add(-5*time.Minute), 1000)
	if err != nil || attemptTotal(got) != 12 {
		t.Fatalf("restarted within same minute: attempts=%.0f (want 12), err=%v, points=%+v", attemptTotal(got), err, got)
	}
	// Simulate this live instance having already written a flushed
	// minute aggregate. Its in-memory raw samples are still retained.
	third.mu.Lock()
	persisted := third.persistedMinute()
	third.mu.Unlock()
	if persisted.SourceID != third.sourceID {
		t.Fatal("disk point has no source identity")
	}
	if err := third.append(persisted); err != nil {
		t.Fatal(err)
	}
	got, err = third.Read(time.Now().UTC().Add(-5*time.Minute), 1000)
	if err != nil || attemptTotal(got) != 12 {
		t.Fatalf("same-source disk and memory counted twice: %.0f err=%v", attemptTotal(got), err)
	}
	if got[0].SourceID == "" {
		t.Fatal("new persisted rows lack source_id")
	}
}

func TestReviewAlertUnknownWhileMetricsDisappear(t *testing.T) {
	rule := AlertRule{ID: "high_rtt", Metric: "quic_rtt_ms", Direction: "above", Threshold: 1000, Recover: 800, HoldSeconds: 0, RecoverSeconds: 30, Enabled: true}
	e, err := NewAlerts([]AlertRule{rule})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	makePoint := func(sec int, ms *float64) Point {
		return Point{Time: at.Add(time.Duration(sec) * time.Second), Values: map[string]*float64{"quic_rtt_ms": ms}}
	}
	bad := 1500.0
	good := 500.0
	active := e.Evaluate(makePoint(0, &bad))
	if len(active) != 1 || active[0].State != "active" {
		t.Fatalf("initial alert missing: %+v", active)
	}
	restarted, err := NewAlerts([]AlertRule{rule})
	if err != nil {
		t.Fatal(err)
	}
	restarted.Restore(active)
	if len(restarted.Snapshot()["active"].([]Alert)) != 0 ||
		len(restarted.Snapshot()["unknown"].([]Alert)) != 1 {
		t.Fatal("restarted collector presented old active alert as live evidence")
	}
	missing := e.Evaluate(makePoint(3600, nil))
	if len(missing) != 1 || missing[0].State != "unknown" || missing[0].Value != 1500 || !missing[0].LastObserved.Equal(at) {
		t.Fatalf("no-data recovery falsely shown as active/resolved: %+v", missing)
	}
	state := e.Snapshot()
	if len(state["active"].([]Alert)) != 0 || len(state["unknown"].([]Alert)) != 1 {
		t.Fatal("stale alert still in active list")
	}
	if len(e.Evaluate(makePoint(7200, nil))) != 0 {
		t.Fatal("repeated missing samples must not spam events")
	}
	restored, err := NewAlerts([]AlertRule{rule})
	if err != nil {
		t.Fatal(err)
	}
	restored.Restore(state["events"].([]Alert))
	if len(restored.Snapshot()["unknown"].([]Alert)) != 1 {
		t.Fatal("unknown state not restored")
	}
	if v := e.Evaluate(makePoint(7201, &good)); len(v) != 0 {
		t.Fatal("single good sample fabricated recovery")
	}
	if len(e.Snapshot()["unknown"].([]Alert)) != 1 {
		t.Fatal("recovery hold lost unknown state")
	}
	if v := e.Evaluate(makePoint(7232, &good)); len(v) != 1 || v[0].State != "resolved" {
		t.Fatalf("recovery missing after sustained good observations: %+v", v)
	}
	if len(e.Snapshot()["unknown"].([]Alert)) != 0 {
		t.Fatal("resolved incident remained unknown")
	}
	active = e.Evaluate(makePoint(7300, &bad))
	if len(active) != 1 || active[0].State != "active" {
		t.Fatal("new incident not raised")
	}
	unknown := e.MarkUnknown(at.Add(7400 * time.Second))
	if len(unknown) != 1 || unknown[0].State != "unknown" {
		t.Fatal("paused collection left active warning")
	}
	if len(e.MarkUnknown(at.Add(7500*time.Second))) != 0 {
		t.Fatal("duplicate unknown transition")
	}
	if v := e.Evaluate(makePoint(7600, &bad)); len(v) != 1 || v[0].State != "active" {
		t.Fatal("reobserved high RTT did not reactivate")
	}
}

func TestReviewOptionalReportLogsDoNotFailVerifiedReport(t *testing.T) {
	root := t.TempDir()
	optional := []string{"frpc restart log capture was not enabled", "Xray access log was disabled"}
	summary := map[string]interface{}{"state": "ok", "optional_missing": optional, "missing": []string{}}
	info, err := WriteReportWithOptional(root, ID(), map[string][]byte{"snapshot.json": []byte("{}")}, summary, nil, optional)
	if err != nil {
		t.Fatal(err)
	}
	if info.Partial || len(info.Missing) != 0 || len(info.OptionalMissing) != 2 {
		t.Fatalf("optional logging disabled falsely marked core report incomplete: %+v", info)
	}
	if err := verifyTaskResult(TaskSpec{Kind: "report"}, info, nil); err != nil {
		t.Fatalf("optional logging disabled falsely failed verified report: %v", err)
	}
	archive, err := zip.OpenReader(filepath.Join(root, "reports", info.ID+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, member := range archive.File {
		if member.Name != "manifest.json" && member.Name != "summary.json" {
			continue
		}
		reader, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			OptionalMissing []string `json:"optional_missing"`
		}
		if json.Unmarshal(data, &saved) != nil || len(saved.OptionalMissing) != 2 {
			t.Fatalf("%s omits optional evidence list: %s", member.Name, data)
		}
	}
	reports, err := ListReports(root)
	if err != nil || len(reports) != 1 || len(reports[0].OptionalMissing) != 2 {
		t.Fatalf("optional evidence not retained in report sidecar: %+v %v", reports, err)
	}
	raw, err := json.Marshal(info)
	if err != nil || !json.Valid(raw) {
		t.Fatal("invalid persisted optional evidence metadata")
	}
	missingInfo, err := WriteReportWithOptional(root, ID(), map[string][]byte{"snapshot.json": []byte("{}")}, summary, []string{"Windows Python snapshot absent"}, optional)
	if err != nil || !missingInfo.Partial {
		t.Fatalf("missing core evidence must remain partial: %+v %v", missingInfo, err)
	}
	if err := verifyTaskResult(TaskSpec{Kind: "report"}, missingInfo, nil); err == nil {
		t.Fatal("missing core evidence silently passed verification")
	}
}
