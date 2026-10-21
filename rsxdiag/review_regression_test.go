package rsxdiag

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func awaitReviewTask(t *testing.T, r *Runner) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tasks := r.Snapshot()
		if len(tasks) > 0 && tasks[0].State != "running" && tasks[0].State != "cancelling" {
			return tasks[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("diagnostic task did not finish")
	return Task{}
}

func TestReviewTaskStatusChecksMeasuredResult(t *testing.T) {
	cases := []struct {
		name     string
		spec     TaskSpec
		result   interface{}
		steps    []Step
		expected string
	}{
		{"RPC200ButProbeFailed", TaskSpec{Kind: "raw_benchmark", Bytes: 1_000_000, Direction: "download"}, map[string]interface{}{"state": "failed", "bytes": 1_000_000}, nil, "completed_failed"},
		{"ShortDownload", TaskSpec{Kind: "target_benchmark", Bytes: 50_000_000}, map[string]interface{}{"state": "ok", "bytes": 1_000_000}, nil, "completed_failed"},
		{"XrayUnverified", TaskSpec{Kind: "route_check"}, map[string]interface{}{"routing": map[string]interface{}{"state": "unverified"}}, nil, "completed_failed"},
		{"ProbePayloadNotVerified", TaskSpec{Kind: "socks_benchmark", Bytes: 1_000_000, Direction: "download"}, map[string]interface{}{"state": "ok", "bytes": 1_000_000, "verified": false}, nil, "completed_failed"},
		{"PartialReport", TaskSpec{Kind: "report"}, map[string]interface{}{"partial": true}, nil, "completed_failed"},
		{"SelfTestDegraded", TaskSpec{Kind: "self_test"}, map[string]interface{}{"state": "degraded"}, []Step{{Name: "udp", State: "failed", Error: "timeout"}}, "completed_failed"},
		{"FullyVerified", TaskSpec{Kind: "raw_benchmark", Bytes: 1_000_000, Direction: "download"}, map[string]interface{}{"state": "ok", "bytes": 1_000_000, "verified": true}, nil, "completed_passed"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner()
			defer r.Close()
			_, err := r.Start(context.Background(), tt.spec, func(ctx context.Context, id string, add func(Step)) (interface{}, error) {
				for _, s := range tt.steps {
					add(s)
				}
				return tt.result, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got := awaitReviewTask(t, r)
			if got.State != tt.expected {
				t.Fatalf("task status %s, want %s: %+v", got.State, tt.expected, got)
			}
			if tt.expected == "completed_failed" && got.Error == "" {
				t.Fatal("failed task missing explanatory error")
			}
		})
	}
}

func TestReviewRecentHistoryRecoversDiskAfterRestart(t *testing.T) {
	dir := t.TempDir()
	history, err := NewHistory(dir, 30, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Minute)
	up := 1.0
	history.Add(Point{Time: old.Add(5 * time.Second), Seconds: 5, Values: map[string]*float64{"warp_online": &up}, Totals: map[string]float64{"attempts": 3}})
	history.Close()
	restored, err := NewHistory(dir, 30, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Read(time.Now().Add(-5*time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Totals["attempts"] != 3 {
		t.Fatalf("short history not restored: %+v", got)
	}
	// A new sample in the same boot minute is distinct from a persisted
	// pre-restart partial minute and may not silently replace it.
	now := time.Now().UTC()
	restored.Add(Point{Time: now, Seconds: 5, Values: map[string]*float64{"warp_online": &up}})
	got, err = restored.Read(time.Now().Add(-5*time.Minute), 1000)
	if err != nil || len(got) != 2 {
		t.Fatalf("disk + memory recent not merged: %+v err=%v", got, err)
	}
	// The exact 60-minute UI view also mixes live five-second points with
	// persisted minutes, despite the small elapsed time during the query.
	got, err = restored.Read(time.Now().Add(-time.Hour), 1000)
	if err != nil || len(got) != 2 {
		t.Fatalf("standard 60-minute view lost samples: %+v err=%v", got, err)
	}
}

func TestReviewReportManifestAndSidecarArchiveIntegrity(t *testing.T) {
	root := t.TempDir()
	id := ID()
	raw := []byte("unredacted=203.0.113.77 domain=private.example:443")
	info, err := WriteReport(root, id, map[string][]byte{"original.log": raw}, map[string]interface{}{"phase": "rsx3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "reports", id+".zip")
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	if info.Bytes != int64(len(archive)) || info.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("external ZIP metadata wrong")
	}
	sidecar, err := os.ReadFile(filepath.Join(root, "reports", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored ReportInfo
	if json.Unmarshal(sidecar, &stored) != nil || stored.SHA256 != info.SHA256 || stored.Bytes != info.Bytes {
		t.Fatalf("sidecar not final ZIP digest: %s", sidecar)
	}
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var manifest ReportManifest
	for _, entry := range z.File {
		if entry.Name != "manifest.json" {
			continue
		}
		f, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || json.Unmarshal(data, &manifest) != nil {
			t.Fatalf("bad manifest: %s %v", data, err)
		}
	}
	if manifest.ArchiveMetadata != id+".json" || manifest.Files["original.log"] == "" || manifest.Redacted {
		t.Fatalf("manifest scope inconsistent: %+v", manifest)
	}
	if !strings.Contains(manifest.IntegrityScope, "sidecar") {
		t.Fatal("ZIP hash location not explained")
	}
	if manifest.Files["original.log"] != hex.EncodeToString(func() []byte { h := sha256.Sum256(raw); return h[:] }()) {
		t.Fatal("member hash mismatch")
	}
	if !bytes.Contains(raw, []byte("private.example")) {
		t.Fatal("original report was unexpectedly changed")
	}
}
