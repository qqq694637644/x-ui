package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/reverseexit"
	"x-ui/rsxdiag"
)

func TestDiagnosticsManagedConfigKeepsWarpTransportSeparate(t *testing.T) {
	c := defaultReverseFRPCConfig()
	c.FRPCBinaryPath = buildMockFRPC(t)
	c.FRPCConfigPath = filepath.Join(t.TempDir(), "frpc.toml")
	c.FRPSServerAddr = "203.0.113.7"
	c.FRPSToken = "fake-test-frps-token"
	c.XTCPSecretKey = "fake-test-xtcp-secret"
	c.STUNServer = "127.0.0.1:3478"
	agent := reverseexit.DefaultConfig()
	agent.WARPAddr = "127.0.0.1:40000"
	agent.TargetSOCKSAddr = "127.0.0.1:40001"
	agent.Token = "diagnostic-test-token-more-than-32-characters"
	agent.DiagnosticsEnabled = true
	agent.FRPCDiagnosticsAddr = "127.0.0.1:9321"
	raw, err := renderManagedFRPC(c, agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"diagnostics.enabled = true", "diagnostics.listenAddr = \"127.0.0.1:9321\"", "diagnostics.token = \"" + rsxdiag.LocalToken(agent.Token) + "\"", "transport.proxyURL = \"socks5://127.0.0.1:40000\"", "udpProxyURL = \"socks5://127.0.0.1:40000\""} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing generated diagnostic/WARP value %q", expected)
		}
	}
	if strings.Index(raw, "diagnostics.enabled") > strings.Index(raw, "[[visitors]]") || strings.Contains(raw, "40001") {
		t.Fatal("diagnostics nested under wrong TOML table or target Xray applied to frpc")
	}
	if _, err := verifyRuntimePrivacy(raw, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimePrivacy(strings.ReplaceAll(raw, "40000", "40001"), agent); err == nil {
		t.Fatal("wrong WARP route accepted")
	}
	agent.DiagnosticsEnabled = false
	raw, err = renderManagedFRPC(c, agent)
	if err != nil || strings.Contains(raw, "diagnostics.enabled") {
		t.Fatal("disabled diagnostics changed prior frpc config")
	}
}
func TestDiagnosticsSettingsAndSnapshotMissingData(t *testing.T) {
	cfg := reverseSocksSaved{Config: reverseexit.DefaultConfig(), reverseFRPCConfig: defaultReverseFRPCConfig()}
	cfg.FRPCConfigPath = filepath.Join(t.TempDir(), "frpc.toml")
	cfg.WARPAddr = "127.0.0.1:1"
	cfg.Token = "diagnostic-test-token-more-than-32-characters"
	cfg.FRPCDiagnosticsAddr = "127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := newDiagnostics(ctx, cfg, reverseexit.NewManager(), newManagedFRPC(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	snapshot := d.Snapshot()
	node, ok := snapshot["node"].(diagnosticSource)
	if !ok || !node.Stale || node.AgeSeconds != -1 {
		t.Fatal("uncollected node reported as healthy")
	}
	if snapshot["agent"] != nil {
		t.Fatal("missing Agent statistics reported as zero")
	}
	settings := d.Settings()
	settings.Paused = true
	settings.HistoryBudgetMiB = 4
	settings.RetentionDays = 7
	if err := d.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	settings.RetentionDays = 3
	if err := d.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	var loaded DiagnosticsSettings
	if err := rsxdiag.ReadSettings(d.root, &loaded); err != nil || loaded.RetentionDays != 3 {
		t.Fatalf("settings not persisted %v", err)
	}
	if _, err := d.StartTask(rsxdiag.TaskSpec{Kind: "self_test"}); err == nil {
		t.Fatal("paused collector started a probe")
	}
	settings.RetentionDays = 31
	if err := d.SetSettings(settings); err == nil {
		t.Fatal("unbounded retention accepted")
	}
}
func TestDiagnosticsCounterResetsAndRatioWindows(t *testing.T) {
	d := &DiagnosticsRuntime{counters: map[string]counterObservation{}}
	now := time.Now()
	if _, _, ok := d.delta("process/conn", 100, now); ok {
		t.Fatal("first observation invented rate")
	}
	delta, seconds, ok := d.delta("process/conn", 300, now.Add(time.Second))
	if !ok || delta != 200 || seconds != 1 {
		t.Fatal("incorrect rate delta")
	}
	if _, _, ok := d.delta("process/conn", 1, now.Add(2*time.Second)); ok {
		t.Fatal("counter reset produced negative rate")
	}
	if _, _, ok := d.delta("process/conn", 500, now.Add(time.Minute)); ok {
		t.Fatal("sample gap reported as continuous observation")
	}
	p := rsxdiag.Point{Time: now, Values: map[string]*float64{}, Totals: map[string]float64{"quic_sent_packets": 100, "quic_loss_declarations": 10}}
	d.addQualityWindows(&p)
	p = rsxdiag.Point{Time: now.Add(time.Second), Values: map[string]*float64{}, Totals: map[string]float64{"quic_sent_packets": 900, "quic_loss_declarations": 0}}
	d.addQualityWindows(&p)
	if got := p.Values["quic_loss_pct_10s"]; got == nil || *got != 1 {
		t.Fatalf("ratio must weight packet counts, got %v", got)
	}
}
func TestDiagnosticsXrayEvidenceDoesNotGuessRulesOrReuseTuples(t *testing.T) {
	line := "2026/10/18 14:00:00.123 from tcp:127.0.0.1:45123 accepted tcp:original.example:443 [rsx3-egress -> warp]"
	result := correlateOutbound(line, "127.0.0.1:45123", "original.example:443")
	if result["verified"] != true || result["outbound"] != "warp" || result["rule_verified"] != false {
		t.Fatalf("wrong actual route result: %+v", result)
	}
	for _, bad := range []string{"127.0.0.1:4512", "127.0.0.1:451234"} {
		if result := correlateOutbound(line, bad, "original.example:443"); result["verified"] == true {
			t.Fatal("substring port matched a different TCP connection")
		}
	}
	ambiguous := line + "\n" + strings.ReplaceAll(line, "-> warp", "-> freedom")
	if correlateOutbound(ambiguous, "127.0.0.1:45123", "original.example:443")["verified"] == true {
		t.Fatal("ambiguous routing reported as certain")
	}
	start := time.Date(2026, 10, 18, 15, 0, 0, 0, time.Local)
	if correlateOutboundWithin(line, "127.0.0.1:45123", "original.example:443", start, nil)["verified"] == true {
		t.Fatal("stale connection tuple reused without timestamp evidence")
	}
	if correlateOutbound("", "127.0.0.1:45123", "original.example:443")["state"] != "unverified" {
		t.Fatal("missing access log inferred a route")
	}
}
func TestDiagnosticsIPEchoAndPartialCaptureReport(t *testing.T) {
	for _, raw := range []string{"104.28.1.2", `{"ip":"104.28.1.2"}`} {
		if ip, err := parseEchoIP([]byte(raw)); err != nil || ip != "104.28.1.2" {
			t.Fatalf("valid source IP rejected %s %v", ip, err)
		}
	}
	for _, raw := range []string{"not an IP", "127.0.0.1", "0.0.0.0", `{"not_ip":"104.28.1.2"}`} {
		if _, err := parseEchoIP([]byte(raw)); err == nil {
			t.Fatalf("unverified IP response accepted %q", raw)
		}
	}
	if captureIncomplete([]byte(`{"complete":true,"state":"stopped","bytes":100,"dropped_events":0}`)) != "" {
		t.Fatal("valid capture marked missing")
	}
	for _, raw := range []string{`null`, `{"complete":false,"state":"size_limit","bytes":100}`, `{"complete":true,"state":"stopped","bytes":0}`, `{"complete":true,"state":"stopped","bytes":100,"dropped_events":1}`} {
		if captureIncomplete([]byte(raw)) == "" {
			t.Fatal("incomplete capture labelled complete")
		}
	}
}
func TestDiagnosticsReportTaskStoresPrivateUnredactedBundle(t *testing.T) {
	cfg := reverseSocksSaved{Config: reverseexit.DefaultConfig(), reverseFRPCConfig: defaultReverseFRPCConfig()}
	cfg.FRPCConfigPath = filepath.Join(t.TempDir(), "frpc.toml")
	cfg.Token = "diagnostic-test-token-more-than-32-characters"
	cfg.WARPAddr = "127.0.0.1:1"
	cfg.FRPCDiagnosticsAddr = "127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := newDiagnostics(ctx, cfg, reverseexit.NewManager(), newManagedFRPC(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.StartTask(rsxdiag.TaskSpec{Kind: "report"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for d.runner.Active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	tasks := d.runner.Snapshot()
	// A report with missing sources is successfully saved, but its diagnostic
	// verification must fail explicitly rather than being labelled passed.
	if tasks[0].State != "completed_failed" || tasks[0].Error == "" {
		t.Fatalf("report task: %+v", tasks[0])
	}
	path, err := rsxdiag.ReportPath(d.root, task.ID, "zip")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatal("report missing")
	}
	reports, err := rsxdiag.ListReports(d.root)
	if err != nil || len(reports) != 1 || !reports[0].Partial || reports[0].Redacted {
		t.Fatal("unknown sources not marked partial/unredacted")
	}
	raw, err := json.Marshal(d.Snapshot())
	if err != nil || !json.Valid(raw) {
		t.Fatal("diagnostic snapshot cannot serialize")
	}
}
