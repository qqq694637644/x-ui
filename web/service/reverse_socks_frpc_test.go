package service

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"x-ui/reverseexit"
)

func TestReverseSocksSavedSettingsPreserveManagedFRPCFields(t *testing.T) {
	original := reverseSocksSaved{
		Config: reverseexit.DefaultConfig(),
		reverseFRPCConfig: reverseFRPCConfig{
			FRPCBinaryPath: defaultFRPCBinary,
			FRPCConfigPath: defaultFRPCConfig,
			FRPSServerAddr: "203.0.113.7",
			FRPSServerPort: 7000,
			FRPSToken:      "frps-secret",
			XTCPServerName: "rsx2_node",
			XTCPSecretKey:  "xtcp-secret",
			STUNServer:     "1.1.1.1:3478",
		},
		Enabled: true,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"frpcBinaryPath", "frpcConfigPath", "frpsServerAddr", "frpsToken", "xtcpSecretKey"} {
		if !strings.Contains(string(data), "\""+key+"\"") {
			t.Fatalf("stored SQLite JSON is missing key %s: %s", key, data)
		}
	}
	var decoded reverseSocksSaved
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FRPCBinaryPath != defaultFRPCBinary || decoded.FRPCConfigPath != defaultFRPCConfig ||
		decoded.FRPSToken != "frps-secret" || decoded.XTCPSecretKey != "xtcp-secret" || !decoded.Enabled {
		t.Fatalf("managed frpc settings did not survive JSON roundtrip: %+v", decoded.reverseFRPCConfig)
	}
}

func TestManagedFRPCRealForkPrivacyValidation(t *testing.T) {
	binary := os.Getenv("REVERSE_SOCKS_TEST_REAL_FRPC_BIN")
	if binary == "" {
		t.Skip("set REVERSE_SOCKS_TEST_REAL_FRPC_BIN to test real custom frpc binary")
	}
	cfg := defaultReverseFRPCConfig()
	cfg.FRPCBinaryPath = binary
	cfg.FRPCConfigPath = filepath.Join(t.TempDir(), "managed-frpc.toml")
	cfg.FRPSServerAddr = "203.0.113.7"
	cfg.FRPSToken = "private-frps-token"
	cfg.XTCPSecretKey = "private-xtcp-secret"
	cfg.STUNServer = "1.1.1.1:3478"
	agent := reverseexit.DefaultConfig()
	agent.WARPAddr = "127.0.0.1:40000"
	agent.Token = "private-long-shared-rsx2-token"
	content, err := renderManagedFRPC(cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := stageFRPCConfig(context.Background(), cfg, content)
	if err != nil {
		t.Fatalf("real private frpc did not pass positive AND negative config validation: %v", err)
	}
	defer os.Remove(temp)
	for _, stun := range []string{"stun.miwifi.com:3478", "74.125.250.129:19302"} {
		cfg.STUNServer = stun
		content, err := renderManagedFRPC(cfg, agent)
		if err != nil {
			t.Fatalf("render STUN %q: %v", stun, err)
		}
		temp, err := stageFRPCConfig(context.Background(), cfg, content)
		if err != nil {
			t.Fatalf("real custom frpc rejected valid STUN %q: %v", stun, err)
		}
		_ = os.Remove(temp)
	}
}

func TestManagedFRPCWithoutDatabase(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := occupied.Addr().String()
	if err := ensureVisitorPortAvailable(port); err == nil {
		t.Fatal("allowed launch on an unrelated listener's port")
	}
	_ = occupied.Close()
	if err := ensureVisitorPortAvailable(port); err != nil {
		t.Fatalf("refused a free private XTCP Visitor port: %v", err)
	}
	binary := buildMockFRPC(t)
	cfg := defaultReverseFRPCConfig()
	cfg.FRPCBinaryPath = binary
	cfg.FRPCConfigPath = filepath.Join(t.TempDir(), "managed", "frpc.toml")
	cfg.FRPSServerAddr = "203.0.113.7"
	cfg.FRPSServerPort = 7000
	cfg.FRPSToken = "private-frps-token"
	cfg.XTCPServerName = "rsx2_node"
	cfg.XTCPSecretKey = "private-xtcp-secret"
	cfg.STUNServer = "1.1.1.1:3478"

	agent := reverseexit.DefaultConfig()
	agent.WARPAddr = "127.0.0.1:40000"
	agent.Token = "private-rsx2-token-more-than-16"

	contents, err := renderManagedFRPC(cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"serverAddr = \"203.0.113.7\"",
		"transport.proxyURL = \"socks5://127.0.0.1:40000\"",
		"udpProxyURL = \"socks5://127.0.0.1:40000\"",
		"disableAssistedAddrs = true",
		"minRetryInterval = 10",
		"maxRetriesAnHour = 120",
	} {
		if !strings.Contains(contents, field) {
			t.Fatalf("managed private config missing %q", field)
		}
	}

	temp, err := stageFRPCConfig(context.Background(), cfg, contents)
	if err != nil {
		t.Fatal(err)
	}
	if err := activateFRPCConfig(cfg, temp); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.FRPCConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("frpc config secret file not private: %o", info.Mode().Perm())
	}

	managed := newManagedFRPC(nil)
	defer managed.Stop()
	if err := managed.Start(cfg.FRPCBinaryPath, cfg.FRPCConfigPath); err != nil {
		t.Fatal(err)
	}
	first := managed.Status()
	if first.State != "running" || first.PID == 0 {
		t.Fatalf("expected running owned frpc: %+v", first)
	}
	// "verify" alone is NOT sufficient: old frpc may silently accept an
	// unknown udpProxyURL. This sentinel intentionally accepts bad config
	// and must be rejected by the negative capability probe.
	t.Setenv("MOCK_FRPC_LAX_VERIFY", "1")
	if _, err := stageFRPCConfig(context.Background(), cfg, contents); err == nil ||
		!strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("accepted frpc that ignores privacy options: %v", err)
	}
	t.Setenv("MOCK_FRPC_LAX_VERIFY", "")
	if managed.Status().PID != first.PID {
		t.Fatal("failed validation changed a running process")
	}
	managed.Stop()
	if state := managed.Status(); state.PID != 0 || state.State != "stopped" {
		t.Fatalf("managed process remained after stop: %+v", state)
	}

	// Refuse to overwrite a user's unrelated frpc.toml.
	if err := os.WriteFile(cfg.FRPCConfigPath, []byte("serverAddr=\"unmanaged\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageFRPCConfig(context.Background(), cfg, contents); err == nil {
		t.Fatal("overwrote an unmanaged frpc.toml")
	}

	for _, stun := range []string{"stun.miwifi.com:3478", "74.125.250.129:19302"} {
		cfg.STUNServer = stun
		if rendered, err := renderManagedFRPC(cfg, agent); err != nil ||
			!strings.Contains(rendered, "natHoleSTUNServer = \""+stun+"\"") {
			t.Fatalf("valid STUN domain/IP rejected or not rendered: %q %v", stun, err)
		}
	}
	for _, stun := range []string{
		"", "stun.miwifi.com", "stun.miwifi.com:0", "stun..miwifi.com:3478",
		"-stun.example.com:3478", "stun.example.com:65536", "74.125.250.999:19302",
		"[::1]:3478",
	} {
		cfg.STUNServer = stun
		if _, err := renderManagedFRPC(cfg, agent); err == nil {
			t.Fatalf("invalid STUN server %q accepted", stun)
		}
	}
	agent.WARPAddr = "0.0.0.0:40000"
	cfg.STUNServer = "1.1.1.1:3478"
	if _, err := renderManagedFRPC(cfg, agent); err == nil {
		t.Fatal("allowed non-loopback WARP SOCKS5 endpoint")
	}
}
