package service

import (
	"encoding/json"
	"testing"
	"x-ui/database/model"
)

func TestGenXrayOutboundConfigOmitsRemovedMkcpHeaderAndSeed(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:                  1,
		RemoteAddress:       "example.com",
		RemotePort:          443,
		Protocol:            "vless",
		UUID:                "00000000-0000-0000-0000-000000000000",
		KcpHeaderType:       "srtp",
		KcpSeed:             "legacy-seed",
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
	if !ok || len(udp) != 2 {
		t.Fatalf("finalmask.udp = %#v, want two masks", finalmask["udp"])
	}
	headerMask, ok := udp[0].(map[string]interface{})
	if !ok || headerMask["type"] != "header-srtp" {
		t.Fatalf("finalmask.udp[0] = %#v, want header-srtp", udp[0])
	}
	mkcpMask, ok := udp[1].(map[string]interface{})
	if !ok || mkcpMask["type"] != "mkcp-aes128gcm" {
		t.Fatalf("finalmask.udp[1] = %#v, want mkcp-aes128gcm", udp[1])
	}
	settings, ok := mkcpMask["settings"].(map[string]interface{})
	if !ok || settings["password"] != "legacy-seed" {
		t.Fatalf("mkcp-aes128gcm settings = %#v, want password legacy-seed", mkcpMask["settings"])
	}
}
