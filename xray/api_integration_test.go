package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Optional real-kernel test. CI sets RSX_XRAY_26327_BIN to the unmodified
// official v26.3.27 binary. The test does not require a running x-ui service.
func TestXray26327RealStatsServiceGRPC(t *testing.T) {
	binary := os.Getenv("RSX_XRAY_26327_BIN")
	if binary == "" {
		t.Skip("set RSX_XRAY_26327_BIN to the official Xray v26.3.27 binary")
	}
	version, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "Xray 26.3.27") {
		t.Fatalf("test must use official Xray 26.3.27: %v %s", err, version)
	}
	allocPort := func() int {
		t.Helper()
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		return port
	}
	apiPort := allocPort()
	targetPort := allocPort()
	if targetPort == apiPort {
		targetPort = allocPort()
	}
	raw := fmt.Sprintf(`{
		"api": {"tag":"api","listen":"127.0.0.1:%d","services":["StatsService","HandlerService","LoggerService"]},
		"inbounds":[{"tag":"xray-target","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"udp":true}}],
		"outbounds":[{"tag":"direct","protocol":"freedom","settings":{}}],
		"policy":{"system":{"statsInboundDownlink":true,"statsInboundUplink":true}},
		"stats":{},
		"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},{"type":"field","inboundTag":["xray-target"],"outboundTag":"direct"}]}
	}`, apiPort, targetPort)
	cfg := apiConfigForTest(t, raw)
	if err := cfg.ConfigureStatsAPI(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "xray-26327.json")
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	testCommand := exec.Command(binary, "run", "-test", "-config", configPath)
	testResult, testErr := testCommand.CombinedOutput()
	if testErr != nil {
		t.Fatalf("official Xray 26.3.27 rejected simplified API config: %v\n%s", testErr, testResult)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "-config", configPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	proxy := newProcess(cfg)
	if err := proxy.refreshAPIPort(); err != nil {
		t.Fatal(err)
	}
	if proxy.apiPort != apiPort {
		t.Fatalf("StatsService port discovery=%d, want=%d", proxy.apiPort, apiPort)
	}
	var lastErr error
	for ctx.Err() == nil {
		_, lastErr = proxy.GetTraffic(false)
		if lastErr == nil {
			return // QueryStats RPC succeeded on the official Xray 26.3.27 core
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("Xray v26.3.27 QueryStats gRPC never became healthy: %v\n%s", lastErr, output.String())
}
