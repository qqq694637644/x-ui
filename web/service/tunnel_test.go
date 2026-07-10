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
	if !bytes.Equal(bytes.TrimSpace(parsed.Rules[0]), bytes.TrimSpace(tunnelRule)) {
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
