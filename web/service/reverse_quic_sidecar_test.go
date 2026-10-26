package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReverseQUICIsolatedSidecarHasNoOldRoutingOrInbound(t *testing.T) {
	cfg := testReverseQUICBridgeConfig()
	sidecar, err := BuildIsolatedReverseQUICConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sidecar.InboundConfigs) != 0 {
		t.Fatal("isolated Bridge must not reuse main x-ui inbounds")
	}
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(sidecar.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 3 {
		t.Fatalf("sidecar should have exactly 3 outbounds: %v", outbounds)
	}
	for _, item := range outbounds {
		if item["tag"] == "warp" || item["tag"] == "blocked" {
			t.Fatalf("sidecar imported main Xray outbound: %v", item)
		}
	}
	var rules map[string]interface{}
	if err := json.Unmarshal(sidecar.RouterConfig, &rules); err != nil {
		t.Fatal(err)
	}
	routeEntries := rules["rules"].([]interface{})
	if len(routeEntries) != 2 {
		t.Fatalf("only two sidecar routes allowed: %#v", routeEntries)
	}
	if routeEntries[0].(map[string]interface{})["outboundTag"] != reverseQUICOutboundTag ||
		routeEntries[1].(map[string]interface{})["outboundTag"] != reverseQUICEgressTag {
		t.Fatal("sidecar reversed routing is not fail-closed")
	}
	var api map[string]interface{}
	if err := json.Unmarshal(sidecar.API, &api); err != nil {
		t.Fatal(err)
	}
	if api["listen"] != "127.0.0.1:62790" {
		t.Fatalf("sidecar API must be isolated from main Xray: %v", api)
	}
	if !strings.Contains(string(sidecar.LogConfig), "\"loglevel\":\"debug\"") {
		t.Fatal("independent Xray must log Brutal negotiation and reverse errors for diagnosis")
	}
	if !strings.Contains(string(sidecar.OutboundConfigs), "\"port\":40000") {
		t.Fatal("transport SOCKS must use explicitly configured port 40000")
	}
}

func TestReverseQUICIsolatedRejectsUnsafeTransport(t *testing.T) {
	for _, address := range []string{"8.8.8.8:40000", "127.0.0.1:0", "127.0.0.1:65536", "https://127.0.0.1:40000"} {
		cfg := testReverseQUICBridgeConfig()
		cfg.TransportSOCKSAddress = address
		if _, err := BuildIsolatedReverseQUICConfig(cfg); err == nil {
			t.Fatalf("unsafe transport address %q accepted", address)
		}
	}
}

func TestReverseQUICSidecarRedactsSensitiveLogFields(t *testing.T) {
	cfg := testReverseQUICBridgeConfig()
	line := strings.Join([]string{cfg.Auth, cfg.CertificatePin, cfg.SOCKS5Username, cfg.SOCKS5Password, "empty worker list"}, " ")
	redacted := sanitizeReverseQUICLog(line, cfg)
	for _, secret := range []string{cfg.Auth, cfg.CertificatePin, cfg.SOCKS5Username, cfg.SOCKS5Password} {
		if secret != "" && strings.Contains(redacted, secret) {
			t.Fatal("credentials leaked into diagnostic output")
		}
	}
	if !strings.Contains(redacted, "empty worker list") {
		t.Fatal("diagnostic redaction lost the error")
	}
}

func TestReverseQUICSidecarConfigNeverMutatesMainConfig(t *testing.T) {
	main := testReverseQUICBase()
	before, err := json.Marshal(main)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := BuildIsolatedReverseQUICConfig(testReverseQUICBridgeConfig())
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(main)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || sidecar == main {
		t.Fatal("isolated config modified main Xray")
	}
	if strings.Contains(string(main.Reverse), reverseQUICBridgeTag) ||
		strings.Contains(string(main.OutboundConfigs), reverseQUICOutboundTag) {
		t.Fatal("reverse Bridge leaked into main Xray")
	}
}
