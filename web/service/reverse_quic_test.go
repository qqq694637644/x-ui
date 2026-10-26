package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/json_util"
	"x-ui/xray"
)

func testReverseQUICBridgeConfig() ReverseQUICBridgeConfig {
	return ReverseQUICBridgeConfig{
		Enabled: true, Domain: "reverse-node.internal",
		PublicHost: "home.example.test", PublicPort: 18443,
		ServerName:     "home.example.test",
		Auth:           "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CertificatePin: strings.Repeat("a", 64),
		BrutalUp:       "24mbps", BrutalDown: "16mbps",
		TransportProxyTag: "warp",
		EgressMode:        "socks5", SOCKS5Address: "127.0.0.1:40001",
		SOCKS5Username: "username", SOCKS5Password: "password",
	}
}

func testReverseQUICBase() *xray.Config {
	return &xray.Config{
		Reverse:         json_util.RawMessage("{\"portals\":[{\"tag\":\"old\",\"domain\":\"old.internal\"}]}"),
		OutboundConfigs: json_util.RawMessage("[{\"tag\":\"warp\",\"protocol\":\"socks\",\"settings\":{\"servers\":[{\"address\":\"127.0.0.1\",\"port\":40000}]}},{\"tag\":\"direct\",\"protocol\":\"freedom\",\"settings\":{}}]"),
		RouterConfig:    json_util.RawMessage("{\"rules\":[{\"type\":\"field\",\"domain\":[\"domain:example.test\"],\"outboundTag\":\"warp\"},{\"type\":\"field\",\"network\":\"tcp,udp\",\"outboundTag\":\"direct\"}]}"),
	}
}

func parseObject(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func parseOutbounds(t *testing.T, raw []byte) []interface{} {
	t.Helper()
	var result []interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func findOutbound(t *testing.T, entries []interface{}, tag string) map[string]interface{} {
	t.Helper()
	for _, value := range entries {
		item := value.(map[string]interface{})
		if item["tag"] == tag {
			return item
		}
	}
	t.Fatalf("missing outbound tag %s", tag)
	return nil
}

func TestReverseQUICBridgeAppendsReverseAndForcesSelectedSOCKS5Exit(t *testing.T) {
	cfg := testReverseQUICBridgeConfig()
	x := testReverseQUICBase()
	if err := applyReverseQUICBridge(x, cfg); err != nil {
		t.Fatal(err)
	}
	reverse := parseObject(t, x.Reverse)
	if len(reverse["portals"].([]interface{})) != 1 {
		t.Fatal("existing reverse.portals lost")
	}
	bridge := reverse["bridges"].([]interface{})[0].(map[string]interface{})
	if bridge["tag"] != reverseQUICBridgeTag || bridge["domain"] != cfg.Domain {
		t.Fatal("VPS must create Bridge, not Portal")
	}
	outbounds := parseOutbounds(t, x.OutboundConfigs)
	if len(outbounds) != 4 {
		t.Fatalf("old outbounds were lost: %v", outbounds)
	}
	_ = findOutbound(t, outbounds, "warp")
	hy := findOutbound(t, outbounds, reverseQUICOutboundTag)
	if hy["protocol"] != "hysteria" {
		t.Fatal("reverse connection is not Hysteria2")
	}
	settings := hy["settings"].(map[string]interface{})
	if settings["address"] != cfg.PublicHost || settings["port"] != float64(cfg.PublicPort) {
		t.Fatal("VPS must actively dial domestic DDNS")
	}
	stream := hy["streamSettings"].(map[string]interface{})
	tls := stream["tlsSettings"].(map[string]interface{})
	if tls["pinnedPeerCertSha256"] != cfg.CertificatePin ||
		tls["serverName"] != cfg.PublicHost {
		t.Fatal("TLS certificate verification or pin missing")
	}
	quic := stream["finalmask"].(map[string]interface{})["quicParams"].(map[string]interface{})
	if quic["congestion"] != "force-brutal" || quic["brutalUp"] != cfg.BrutalUp || quic["brutalDown"] != cfg.BrutalDown {
		t.Fatal("Brutal must be forced, without fallback")
	}
	if stream["sockopt"].(map[string]interface{})["domainStrategy"] != "UseIPv6" {
		t.Fatal("domestic DDNS should use IPv6")
	}
	if stream["sockopt"].(map[string]interface{})["dialerProxy"] != cfg.TransportProxyTag {
		t.Fatal("Hysteria2 QUIC transport must be chained through the existing WARP SOCKS5 outbound")
	}
	egress := findOutbound(t, outbounds, reverseQUICEgressTag)
	if egress["protocol"] != "socks" {
		t.Fatal("SOCKS5 must be true VPS egress")
	}
	server := egress["settings"].(map[string]interface{})["servers"].([]interface{})[0].(map[string]interface{})
	if server["address"] != "127.0.0.1" || server["port"] != float64(40001) {
		t.Fatal("SOCKS5 server address mismatch")
	}
	user := server["users"].([]interface{})[0].(map[string]interface{})
	if user["user"] != "username" || user["pass"] != "password" {
		t.Fatal("SOCKS5 authentication lost")
	}
	rules := parseObject(t, x.RouterConfig)["rules"].([]interface{})
	if len(rules) != 4 || rules[2].(map[string]interface{})["outboundTag"] != "warp" {
		t.Fatal("existing routing rules lost")
	}
	if rules[0].(map[string]interface{})["outboundTag"] != reverseQUICOutboundTag {
		t.Fatal("QUIC reverse registration must precede generic routes")
	}
	if rules[1].(map[string]interface{})["outboundTag"] != reverseQUICEgressTag ||
		rules[1].(map[string]interface{})["inboundTag"].([]interface{})[0] != reverseQUICBridgeTag {
		t.Fatal("all reverse traffic must use configured egress before generic rules")
	}
}

func TestReverseQUICDirectExitAndDisabledNoOp(t *testing.T) {
	cfg := testReverseQUICBridgeConfig()
	cfg.EgressMode = "direct"
	cfg.SOCKS5Username = ""
	cfg.SOCKS5Password = ""
	x := testReverseQUICBase()
	if err := applyReverseQUICBridge(x, cfg); err != nil {
		t.Fatal(err)
	}
	if findOutbound(t, parseOutbounds(t, x.OutboundConfigs), reverseQUICEgressTag)["protocol"] != "freedom" {
		t.Fatal("direct must use Xray freedom outbound")
	}
	x = testReverseQUICBase()
	before, _ := json.Marshal(x)
	cfg.Enabled = false
	if err := applyReverseQUICBridge(x, cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(x)
	if string(before) != string(after) {
		t.Fatal("disabled reverse must not touch existing config")
	}
}

func TestReverseQUICRejectsInvalidConfigAndCollisions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ReverseQUICBridgeConfig)
	}{
		{"short auth", func(c *ReverseQUICBridgeConfig) { c.Auth = "weak" }},
		{"wrong port", func(c *ReverseQUICBridgeConfig) { c.PublicPort = 0 }},
		{"ipv4 host", func(c *ReverseQUICBridgeConfig) { c.PublicHost = "127.0.0.1" }},
		{"bad pin", func(c *ReverseQUICBridgeConfig) { c.CertificatePin = "bad" }},
		{"bad brutal", func(c *ReverseQUICBridgeConfig) { c.BrutalUp = "bbr" }},
		{"missing transport tag", func(c *ReverseQUICBridgeConfig) { c.TransportProxyTag = " " }},
		{"not socks", func(c *ReverseQUICBridgeConfig) { c.EgressMode = "warp-fallback" }},
		{"nonlocal socks", func(c *ReverseQUICBridgeConfig) { c.SOCKS5Address = "8.8.8.8:1080" }},
		{"bad socks port", func(c *ReverseQUICBridgeConfig) { c.SOCKS5Address = "127.0.0.1:70000" }},
		{"no username", func(c *ReverseQUICBridgeConfig) { c.SOCKS5Username = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testReverseQUICBridgeConfig()
			tc.edit(&c)
			if err := validateReverseQUICBridgeConfig(c); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*xray.Config)
	}{
		{"duplicate bridge", func(x *xray.Config) {
			x.Reverse = json_util.RawMessage("{\"bridges\":[{\"tag\":\"reverse-node-bridge\",\"domain\":\"other.internal\"}]}")
		}},
		{"duplicate domain", func(x *xray.Config) {
			x.Reverse = json_util.RawMessage("{\"portals\":[{\"tag\":\"old\",\"domain\":\"reverse-node.internal\"}]}")
		}},
		{"duplicate outbound", func(x *xray.Config) {
			x.OutboundConfigs = json_util.RawMessage("[{\"tag\":\"reverse-node-egress\",\"protocol\":\"freedom\"}]")
		}},
		{"transport tag is not socks", func(x *xray.Config) {
			x.OutboundConfigs = json_util.RawMessage("[{\"tag\":\"warp\",\"protocol\":\"freedom\"}]")
		}},
		{"transport tag missing", func(x *xray.Config) {
			x.OutboundConfigs = json_util.RawMessage("[{\"tag\":\"other\",\"protocol\":\"socks\"}]")
		}},
		{"invalid routing", func(x *xray.Config) { x.RouterConfig = json_util.RawMessage("bad-json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := testReverseQUICBase()
			tc.edit(x)
			if err := applyReverseQUICBridge(x, testReverseQUICBridgeConfig()); err == nil {
				t.Fatal("collision accepted")
			}
		})
	}
}

func TestReverseQUICSaveRollbackPreservesLegacyFRP(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "quic-rollback.db")); err != nil {
		t.Fatal(err)
	}
	legacy := "{\"enabled\":true,\"targetSocksAddr\":\"127.0.0.1:40001\"}"
	if err := database.GetDB().Create(&model.Setting{Key: "reverseSocksConfig", Value: legacy}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &ReverseQUICBridgeService{restartXray: func() error { calls++; return nil }}
	if _, err := s.Save(testReverseQUICBridgeConfig()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("enabled Bridge must restart Xray")
	}
	changed := testReverseQUICBridgeConfig()
	changed.BrutalUp = "88mbps"
	s.restartXray = func() error { return errors.New("injected restart failure") }
	if _, err := s.Save(changed); err == nil {
		t.Fatal("restart failure must reject save")
	}
	stored, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrutalUp != "24mbps" {
		t.Fatal("persisted config not rolled back after Xray failure")
	}
	var old model.Setting
	if err := database.GetDB().Where("key = ?", "reverseSocksConfig").First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.Value != legacy {
		t.Fatal("new Bridge overwrote old FRP / RSX3 config")
	}
}

func TestReverseQUICIntegratedWithProductionXrayConfig(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "quic-integration.db")); err != nil {
		t.Fatal(err)
	}
	template := "{\"api\":{\"tag\":\"api\",\"listen\":\"127.0.0.1:62789\",\"services\":[\"StatsService\"]},\"outbounds\":[{\"tag\":\"warp\",\"protocol\":\"socks\",\"settings\":{\"servers\":[{\"address\":\"127.0.0.1\",\"port\":40000}]}},{\"tag\":\"direct\",\"protocol\":\"freedom\",\"settings\":{}}],\"routing\":{\"rules\":[{\"type\":\"field\",\"domain\":[\"domain:example.test\"],\"outboundTag\":\"warp\"}]}}"
	if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: template}).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(testReverseQUICBridgeConfig())
	if err := database.GetDB().Create(&model.Setting{Key: reverseQUICSettingKey, Value: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg.Reverse), "\"bridges\"") ||
		!strings.Contains(string(cfg.OutboundConfigs), "reverse-node-egress") ||
		!strings.Contains(string(cfg.RouterConfig), "reverse-node-hysteria") {
		t.Fatal("real Xray config omitted new reverse Bridge")
	}
	if !strings.Contains(string(cfg.OutboundConfigs), "\"port\":40000") ||
		!strings.Contains(string(cfg.RouterConfig), "domain:example.test") {
		t.Fatal("real Xray config replaced existing WARP rules")
	}
}
