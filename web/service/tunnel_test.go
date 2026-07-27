package service

import (
	"bytes"
	"encoding/json"
	"testing"
	"x-ui/database/model"
	"x-ui/util/json_util"
)

func TestGenXrayOutboundConfigBuildsFinalMaskFromUIType(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:                  1,
		RemoteAddress:       "example.com",
		RemotePort:          443,
		Protocol:            "vless",
		UUID:                "00000000-0000-0000-0000-000000000000",
		KcpFinalMaskType:    "header-srtp",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpCongestion:       false,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}

	outboundConfig, err := (&TunnelService{}).genXrayOutboundConfig(tunnel)
	if err != nil {
		t.Fatalf("genXrayOutboundConfig() error = %v", err)
	}

	var outbound map[string]interface{}
	if err := json.Unmarshal(outboundConfig, &outbound); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	streamSettings, ok := outbound["streamSettings"].(map[string]interface{})
	if !ok {
		t.Fatalf("streamSettings missing or invalid: %#v", outbound["streamSettings"])
	}
	if got := streamSettings["network"]; got != "mkcp" {
		t.Fatalf("streamSettings.network = %v, want mkcp", got)
	}

	kcpSettings, ok := streamSettings["kcpSettings"].(map[string]interface{})
	if !ok {
		t.Fatalf("kcpSettings missing or invalid: %#v", streamSettings["kcpSettings"])
	}
	if _, ok := kcpSettings["header"]; ok {
		t.Fatalf("kcpSettings must not include removed header field: %#v", kcpSettings["header"])
	}
	if _, ok := kcpSettings["seed"]; ok {
		t.Fatalf("kcpSettings must not include removed seed field: %#v", kcpSettings["seed"])
	}

	finalmask, ok := streamSettings["finalmask"].(map[string]interface{})
	if !ok {
		t.Fatalf("finalmask missing or invalid: %#v", streamSettings["finalmask"])
	}
	udp, ok := finalmask["udp"].([]interface{})
	if !ok || len(udp) != 1 {
		t.Fatalf("finalmask.udp = %#v, want one mask", finalmask["udp"])
	}
	headerMask, ok := udp[0].(map[string]interface{})
	if !ok || headerMask["type"] != "header-srtp" {
		t.Fatalf("finalmask.udp[0] = %#v, want header-srtp", udp[0])
	}
}

func TestGenXrayOutboundConfigOmitsFinalMaskForPlainMkcp(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:                  1,
		RemoteAddress:       "example.com",
		RemotePort:          443,
		Protocol:            "vless",
		UUID:                "00000000-0000-0000-0000-000000000000",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}

	outboundConfig, err := (&TunnelService{}).genXrayOutboundConfig(tunnel)
	if err != nil {
		t.Fatalf("genXrayOutboundConfig() error = %v", err)
	}

	var outbound map[string]interface{}
	if err := json.Unmarshal(outboundConfig, &outbound); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	streamSettings, ok := outbound["streamSettings"].(map[string]interface{})
	if !ok {
		t.Fatalf("streamSettings missing or invalid: %#v", outbound["streamSettings"])
	}
	if _, ok := streamSettings["finalmask"]; ok {
		t.Fatalf("plain mkcp must not include finalmask: %#v", streamSettings["finalmask"])
	}
}

func TestAppendRoutingRulePrependsTunnelRule(t *testing.T) {
	routing := json_util.RawMessage(`{
  "rules": [
    {
      "ip": ["geoip:private"],
      "outboundTag": "blocked",
      "type": "field"
    }
  ]
}`)
	tunnelRule := json.RawMessage(`{
  "inboundTag": ["tunnel-in-1"],
  "outboundTag": "tunnel-out-1",
  "type": "field"
}`)

	if err := appendRoutingRule(&routing, tunnelRule); err != nil {
		t.Fatalf("appendRoutingRule() error = %v", err)
	}

	var parsed struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal([]byte(routing), &parsed); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(parsed.Rules) != 2 {
		t.Fatalf("rules length = %d, want 2", len(parsed.Rules))
	}
	var compactActual bytes.Buffer
	var compactExpected bytes.Buffer
	if err := json.Compact(&compactActual, parsed.Rules[0]); err != nil {
		t.Fatalf("json.Compact(actual rule) error = %v", err)
	}
	if err := json.Compact(&compactExpected, tunnelRule); err != nil {
		t.Fatalf("json.Compact(expected rule) error = %v", err)
	}
	if !bytes.Equal(compactActual.Bytes(), compactExpected.Bytes()) {
		t.Fatalf("first rule = %s, want tunnel rule %s", parsed.Rules[0], tunnelRule)
	}

	var secondRule map[string]interface{}
	if err := json.Unmarshal(parsed.Rules[1], &secondRule); err != nil {
		t.Fatalf("json.Unmarshal(second rule) error = %v", err)
	}
	if secondRule["outboundTag"] != "blocked" {
		t.Fatalf("second rule outboundTag = %v, want blocked", secondRule["outboundTag"])
	}
}

func TestNormalizeTunnelDefaultsLegacyRecordToDirect(t *testing.T) {
	tunnel := &model.Tunnel{}
	(&TunnelService{}).normalizeTunnel(tunnel)
	if tunnel.Mode != TunnelModeDirect {
		t.Fatalf("mode = %q, want %q", tunnel.Mode, TunnelModeDirect)
	}
}

func TestLegacyDirectTunnelShortIDCanStillBeEdited(t *testing.T) {
	tunnel := &model.Tunnel{
		Mode:                TunnelModeDirect,
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemoteAddress:       "198.51.100.20",
		RemotePort:          40000,
		Protocol:            "vmess",
		UUID:                "my-home-xray",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   20,
		KcpDownlinkCapacity: 100,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	service := &TunnelService{}
	service.normalizeTunnel(tunnel)
	if err := service.checkTunnel(tunnel); err != nil {
		t.Fatalf("legacy short ID was rejected: %v", err)
	}
	if got, want := tunnel.UUID, "717ca3f3-97cd-589b-b805-3acd24b97366"; got != want {
		t.Fatalf("normalized UUID = %q, want %q", got, want)
	}
}

func TestPortalConfigUsesVMessMkcpAndReversePortal(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:                  7,
		Mode:                TunnelModePortal,
		Listen:              "0.0.0.0",
		ListenPort:          18081,
		Network:             "tcp,udp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemotePort:          40000,
		Protocol:            "vmess",
		UUID:                "11111111-1111-1111-1111-111111111111",
		KcpFinalMaskType:    "header-srtp",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}

	portalInbound, err := (&TunnelService{}).genXrayPortalInboundConfig(tunnel)
	if err != nil {
		t.Fatalf("genXrayPortalInboundConfig() error = %v", err)
	}
	if portalInbound.Protocol != "vmess" || portalInbound.Port != 40000 {
		t.Fatalf("portal inbound = %#v", portalInbound)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(portalInbound.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	clients := settings["clients"].([]interface{})
	client := clients[0].(map[string]interface{})
	if client["alterId"] != float64(0) {
		t.Fatalf("alterId = %#v", client["alterId"])
	}
	var stream map[string]interface{}
	if err := json.Unmarshal(portalInbound.StreamSettings, &stream); err != nil {
		t.Fatal(err)
	}
	if stream["network"] != "mkcp" {
		t.Fatalf("network = %#v", stream["network"])
	}
	if _, ok := stream["finalmask"]; !ok {
		t.Fatal("finalmask missing")
	}

	reverse := json_util.RawMessage(`{"bridges":[{"tag":"existing","domain":"existing.example"}]}`)
	if err := appendReversePortal(&reverse, tunnel); err != nil {
		t.Fatalf("appendReversePortal() error = %v", err)
	}
	var parsed map[string][]map[string]interface{}
	if err := json.Unmarshal(reverse, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed["portals"]) != 1 || parsed["portals"][0]["tag"] != tunnel.PortalTag() {
		t.Fatalf("portals = %#v", parsed["portals"])
	}
	if len(parsed["bridges"]) != 1 {
		t.Fatalf("existing reverse config was not preserved: %#v", parsed)
	}
}

func TestPortalModeRejectsVless(t *testing.T) {
	tunnel := &model.Tunnel{
		Mode:                TunnelModePortal,
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemotePort:          40000,
		Protocol:            "vless",
		UUID:                "11111111-1111-1111-1111-111111111111",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	if err := (&TunnelService{}).checkTunnel(tunnel); err == nil {
		t.Fatal("portal mode accepted vless")
	}
}
