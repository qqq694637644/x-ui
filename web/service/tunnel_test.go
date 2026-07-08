package service

import (
	"encoding/json"
	"testing"
	"x-ui/database/model"
)

func TestGenXrayOutboundConfigBuildsFinalMaskFromUIType(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:                  1,
		RemoteAddress:       "example.com",
		RemotePort:          443,
		Protocol:            "vless",
		UUID:                "00000000-0000-0000-0000-000000000000",
		KcpFinalMaskType:    "srtp",
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
	legacyMask, ok := udp[0].(map[string]interface{})
	if !ok || legacyMask["type"] != "mkcp-legacy" {
		t.Fatalf("finalmask.udp[0] = %#v, want mkcp-legacy", udp[0])
	}
	settings, ok := legacyMask["settings"].(map[string]interface{})
	if !ok || settings["header"] != "srtp" || settings["value"] != "" {
		t.Fatalf("finalmask.udp[0].settings = %#v, want header=srtp value empty", legacyMask["settings"])
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
