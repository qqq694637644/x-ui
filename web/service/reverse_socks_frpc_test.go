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

	"github.com/BurntSushi/toml"

	"x-ui/reverseexit"
)

func TestManagedFRPCUserAndVisitorNameRender(t *testing.T) {
	cfg := defaultReverseFRPCConfig()
	cfg.FRPCBinaryPath = buildMockFRPC(t)
	cfg.FRPCConfigPath = filepath.Join(t.TempDir(), "frpc.toml")
	cfg.FRPSServerAddr = "203.0.113.7"
	cfg.FRPCUser = "0123456789abcdef"
	cfg.FRPSToken = "private-frps-token"
	cfg.XTCPVisitorName = "overseas-xtcp-visitor"
	cfg.XTCPServerName = "mainland-proxy"
	cfg.XTCPSecretKey = "private-xtcp-secret"
	cfg.STUNServer = "stun.miwifi.com:3478"
	agent := reverseexit.DefaultConfig()
	agent.WARPAddr = "127.0.0.1:40000"
	agent.Token = "private-rsx2-token-more-than-16"

	assertConfigured := func(wantUser, wantVisitor string) {
		t.Helper()
		content, err := renderManagedFRPC(cfg, agent)
		if err != nil {
			t.Fatal(err)
		}
		if userAt, visitorsAt := strings.Index(content, "user = "), strings.Index(content, "[[visitors]]"); userAt < 0 || visitorsAt < 0 || userAt > visitorsAt {
			t.Fatalf("frpc user must be at TOML root, before [[visitors]]: %s", content)
		}
		var decoded struct {
			User     string `toml:"user"`
			Visitors []struct {
				Name       string `toml:"name"`
				ServerName string `toml:"serverName"`
			} `toml:"visitors"`
		}
		// x-ui's existing BurntSushi/toml v0.3 parser does not understand
		// dotted auth.method / transport.proxyURL keys. Decode only the
		// identity-relevant TOML subset here to check the real root/visitor
		// scoping; the private frpc's 'verify -c' below validates the FULL
		// generated config using frpc's current TOML parser.
		var identityLines []string
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "user = ") || line == "[[visitors]]" ||
				strings.HasPrefix(line, "name = ") || strings.HasPrefix(line, "serverName = ") {
				identityLines = append(identityLines, line)
			}
		}
		if _, err := toml.Decode(strings.Join(identityLines, "\n"), &decoded); err != nil {
			t.Fatalf("generated frpc user/visitor TOML has invalid structure: %v", err)
		}
		if decoded.User != wantUser || len(decoded.Visitors) != 1 ||
			decoded.Visitors[0].Name != wantVisitor ||
			decoded.Visitors[0].ServerName != "mainland-proxy" {
			t.Fatalf("incorrect top-level user, visitor name or mainland proxy name: %+v", decoded)
		}
		stage, err := stageFRPCConfig(context.Background(), cfg, content)
		if err != nil {
			t.Fatalf("custom frpc rejected user/visitor settings: %v", err)
		}
		if err := activateFRPCConfig(cfg, stage); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(cfg.FRPCConfigPath)
		if err != nil || string(actual) != content {
			t.Fatalf("managed file does not match the verified user/visitor TOML: %v", err)
		}
	}

	assertConfigured("0123456789abcdef", "overseas-xtcp-visitor")
	// A repeated Save or a later x-ui restart must not silently reinstate
	// the hardcoded rsx2_outside Visitor name.
	cfg.FRPCUser = "new-user"
	cfg.XTCPVisitorName = "visitor-renamed"
	assertConfigured("new-user", "visitor-renamed")
	for _, bad := range []struct{ user, visitor string }{
		{"has spaces", "visitor"},
		{"has\nnewline", "visitor"},
		{"valid-user", ""},
		{"valid-user", "name with spaces"},
		{"valid-user", "name\nnewline"},
	} {
		cfg.FRPCUser, cfg.XTCPVisitorName = bad.user, bad.visitor
		if _, err := renderManagedFRPC(cfg, agent); err == nil {
			t.Fatalf("accepted invalid user/visitor: user=%q visitor=%q", bad.user, bad.visitor)
		}
	}
}

func TestReverseSocksSavedSettingsPreserveManagedFRPCFields(t *testing.T) {
	original := reverseSocksSaved{
		Config: reverseexit.DefaultConfig(),
		reverseFRPCConfig: reverseFRPCConfig{
			FRPCBinaryPath:  defaultFRPCBinary,
			FRPCConfigPath:  defaultFRPCConfig,
			FRPSServerAddr:  "203.0.113.7",
			FRPSServerPort:  7000,
			FRPCUser:        "user-from-frps",
			FRPSToken:       "frps-secret",
			XTCPVisitorName: "custom-visitor",
			XTCPServerName:  "rsx2_node",
			XTCPSecretKey:   "xtcp-secret",
			STUNServer:      "1.1.1.1:3478",
		},
		Enabled: true,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"frpcBinaryPath", "frpcConfigPath", "frpsServerAddr", "frpcUser", "xtcpVisitorName", "frpsToken", "xtcpSecretKey"} {
		if !strings.Contains(string(data), "\""+key+"\"") {
			t.Fatalf("stored SQLite JSON is missing key %s: %s", key, data)
		}
	}
	var decoded reverseSocksSaved
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FRPCBinaryPath != defaultFRPCBinary || decoded.FRPCConfigPath != defaultFRPCConfig ||
		decoded.FRPCUser != "user-from-frps" || decoded.XTCPVisitorName != "custom-visitor" ||
		decoded.FRPSToken != "frps-secret" || decoded.XTCPSecretKey != "xtcp-secret" || !decoded.Enabled {
		t.Fatalf("managed frpc settings did not survive JSON roundtrip: %+v", decoded.reverseFRPCConfig)
	}
}

func TestReverseSocksFormUsesCurrentSTUNAndClearsSecrets(t *testing.T) {
	stored := reverseSocksSaved{
		Config: reverseexit.DefaultConfig(),
		reverseFRPCConfig: reverseFRPCConfig{
			STUNServer:      "stun.miwifi.com:3478",
			FRPCUser:        "original-user",
			XTCPVisitorName: "original-visitor",
			FRPSToken:       "old-frps-token",
			XTCPSecretKey:   "old-xtcp-key",
		},
	}
	stored.Token = "old-rsx2-token"
	stored.WARPPassword = "old-warp-password"
	updated := configFromReverseSocksForm(stored, ReverseSocksForm{
		STUNServer:      "74.125.250.129:19302",
		FRPCUser:        "new-user",
		XTCPVisitorName: "new-visitor",
	})
	if updated.STUNServer != "74.125.250.129:19302" {
		t.Fatalf("current form STUN ignored: %q", updated.STUNServer)
	}
	if updated.FRPCUser != "new-user" || updated.XTCPVisitorName != "new-visitor" {
		t.Fatalf("current form user / Visitor name ignored: %+v", updated.reverseFRPCConfig)
	}
	if updated.WARPProbe != "1.1.1.1:443" {
		t.Fatalf("empty optional WARP probe removed its default: %q", updated.WARPProbe)
	}
	if updated.Token != "" || updated.FRPSToken != "" || updated.XTCPSecretKey != "" || updated.WARPPassword != "" {
		t.Fatal("the single-user editor must clear secrets explicitly set to empty")
	}
	if stored.STUNServer != "stun.miwifi.com:3478" {
		t.Fatal("applying a form unexpectedly mutated the stored values")
	}
	if stored.FRPCUser != "original-user" || stored.XTCPVisitorName != "original-visitor" {
		t.Fatalf("applying a form mutated saved frpc user/Visitor fields: %+v", stored.reverseFRPCConfig)
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
	cfg.FRPCUser = "0123456789abcdef"
	cfg.FRPSToken = "private-frps-token"
	cfg.XTCPVisitorName = "overseas-xtcp-visitor"
	cfg.XTCPSecretKey = "private-xtcp-secret"
	cfg.STUNServer = "1.1.1.1:3478"
	agent := reverseexit.DefaultConfig()
	agent.WARPAddr = "127.0.0.1:40000"
	agent.Token = "private-long-shared-rsx2-token"
	content, err := renderManagedFRPC(cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "user = \"0123456789abcdef\"") ||
		!strings.Contains(content, "name = \"overseas-xtcp-visitor\"") {
		t.Fatalf("real custom frpc test config lost user or visitor name: %s", content)
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
