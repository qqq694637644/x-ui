package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
)

const hysteriaTestAuth = "0123456789abcdef0123456789abcdef"

func validHysteria2Inbound() *model.Inbound {
	return &model.Inbound{
		Enable:   true,
		Protocol: model.Hysteria,
		Port:     18443,
		Listen:   "::",
		Tag:      "hy2-native-18443",
		Settings: `{"version":2,"clients":[{"auth":"` + hysteriaTestAuth + `","email":"test@example.test"}]}`,
		StreamSettings: `{
			"network":"hysteria","security":"tls",
			"tlsSettings":{"alpn":["h3"],"certificates":[
				{"certificateFile":"/secure/fullchain.pem","keyFile":"/secure/privkey.pem"}]},
			"hysteriaSettings":{"version":2},
			"finalmask":{"quicParams":{"congestion":"force-brutal",
				"brutalUp":"16mbps","brutalDown":"24mbps"}}
		}`,
		Sniffing: `{}`,
	}
}

func TestNativeHysteria2Validation(t *testing.T) {
	valid := validHysteria2Inbound()
	if err := validateInboundTransport(valid); err != nil {
		t.Fatalf("valid Xray-core 26.3.27 native Hysteria2 rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		modify func(*model.Inbound)
	}{
		{"missing auth", func(x *model.Inbound) { x.Settings = `{"version":2,"clients":[{"auth":"weak"}]}` }},
		{"missing clients", func(x *model.Inbound) { x.Settings = `{"version":2,"clients":[]}` }},
		{"invalid version", func(x *model.Inbound) { x.Settings = strings.Replace(x.Settings, `"version":2`, `"version":1`, 1) }},
		{"no stream", func(x *model.Inbound) { x.StreamSettings = "" }},
		{"no TLS", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"security":"tls"`, `"security":"none"`, 1)
		}},
		{"wrong ALPN", func(x *model.Inbound) { x.StreamSettings = strings.Replace(x.StreamSettings, `["h3"]`, `["h2"]`, 1) }},
		{"missing TLS private key", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"keyFile":"/secure/privkey.pem"`, `"keyFile":""`, 1)
		}},
		{"wrong transport", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"network":"hysteria"`, `"network":"tcp"`, 1)
		}},
		{"wrong hysteria version", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"hysteriaSettings":{"version":2}`, `"hysteriaSettings":{"version":1}`, 1)
		}},
		{"duplicate transport auth", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"hysteriaSettings":{"version":2}`, `"hysteriaSettings":{"version":2,"auth":"duplicate-secret"}`, 1)
		}},
		{"brutal fallback BBR", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, "force-brutal", "brutal", 1)
		}},
		{"missing Brutal rate", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"brutalUp":"16mbps"`, `"brutalUp":""`, 1)
		}},
		{"unsafe Brutal rate", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"brutalDown":"24mbps"`, `"brutalDown":"-24mbps"`, 1)
		}},
		{"too high Brutal rate", func(x *model.Inbound) {
			x.StreamSettings = strings.Replace(x.StreamSettings, `"brutalUp":"16mbps"`, `"brutalUp":"9999999999mbps"`, 1)
		}},
		{"wrong app protocol", func(x *model.Inbound) { x.Protocol = model.VMess }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *valid
			test.modify(&copy)
			if err := validateInboundTransport(&copy); err == nil {
				t.Fatal("accepted invalid native Hysteria2 settings")
			}
		})
	}
}

func TestHysteriaUDPListenerDetectionAndPortSharing(t *testing.T) {
	inbound := validHysteria2Inbound()
	hysteria, err := inboundListenerEndpoint(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if hysteria.Protocols != listenerUDP {
		t.Fatalf("Hysteria2 listener classified as %d instead of UDP", hysteria.Protocols)
	}
	tcp := listenerEndpoint{Address: "0.0.0.0", Port: inbound.Port, Protocols: listenerTCP}
	udp := listenerEndpoint{Address: "0.0.0.0", Port: inbound.Port, Protocols: listenerUDP}
	if endpointsConflict(hysteria, tcp) || endpointsConflict(tcp, hysteria) {
		t.Fatal("native Hysteria2 UDP must not conflict with TCP listener")
	}
	if !endpointsConflict(hysteria, udp) || !endpointsConflict(udp, hysteria) {
		t.Fatal("native Hysteria2 UDP must conflict with another UDP listener")
	}
}

func TestNativeHysteriaIsMergedIntoExistingXrayRouting(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "hy2-native.db")); err != nil {
		t.Fatal(err)
	}
	template := `{"api":{"tag":"api","listen":"127.0.0.1:62789","services":["StatsService"]},
	"outbounds":[{"tag":"direct","protocol":"freedom","settings":{}},
	{"tag":"warp","protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":40000}]}}],
	"routing":{"rules":[{"type":"field","domain":["domain:example.test"],"outboundTag":"warp"}]}}`
	if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: template}).Error; err != nil {
		t.Fatal(err)
	}
	inbound := validHysteria2Inbound()
	if err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	var match bool
	for _, entry := range cfg.InboundConfigs {
		if entry.Tag != inbound.Tag {
			continue
		}
		if entry.Protocol != "hysteria" || entry.Port != inbound.Port {
			t.Fatalf("incorrect generated Xray inbound: %+v", entry)
		}
		var stream struct {
			Network   string `json:"network"`
			FinalMask struct {
				QuicParams struct {
					Congestion string `json:"congestion"`
				} `json:"quicParams"`
			} `json:"finalmask"`
		}
		if err := json.Unmarshal(entry.StreamSettings, &stream); err != nil {
			t.Fatal(err)
		}
		if stream.Network != "hysteria" || stream.FinalMask.QuicParams.Congestion != "force-brutal" {
			t.Fatalf("native Hysteria/Brutal lost in assembled Xray config: %+v", stream)
		}
		match = true
	}
	if !match {
		t.Fatal("native Hysteria inbound missing from existing Xray process config")
	}
	if !strings.Contains(string(cfg.OutboundConfigs), "40000") ||
		!strings.Contains(string(cfg.RouterConfig), "domain:example.test") {
		t.Fatal("native Hysteria inbound unexpectedly rewrote the existing WARP vs freedom routing")
	}
}
