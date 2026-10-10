package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/database"
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

func TestNormalizePortalXHTTPRemoteAddressToLowercase(t *testing.T) {
	tunnel := &model.Tunnel{
		Mode:            TunnelModePortal,
		PortalTransport: PortalTransportXHTTP,
		RemoteAddress:   "CDN.ASDASDASDAS.SHOP",
	}
	(&TunnelService{}).normalizeTunnel(tunnel)
	if got, want := tunnel.RemoteAddress, "cdn.asdasdasdas.shop"; got != want {
		t.Fatalf("Portal XHTTP RemoteAddress = %q, want %q", got, want)
	}
}

func TestPortalXHTTPRejectsNonFixedManagedPaths(t *testing.T) {
	paths := []string{"/foo*", "/foo bar", "/foo{", "/__xui_health"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			tunnel := &model.Tunnel{
				Mode:                TunnelModePortal,
				ListenPort:          18081,
				Network:             "tcp",
				TargetAddress:       "127.0.0.1",
				TargetPort:          18081,
				RemoteAddress:       "CDN.ASDASDASDAS.SHOP",
				RemotePort:          443,
				Protocol:            "vless",
				UUID:                "11111111-1111-1111-1111-111111111111",
				PortalTransport:     PortalTransportXHTTP,
				PortalListenPort:    26418,
				XHttpPath:           path,
				KcpFinalMaskType:    "none",
				KcpMtu:              1350,
				KcpTti:              20,
				KcpUplinkCapacity:   5,
				KcpDownlinkCapacity: 20,
				KcpReadBufferSize:   2,
				KcpWriteBufferSize:  2,
			}
			service := &TunnelService{}
			service.normalizeTunnel(tunnel)
			if err := service.checkTunnel(tunnel); err == nil {
				t.Fatalf("Portal XHTTP path %q unexpectedly passed strict managed validation", path)
			}
		})
	}
}

func TestPortalXHTTPRejectsNon443PublicPort(t *testing.T) {
	tunnel := &model.Tunnel{
		Mode:                TunnelModePortal,
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemoteAddress:       "cdn.example.com",
		RemotePort:          8443,
		Protocol:            "vless",
		UUID:                "11111111-1111-1111-1111-111111111111",
		PortalTransport:     PortalTransportXHTTP,
		PortalListenPort:    26418,
		XHttpPath:           "/portal-xhttp",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	service := &TunnelService{}
	service.normalizeTunnel(tunnel)
	if err := service.checkTunnel(tunnel); err == nil || !strings.Contains(err.Error(), "443") {
		t.Fatalf("Portal XHTTP non-443 public port error = %v", err)
	}
}

func TestAddPortalXHTTPRequiresActiveManagedEndpointAfterOwnershipExists(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal-binding.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, _ = createPublishedManagedInboundForTest(t, "portal-binding", "portal-binding", 26417, "/fixed", "cdn.asdasdasdas.shop")

	tunnel := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		Remark:           "typo-portal",
		Listen:           "127.0.0.1",
		ListenPort:       18081,
		Network:          "tcp",
		TargetAddress:    "127.0.0.1",
		TargetPort:       18082,
		RemoteAddress:    "typo.asdasdasdas.shop",
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "88888888-8888-8888-8888-888888888888",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	service := &TunnelService{}
	err := service.AddTunnel(tunnel)
	if err == nil || !strings.Contains(err.Error(), "active PublicEndpoint") {
		t.Fatalf("AddTunnel() unbound managed Portal error = %v", err)
	}
	var count int64
	if err := database.GetDB().Model(&model.Tunnel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unbound Portal persisted despite managed ownership: count=%d", count)
	}
}

func TestAddPortalXHTTPRejectsDrainingEndpointBinding(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal-draining-binding.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "portal-draining-binding", "portal-draining", 26417, "/fixed", "active.asdasdasdas.shop")
	draining := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "draining.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusDraining,
		CreatedAt: time.Now().Unix(),
		RetireAt:  time.Now().Add(time.Minute).Unix(),
	}
	if err := database.GetDB().Create(draining).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		Remark:           "draining-portal",
		Listen:           "127.0.0.1",
		ListenPort:       18081,
		Network:          "tcp",
		TargetAddress:    "127.0.0.1",
		TargetPort:       18082,
		RemoteAddress:    draining.Host,
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "98989898-9898-9898-9898-989898989898",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	err := (&TunnelService{}).AddTunnel(tunnel)
	if err == nil || !strings.Contains(err.Error(), "active PublicEndpoint") {
		t.Fatalf("AddTunnel() draining Portal binding error = %v", err)
	}
	var count int64
	if err := database.GetDB().Model(&model.Tunnel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("Portal bound to draining endpoint was persisted: count=%d", count)
	}
}

func TestUpdatePortalXHTTPRejectsRetargetToDrainingEndpoint(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal-update-draining-binding.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, active := createPublishedManagedInboundForTest(t, "portal-update-draining", "portal-update-draining", 26417, "/fixed", "active.asdasdasdas.shop")
	draining := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "draining.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusDraining,
		CreatedAt: time.Now().Unix(),
		RetireAt:  time.Now().Add(time.Minute).Unix(),
	}
	if err := database.GetDB().Create(draining).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		Remark:           "active-portal",
		Listen:           "127.0.0.1",
		ListenPort:       18081,
		Network:          "tcp",
		TargetAddress:    "127.0.0.1",
		TargetPort:       18082,
		RemoteAddress:    active.Host,
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "97979797-9797-9797-9797-979797979797",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	service := &TunnelService{syncManagedRoutesHook: func() error { return nil }}
	if err := service.AddTunnel(tunnel); err != nil {
		t.Fatalf("AddTunnel() active Portal error = %v", err)
	}
	tunnel.RemoteAddress = draining.Host
	if err := service.UpdateTunnel(tunnel, 1); err == nil || !strings.Contains(err.Error(), "active PublicEndpoint") {
		t.Fatalf("UpdateTunnel() draining retarget error = %v", err)
	}
	stored, err := service.GetTunnel(tunnel.Id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RemoteAddress != active.Host {
		t.Fatalf("Portal RemoteAddress changed to draining host despite rejection: %q", stored.RemoteAddress)
	}
}

func TestAddManagedPortalCaddyFailureRollsBackAndFailsClosed(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal-add-caddy-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, endpoint := createPublishedManagedInboundForTest(t, "portal-add-caddy-fail", "portal-owner", 26417, "/fixed", "portal-owner.asdasdasdas.shop")
	tunnel := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		Remark:           "managed-portal",
		Listen:           "127.0.0.1",
		ListenPort:       18081,
		Network:          "tcp",
		TargetAddress:    "127.0.0.1",
		TargetPort:       18082,
		RemoteAddress:    endpoint.Host,
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "77777777-7777-7777-7777-777777777777",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	service := &TunnelService{syncManagedRoutesHook: func() error {
		return errors.New("injected managed Caddy failure")
	}}
	err := service.AddTunnel(tunnel)
	if err == nil || !strings.Contains(err.Error(), "Caddy") {
		t.Fatalf("AddTunnel() Caddy failure error = %v", err)
	}
	var count int64
	if err := database.GetDB().Model(&model.Tunnel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("managed Portal persisted after failed Caddy sync: count=%d", count)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after Portal Caddy sync failure")
	}
	defer setManagedStateHealthy(true)
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

func TestPortalXHTTPUsesVLESSAndLocalTCPInbound(t *testing.T) {
	tunnel := &model.Tunnel{
		Mode:                TunnelModePortal,
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemoteAddress:       "cdn.example.com",
		RemotePort:          443,
		Protocol:            "vless",
		UUID:                "11111111-1111-1111-1111-111111111111",
		PortalTransport:     PortalTransportXHTTP,
		PortalListenPort:    26418,
		XHttpPath:           "/portal-xhttp",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	service := &TunnelService{}
	if err := service.checkTunnel(tunnel); err != nil {
		t.Fatalf("VLESS/XHTTP portal rejected: %v", err)
	}
	portalInbound, err := service.genXrayPortalInboundConfig(tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if portalInbound.Protocol != "vless" || portalInbound.Port != 26418 || string(portalInbound.Listen) != `"127.0.0.1"` {
		t.Fatalf("portal inbound = %#v", portalInbound)
	}
	var stream map[string]interface{}
	if err := json.Unmarshal(portalInbound.StreamSettings, &stream); err != nil {
		t.Fatal(err)
	}
	if stream["network"] != "xhttp" || stream["security"] != "none" {
		t.Fatalf("streamSettings = %#v", stream)
	}
	xhttp := stream["xhttpSettings"].(map[string]interface{})
	if xhttp["path"] != "/portal-xhttp" || xhttp["mode"] != "auto" {
		t.Fatalf("xhttpSettings = %#v", xhttp)
	}
}

func TestPortalTransportProtocolPairIsValidated(t *testing.T) {
	base := model.Tunnel{
		Mode:                TunnelModePortal,
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemoteAddress:       "cdn.example.com",
		RemotePort:          443,
		PortalListenPort:    26418,
		XHttpPath:           "/portal-xhttp",
		UUID:                "11111111-1111-1111-1111-111111111111",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	service := &TunnelService{}

	xhttpVMess := base
	xhttpVMess.PortalTransport = PortalTransportXHTTP
	xhttpVMess.Protocol = "vmess"
	if err := service.checkTunnel(&xhttpVMess); err == nil {
		t.Fatal("XHTTP portal accepted VMess")
	}

	mkcpVLESS := base
	mkcpVLESS.PortalTransport = PortalTransportMkcp
	mkcpVLESS.Protocol = "vless"
	mkcpVLESS.RemotePort = 40000
	if err := service.checkTunnel(&mkcpVLESS); err == nil {
		t.Fatal("mKCP portal accepted VLESS")
	}
}
