package rsxdiag

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func value(v float64) *float64 { return &v }
func TestMeterLiveCountsExactlyAndBounds(t *testing.T) {
	m := NewMeter("test")
	s := m.Open("epoch-a", 1, "original.example:443")
	if got := m.Snapshot(10); got.Metrics.Active != 1 || got.Metrics.Latency["p95"] != nil {
		t.Fatal("active or unmeasured latency incorrect")
	}
	s.Ready(time.Millisecond, time.Millisecond, time.Millisecond)
	s.Ready(0, 0, 0)
	payload := bytes.Repeat([]byte("data"), 1024)
	var dst bytes.Buffer
	n, err := s.Copy(&dst, bytes.NewReader(payload), "download")
	if err != nil || n != int64(len(payload)) {
		t.Fatal(err)
	}
	s.Finish("download", nil)
	s.Finish("download", errors.New("duplicate close"))
	got := m.Snapshot(10)
	if got.Metrics.Active != 0 || got.Metrics.Success != 1 || got.Metrics.Failures != 0 || got.Metrics.Closed != 1 || got.Metrics.DownloadBytes != uint64(len(payload)) {
		t.Fatalf("counter mismatch %+v", got.Metrics)
	}
	if got.Sessions[0].Target != "original.example:443" {
		t.Fatal("target redacted")
	}
	for i := uint32(2); i < 400; i++ {
		span := m.Open("epoch-a", i, "x:1")
		span.Finish("open", errors.New("connection reset by peer"))
	}
	if len(m.Snapshot(1024).Sessions) > MaxRecent {
		t.Fatal("session ring unbounded")
	}
	for i := 0; i < 1000; i++ {
		m.Event("test", "original details", "")
	}
	if len(m.Snapshot(1).Events) > MaxEvents {
		t.Fatal("event ring unbounded")
	}
}
func TestMeterConcurrentAccounting(t *testing.T) {
	m := NewMeter("test")
	var wg sync.WaitGroup
	for i := uint32(0); i < 20; i++ {
		wg.Add(1)
		go func(id uint32) {
			defer wg.Done()
			s := m.Open("epoch", id, "t:443")
			s.Ready(0, 0, 0)
			_, _ = s.Copy(io.Discard, bytes.NewReader(make([]byte, 1024)), "upload")
			s.Finish("upload", nil)
		}(i)
	}
	for i := 0; i < 100; i++ {
		_ = m.Snapshot(16)
	}
	wg.Wait()
	if got := m.Snapshot(16); got.Metrics.UploadBytes != 20*1024 || got.Metrics.Active != 0 {
		t.Fatalf("counter mismatch %+v", got.Metrics)
	}
}
func TestHistoryPersistenceRetentionAndCoverage(t *testing.T) {
	dir := t.TempDir()
	h, err := NewHistory(dir, 30, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start := now.Add(-2 * time.Hour).Truncate(time.Minute)
	hist := NewHistogram()
	ObserveHistogram(hist, 450)
	p := Point{Time: start, Seconds: 5, Values: map[string]*float64{"xtcp_online": value(1), "rtt_ms": value(450)}, Totals: map[string]float64{"download_bytes": 1000}, Histograms: map[string][]uint64{"rtt_ms": hist}}
	h.Add(p)
	p.Time = start.Add(65 * time.Second)
	p.Values = map[string]*float64{"xtcp_online": value(0), "rtt_ms": nil}
	h.Add(p)
	h.Close()
	reopened, err := NewHistory(dir, 30, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	points, err := reopened.Read(start.Add(-time.Minute), 100)
	if err != nil || len(points) != 2 {
		t.Fatalf("history reopen count=%d err=%v", len(points), err)
	}
	summary := Summarize(points, 3*time.Hour)
	if summary.ObservedSeconds != 10 || summary.CoveragePct > 1 {
		t.Fatalf("unobserved time counted as up %+v", summary)
	}
	if summary.Availability["xtcp_online"] == nil || *summary.Availability["xtcp_online"] != 50 {
		t.Fatal("availability denominator incorrect")
	}
	old := filepath.Join(dir, now.AddDate(0, 0, -35).Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reopened.prune(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired history retained")
	}
	large := filepath.Join(dir, now.AddDate(0, 0, -1).Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(large, make([]byte, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reopened.prune(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(large); !os.IsNotExist(err) {
		t.Fatal("disk cap not enforced")
	}
}
func TestHistoryRingBoundedAndHistogramMerging(t *testing.T) {
	h, err := NewHistory(t.TempDir(), 30, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	for i := 0; i < 800; i++ {
		h.Add(Point{Time: now.Add(time.Duration(i-800) * 5 * time.Second), Seconds: 5, Values: map[string]*float64{"xtcp_online": value(1)}, Totals: map[string]float64{}})
	}
	if got := len(h.Recent(now.Add(-2 * time.Hour))); got > 720 {
		t.Fatalf("unbounded ring %d", got)
	}
	a, b := NewHistogram(), NewHistogram()
	for i := 0; i < 99; i++ {
		ObserveHistogram(a, 10)
	}
	ObserveHistogram(b, 1000)
	MergeHistogram(a, b)
	q := HistogramQuantiles(a)
	if q["p95_upper_ms"] != float64(10) {
		t.Fatalf("averaged percentiles instead of merged histogram %+v", q)
	}
}
func TestAlertHysteresisMissingSamplesAndDedup(t *testing.T) {
	rules := []AlertRule{{ID: "down", Metric: "xtcp_online", Direction: "below", Threshold: .5, Recover: .9, HoldSeconds: 30, RecoverSeconds: 5, Enabled: true}}
	e, err := NewAlerts(rules)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	point := func(seconds int, v *float64) Point {
		return Point{Time: at.Add(time.Duration(seconds) * time.Second), Values: map[string]*float64{"xtcp_online": v}}
	}
	if len(e.Evaluate(point(0, nil))) != 0 || len(e.Evaluate(point(1, value(0)))) != 0 || len(e.Evaluate(point(20, value(0)))) != 0 {
		t.Fatal("premature alert")
	}
	if a := e.Evaluate(point(31, value(0))); len(a) != 1 || a[0].State != "active" {
		t.Fatalf("alert not raised %+v", a)
	}
	if len(e.Evaluate(point(40, value(0)))) != 0 {
		t.Fatal("duplicate sustained alert")
	}
	if len(e.Evaluate(point(50, value(1)))) != 0 {
		t.Fatal("recovered without hold")
	}
	if a := e.Evaluate(point(55, value(1))); len(a) != 1 || a[0].State != "resolved" {
		t.Fatal("no recovery event")
	}
}
func TestRunnerLimitsCancelAndNoAutomaticNetworkActions(t *testing.T) {
	r := NewRunner()
	defer r.Close()
	started := make(chan struct{})
	task, err := r.Start(context.Background(), TaskSpec{Kind: "self_test"}, func(ctx context.Context, id string, step func(Step)) (interface{}, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := r.Start(context.Background(), TaskSpec{Kind: "report"}, nil); err == nil {
		t.Fatal("parallel task accepted")
	}
	if err := r.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for r.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := r.Snapshot()[0].State; state != "cancelled" {
		t.Fatalf("bad cancel state %s", state)
	}
	if _, err := r.Start(context.Background(), TaskSpec{Kind: "raw_benchmark", Bytes: 1 << 40, Direction: "download"}, nil); err == nil {
		t.Fatal("unsafe unbounded traffic accepted")
	}
}
func TestRawReportNoRedactionEscapesHTMLAndRejectsPaths(t *testing.T) {
	root := t.TempDir()
	id := ID()
	raw := []byte("203.0.113.7 example.test:443 <script>alert(1)</script>")
	info, err := WriteReport(root, id, map[string][]byte{"original.log": raw}, map[string]interface{}{"raw": string(raw)}, []string{"node unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Redacted || !info.Partial {
		t.Fatal("report scope incorrect")
	}
	path, err := ReportPath(root, id, "zip")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	found := false
	for _, entry := range z.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name == "original.log" {
			found = true
			if !bytes.Equal(data, raw) {
				t.Fatal("raw log altered")
			}
		}
		if entry.Name == "report.html" && bytes.Contains(data, []byte("<script>alert")) {
			t.Fatal("raw log HTML injection")
		}
	}
	if !found {
		t.Fatal("raw file missing")
	}
	if _, err := ReportPath(root, "../passwords", "zip"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := WriteReport(root, ID(), map[string][]byte{"../secret": raw}, nil, nil); err == nil {
		t.Fatal("zip traversal accepted")
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteReport(root, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPath(root, id, "zip"); err == nil {
		t.Fatal("report deletion failed")
	}
}
func TestLocalClientLoopbackAuthAndResponseLimits(t *testing.T) {
	secret := strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "ok"})
	}))
	defer srv.Close()
	client := LocalClient{Addr: strings.TrimPrefix(srv.URL, "http://"), Token: secret}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Do(ctx, "GET", "/v1/snapshot", nil, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(ctx, "GET", "/v1/snapshot", nil, 1); err == nil {
		t.Fatal("response bound ignored")
	}
	for _, address := range []string{"localhost:9320", "0.0.0.0:9320", "203.0.113.1:9320", "127.0.0.1:0"} {
		if (LocalClient{Addr: address, Token: secret}).Validate() == nil {
			t.Fatal("unsafe local diagnostic endpoint accepted")
		}
	}
}
