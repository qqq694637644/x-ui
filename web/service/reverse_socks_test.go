package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"x-ui/database"
)

func TestReverseSocksWebConfigSecretsAndLifecycle(t *testing.T) {
	frpc := buildMockFRPC(t)
	configPath := filepath.Join(t.TempDir(), "frpc.toml")
	if err := database.InitDB(filepath.Join(t.TempDir(), "reverse-socks.db")); err != nil {
		t.Fatal(err)
	}
	service := NewReverseSocksService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	form := ReverseSocksForm{
		Enabled:         false,
		VisitorAddr:     "127.0.0.1:6000",
		UDPBindAddr:     "127.0.0.1:6002",
		UDPPeerAddr:     "127.0.0.1:6001",
		WARPAddr:        "127.0.0.1:40000",
		WARPUsername:    "myuser",
		WARPPassword:    "super-secret-warp-password",
		Token:           "super-secret-rsx2-token-123",
		FRPCBinaryPath:  frpc,
		FRPCConfigPath:  configPath,
		FRPSServerAddr:  "127.0.0.1",
		FRPSServerPort:  7000,
		FRPCUser:        "0123456789abcdef",
		FRPSToken:       "private-frps-token",
		XTCPVisitorName: "overseas-xtcp-visitor",
		XTCPServerName:  "rsx2_node",
		XTCPSecretKey:   "private-xtcp-key",
		STUNServer:      "1.1.1.1:3478",
	}
	view, err := service.Save(form)
	if err != nil {
		t.Fatal(err)
	}
	if view.FRPCUser != form.FRPCUser || view.XTCPVisitorName != form.XTCPVisitorName ||
		view.Token != form.Token || view.WARPPassword != form.WARPPassword ||
		view.FRPSToken != form.FRPSToken || view.XTCPSecretKey != form.XTCPSecretKey || view.Enabled {
		t.Fatalf("unexpected saved view: %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), form.Token) || !strings.Contains(string(encoded), form.WARPPassword) ||
		!strings.Contains(string(encoded), form.FRPSToken) || !strings.Contains(string(encoded), form.XTCPSecretKey) {
		t.Fatal("single-user Web response did not contain configured secrets")
	}
	form.Enabled = true
	if _, err := service.Save(form); err != nil {
		t.Fatal(err)
	}
	saved, err := service.load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "super-secret-rsx2-token-123" ||
		saved.WARPPassword != "super-secret-warp-password" ||
		saved.FRPSToken != "private-frps-token" || saved.XTCPSecretKey != "private-xtcp-key" ||
		saved.FRPCUser != form.FRPCUser || saved.XTCPVisitorName != form.XTCPVisitorName || !saved.Enabled {
		t.Fatal("Web editor changed secrets or did not enable service")
	}
	view, err = service.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.Status.State != "stopped" {
		t.Fatalf("stop did not stop managed worker: %+v", view.Status)
	}
	form.Enabled = true
	form.WARPAddr = ""
	if _, err := service.Save(form); err == nil {
		t.Fatal("enabled without mandatory WARP endpoint")
	}
	view, err = service.Get()
	if err != nil || view.Enabled {
		t.Fatalf("invalid update damaged saved config: %+v, err=%v", view, err)
	}
	// Configuration survives a simulated panel restart.
	other := NewReverseSocksService()
	if err := other.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer other.Stop()
	view, err = other.Get()
	if err != nil || view.WARPAddr != "127.0.0.1:40000" || view.Token != form.Token ||
		view.FRPCUser != form.FRPCUser || view.XTCPVisitorName != form.XTCPVisitorName {
		t.Fatalf("restart did not restore saved config: %+v, err=%v", view, err)
	}
	// Unlike the old masked 'leave blank to keep' UI, clearing a field
	// really clears it when the user explicitly saves a disabled form.
	form.Enabled = false
	form.WARPAddr = "127.0.0.1:40000"
	form.Token, form.WARPPassword, form.FRPSToken, form.XTCPSecretKey = "", "", "", ""
	if _, err := other.Save(form); err != nil {
		t.Fatal(err)
	}
	view, err = other.Get()
	if err != nil || view.Token != "" || view.WARPPassword != "" ||
		view.FRPCUser != form.FRPCUser || view.XTCPVisitorName != form.XTCPVisitorName ||
		view.FRPSToken != "" || view.XTCPSecretKey != "" {
		t.Fatalf("explicitly cleared credentials were retained: %+v, err=%v", view, err)
	}
}

// buildMockFRPC creates an actual executable that exercises the same
// subprocess contract as the custom private frpc (verify -c / -c).
func buildMockFRPC(t *testing.T) string {
	t.Helper()
	name := "frpc-mock"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/mockfrpc")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build frpc subprocess fixture: %v: %s", err, output)
	}
	return bin
}

func TestReverseSocksManagedFRPCVerifyLifecycleAndIsolation(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "owned-frpc.db")); err != nil {
		t.Fatal(err)
	}
	bin := buildMockFRPC(t)
	configFile := filepath.Join(t.TempDir(), "private", "frpc.toml")
	s := NewReverseSocksService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	form := ReverseSocksForm{
		Enabled:         true,
		VisitorAddr:     "127.0.0.1:6000",
		UDPBindAddr:     "127.0.0.1:6002",
		UDPPeerAddr:     "127.0.0.1:6001",
		WARPAddr:        "127.0.0.1:40000",
		Token:           "long-private-rsx2-shared-token",
		FRPCBinaryPath:  bin,
		FRPCConfigPath:  configFile,
		FRPSServerAddr:  "127.0.0.1",
		FRPSServerPort:  7000,
		FRPCUser:        "0123456789abcdef",
		FRPSToken:       "private-frps-token",
		XTCPVisitorName: "overseas-xtcp-visitor",
		XTCPServerName:  "rsx2_node",
		XTCPSecretKey:   "private-xtcp-key",
		STUNServer:      "1.1.1.1:3478",
	}
	view, err := s.Save(form)
	if err != nil {
		t.Fatal(err)
	}
	if view.FRPC.State != "running" || view.FRPC.PID == 0 {
		t.Fatalf("managed frpc did not launch: %+v", view.FRPC)
	}
	firstPID := view.FRPC.PID
	contents, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"user = \"0123456789abcdef\"",
		"name = \"overseas-xtcp-visitor\"",
		"serverName = \"rsx2_node\"",
		"transport.wireProtocol = \"v1\"",
		"auth.token = \"private-frps-token\"",
		"transport.proxyURL = \"socks5://127.0.0.1:40000\"",
		"udpProxyURL = \"socks5://127.0.0.1:40000\"",
		"disableAssistedAddrs = true",
	} {
		if !strings.Contains(string(contents), field) {
			t.Fatalf("managed frpc config missing %q", field)
		}
	}
	if runtime.GOOS != "windows" {
		if stat, err := os.Stat(configFile); err != nil || stat.Mode().Perm() != 0600 {
			t.Fatalf("managed frpc config must be 0600, stat=%v err=%v", stat, err)
		}
	}
	if err := s.VerifyFRPC(form); err != nil {
		t.Fatalf("verify current form: %v", err)
	}
	// Unsaved identity edits must be validated from the current browser
	// form without replacing the stored identity or restarting frpc.
	form.FRPCUser, form.XTCPVisitorName = "unsaved-user", "unsaved-visitor"
	if err := s.VerifyFRPC(form); err != nil {
		t.Fatalf("verify edited user and Visitor name: %v", err)
	}
	if saved, err := s.load(); err != nil || saved.FRPCUser != "0123456789abcdef" ||
		saved.XTCPVisitorName != "overseas-xtcp-visitor" {
		t.Fatalf("verify unexpectedly replaced stored frpc identity: %+v, err=%v", saved.reverseFRPCConfig, err)
	}
	form.FRPCUser, form.XTCPVisitorName = "0123456789abcdef", "overseas-xtcp-visitor"
	// Screenshot regression: the stored STUN is still 1.1.1.1:3478,
	// while the browser shows an unsaved DNS name or numeric Google IP.
	// Verify must test the browser form, not the outdated database value.
	for _, pendingSTUN := range []string{"stun.miwifi.com:3478", "74.125.250.129:19302"} {
		form.STUNServer = pendingSTUN
		if err := s.VerifyFRPC(form); err != nil {
			t.Fatalf("valid current unsaved STUN %q was rejected: %v", pendingSTUN, err)
		}
		saved, err := s.load()
		if err != nil || saved.STUNServer != "1.1.1.1:3478" {
			t.Fatal("VerifyFRPC persisted unsaved STUN value")
		}
	}
	form.STUNServer = "invalid..host:3478"
	if err := s.VerifyFRPC(form); err == nil {
		t.Fatal("VerifyFRPC ignored the currently entered invalid STUN hostname")
	}
	form.STUNServer = "1.1.1.1:3478"
	if view, err = s.Restart(); err != nil || view.FRPC.State != "running" || view.FRPC.PID == firstPID {
		t.Fatalf("managed frpc did not restart: %+v %v", view.FRPC, err)
	}
	if logs := s.RestartLogs(); logs.Enabled {
		t.Fatalf("ordinary restart unexpectedly enabled frpc Web logging: %+v", logs)
	}
	// Explicit restart logging must capture both child streams and redact
	// stored credentials before the authenticated Web API returns them.
	t.Setenv("MOCK_FRPC_EMIT_LOGS", "1")
	t.Setenv("MOCK_FRPC_LOG_SECRET", "raw-private-credential")
	if view, err = s.RestartWithLogs(true); err != nil || view.FRPC.State != "running" {
		t.Fatalf("restart with Web logging failed: %+v %v", view.FRPC, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		logs := s.RestartLogs()
		if logs.Enabled && strings.Contains(logs.Output, "mock frpc startup stdout") &&
			strings.Contains(logs.Output, "mock frpc startup stderr") &&
			strings.Contains(logs.Output, "raw-private-credential") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Web restart logs did not capture both streams: %+v", logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Re-saving with changed identity must write it to the real managed
	// frpc.toml; a later Restart and panel restart must retain it.
	form.FRPCUser = "new-frps-user"
	form.XTCPVisitorName = "new-custom-visitor"
	view, err = s.Save(form)
	if err != nil || view.FRPCUser != form.FRPCUser ||
		view.XTCPVisitorName != form.XTCPVisitorName || view.FRPC.State != "running" {
		t.Fatalf("Save did not apply the newly selected frpc identity: %+v, err=%v", view, err)
	}
	contents, err = os.ReadFile(configFile)
	if err != nil || !strings.Contains(string(contents), "user = \"new-frps-user\"") ||
		!strings.Contains(string(contents), "name = \"new-custom-visitor\"") {
		t.Fatalf("Save overwrote the custom user or Visitor name: %s, err=%v", contents, err)
	}
	view, err = s.Restart()
	if err != nil || view.FRPCUser != form.FRPCUser ||
		view.XTCPVisitorName != form.XTCPVisitorName || view.FRPC.State != "running" {
		t.Fatalf("Restart lost the frpc identity: %+v, err=%v", view, err)
	}
	if logs := s.RestartLogs(); logs.Enabled {
		t.Fatal("restart without log checkbox retained previous captured output")
	}
	secondPID := view.FRPC.PID
	t.Setenv("MOCK_FRPC_LAX_VERIFY", "1")
	if _, err := s.Save(ReverseSocksForm{
		Enabled: true, VisitorAddr: form.VisitorAddr,
		UDPBindAddr: form.UDPBindAddr, UDPPeerAddr: form.UDPPeerAddr,
		WARPAddr: form.WARPAddr, Token: form.Token,
		FRPCBinaryPath: bin, FRPCConfigPath: configFile,
		FRPSServerAddr: form.FRPSServerAddr, FRPSServerPort: form.FRPSServerPort,
		FRPCUser: form.FRPCUser, XTCPVisitorName: form.XTCPVisitorName,
		XTCPServerName: form.XTCPServerName, STUNServer: form.STUNServer,
		FRPSToken: form.FRPSToken, XTCPSecretKey: form.XTCPSecretKey,
	}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("insecure legacy frpc accepted despite ignoring private XTCP options: %v", err)
	}
	t.Setenv("MOCK_FRPC_LAX_VERIFY", "")
	if view, err := s.Get(); err != nil || view.FRPC.PID != secondPID {
		t.Fatalf("legacy frpc verification changed the live child: %+v %v", view.FRPC, err)
	}
	form.FRPCBinaryPath = filepath.Join(t.TempDir(), "missing-frpc")
	form.FRPSToken, form.XTCPSecretKey = "", ""
	if _, err := s.Save(form); err == nil {
		t.Fatal("accepted unavailable custom frpc executable")
	}
	if view, err := s.Get(); err != nil || view.FRPC.PID != secondPID {
		t.Fatalf("failed update killed running frpc: %+v %v", view.FRPC, err)
	}
	// Explicit stop must not stop an unrelated frpc process.
	external := exec.Command(bin, "-c", configFile)
	if err := external.Start(); err != nil {
		t.Fatal(err)
	}
	externalDone := make(chan error, 1)
	go func() { externalDone <- external.Wait() }()
	defer func() {
		_ = external.Process.Kill()
		<-externalDone
	}()
	if _, err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if status := s.frpc.Status(); status.State != "stopped" || status.PID != 0 {
		t.Fatalf("owned frpc not stopped: %+v", status)
	}
	select {
	case err := <-externalDone:
		t.Fatalf("stopped external frpc process: %v", err)
	default:
	}
	// Saved enabled configuration must be restored after an x-ui restart.
	if _, err := s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	restarted := NewReverseSocksService()
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	if view, err := restarted.Get(); err != nil || view.FRPC.State != "running" ||
		view.FRPCUser != "new-frps-user" || view.XTCPVisitorName != "new-custom-visitor" {
		t.Fatalf("x-ui restart did not restore the private frpc: %+v, err=%v", view.FRPC, err)
	}
}
