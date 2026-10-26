package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

func apiConfigForTest(t *testing.T, raw string) *Config {
	t.Helper()
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestXray26327NativeStatsAPIProvisionedWithoutLegacyInbound(t *testing.T) {
	cfg := apiConfigForTest(t, `{
		"api": {"tag":"api","services":["HandlerService","LoggerService","StatsService"]},
		"routing":{"rules":[{"type":"field","outboundTag":"warp","domain":["domain:example.com"]}]},
		"outbounds":[{"protocol":"freedom"},{"tag":"warp","protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":40000}]}}],
		"inbounds":[{"tag":"xray-target","listen":"127.0.0.1","port":40001,"protocol":"socks","settings":{"udp":true}}]
	}`)
	if err := cfg.ConfigureStatsAPI(); err != nil {
		t.Fatal(err)
	}
	port, err := cfg.StatsAPIPort()
	if err != nil || port != 62789 {
		t.Fatalf("StatsAPIPort = %d, %v", port, err)
	}
	if len(cfg.InboundConfigs) != 1 || cfg.InboundConfigs[0].Tag != "xray-target" {
		t.Fatal("Xray api.listen must not synthesize a dokodemo inbound or modify SOCKS5 target")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &generated); err != nil {
		t.Fatal(err)
	}
	var api map[string]interface{}
	if err := json.Unmarshal(generated["api"], &api); err != nil {
		t.Fatal(err)
	}
	if api["listen"] != "127.0.0.1:62789" {
		t.Fatalf("api.listen missing: %v", api)
	}
	if strings.Contains(string(generated["routing"]), "62789") ||
		!strings.Contains(string(generated["routing"]), "domain:example.com") ||
		!strings.Contains(string(generated["outbounds"]), "40000") {
		t.Fatal("Xray API provisioning modified routing or target WARP egress")
	}
	if err := cfg.ConfigureStatsAPI(); err != nil {
		t.Fatal("provisioning must be deterministic: ", err)
	}
}

func TestXray26327StatsAPIAllowsConfiguredLoopbackPort(t *testing.T) {
	cfg := apiConfigForTest(t, `{"api":{"tag":"api","listen":"127.0.0.1:18235","services":["StatsService"]}}`)
	if err := cfg.ConfigureStatsAPI(); err != nil {
		t.Fatal(err)
	}
	port, err := cfg.StatsAPIPort()
	if err != nil || port != 18235 {
		t.Fatalf("custom StatsService port = %d, %v", port, err)
	}
	if !strings.Contains(string(cfg.API), "18235") {
		t.Fatalf("user-selected local endpoint overwritten: %s", cfg.API)
	}
}

func TestXray26327StatsAPIRejectsUnsafeAndOldModes(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"no api", `{}`},
		{"missing tag", `{"api":{"services":["StatsService"]}}`},
		{"wrong tag", `{"api":{"tag":"metrics","services":["StatsService"]}}`},
		{"missing stats service", `{"api":{"tag":"api","services":["LoggerService"]}}`},
		{"public listen", `{"api":{"tag":"api","listen":"0.0.0.0:62789","services":["StatsService"]}}`},
		{"external listen", `{"api":{"tag":"api","listen":"8.8.8.8:62789","services":["StatsService"]}}`},
		{"localhost hostname", `{"api":{"tag":"api","listen":"localhost:62789","services":["StatsService"]}}`},
		{"ipv6", `{"api":{"tag":"api","listen":"[::1]:62789","services":["StatsService"]}}`},
		{"port zero", `{"api":{"tag":"api","listen":"127.0.0.1:0","services":["StatsService"]}}`},
		{"invalid port", `{"api":{"tag":"api","listen":"127.0.0.1:65536","services":["StatsService"]}}`},
		{"legacy inbound", `{"api":{"tag":"api","services":["StatsService"]},"inbounds":[{"listen":"127.0.0.1","port":62789,"protocol":"dokodemo-door","tag":"api"}]}`},
		{"port collision", `{"api":{"tag":"api","services":["StatsService"]},"inbounds":[{"listen":"127.0.0.1","port":62789,"protocol":"socks","tag":"target"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := apiConfigForTest(t, tt.json)
			if err := cfg.ConfigureStatsAPI(); err == nil {
				t.Fatalf("invalid API mode unexpectedly accepted: %s", tt.json)
			}
		})
	}
}

func TestTrafficRegexDoesNotPanicOnUnrelatedXrayStats(t *testing.T) {
	for _, name := range []string{
		"outbound>>>warp>>>traffic>>>uplink", "inbound>>>vless>>>traffic>>>downlink",
		"stats>>>not-traffic", "inbound>>>invalid>>>traffic>>>something",
	} {
		match := trafficRegex.FindStringSubmatch(name)
		if strings.Contains(name, "not-traffic") || strings.HasSuffix(name, "something") {
			if match != nil {
				t.Fatalf("unexpected traffic classification for %q", name)
			}
		} else if len(match) != 4 {
			t.Fatalf("valid Xray 26.3.27 stats not recognized: %q", name)
		}
	}
}
