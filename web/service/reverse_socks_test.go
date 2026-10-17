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
		Enabled:        false,
		VisitorAddr:    "127.0.0.1:6000",
		UDPBindAddr:    "127.0.0.1:6002",
		UDPPeerAddr:    "127.0.0.1:6001",
		WARPAddr:       "127.0.0.1:40000",
		WARPUsername:   "myuser",
		WARPPassword:   "super-secret-warp-password",
		Token:          "super-secret-rsx2-token-123",
		FRPCBinaryPath: frpc,
		FRPCConfigPath: configPath,
		FRPSServerAddr: "127.0.0.1",
		FRPSServerPort: 7000,
		FRPSToken:      "private-frps-token",
		XTCPServerName: "rsx2_node",
		XTCPSecretKey:  "private-xtcp-key",
		STUNServer:     "1.1.1.1:3478",
	}
	view, err := service.Save(form)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasToken || !view.HasWARPPassword || !view.HasFRPSToken || !view.HasXTCPSecretKey || view.Enabled {
		t.Fatalf("unexpected saved view: %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), form.Token) || strings.Contains(string(encoded), form.WARPPassword) ||
		strings.Contains(string(encoded), form.FRPSToken) || strings.Contains(string(encoded), form.XTCPSecretKey) {
		t.Fatal("Web JSON exposed an RSX2/WARP secret")
	}
	form.WARPPassword = ""
	form.Token = ""
	form.FRPSToken = ""
	form.XTCPSecretKey = ""
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
		saved.FRPSToken != "private-frps-token" || saved.XTCPSecretKey != "private-xtcp-key" || !saved.Enabled {
		t.Fatal("update cleared stored secrets or did not enable service")
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
	if err != nil || view.WARPAddr != "127.0.0.1:40000" || !view.HasToken {
		t.Fatalf("restart did not restore saved config: %+v, err=%v", view, err)
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
		Enabled:        true,
		VisitorAddr:    "127.0.0.1:6000",
		UDPBindAddr:    "127.0.0.1:6002",
		UDPPeerAddr:    "127.0.0.1:6001",
		WARPAddr:       "127.0.0.1:40000",
		Token:          "long-private-rsx2-shared-token",
		FRPCBinaryPath: bin,
		FRPCConfigPath: configFile,
		FRPSServerAddr: "127.0.0.1",
		FRPSServerPort: 7000,
		FRPSToken:      "private-frps-token",
		XTCPServerName: "rsx2_node",
		XTCPSecretKey:  "private-xtcp-key",
		STUNServer:     "1.1.1.1:3478",
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
	if err := s.VerifyFRPC(); err != nil {
		t.Fatalf("verify saved frpc configuration: %v", err)
	}
	if view, err = s.Restart(); err != nil || view.FRPC.State != "running" || view.FRPC.PID == firstPID {
		t.Fatalf("managed frpc did not restart: %+v %v", view.FRPC, err)
	}
	secondPID := view.FRPC.PID
	t.Setenv("MOCK_FRPC_LAX_VERIFY", "1")
	if _, err := s.Save(ReverseSocksForm{
		Enabled: true, VisitorAddr: form.VisitorAddr,
		UDPBindAddr: form.UDPBindAddr, UDPPeerAddr: form.UDPPeerAddr,
		WARPAddr: form.WARPAddr, Token: form.Token,
		FRPCBinaryPath: bin, FRPCConfigPath: configFile,
		FRPSServerAddr: form.FRPSServerAddr, FRPSServerPort: form.FRPSServerPort,
		XTCPServerName: form.XTCPServerName, STUNServer: form.STUNServer,
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
	if view, err := restarted.Get(); err != nil || view.FRPC.State != "running" {
		t.Fatalf("x-ui restart did not restore the private frpc: %+v, err=%v", view.FRPC, err)
	}
}
