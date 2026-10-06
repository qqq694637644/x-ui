package service

import (
	"testing"
	"x-ui/database/model"
)

func TestPortalUDPConflictsWithBusinessUDPInBothDirections(t *testing.T) {
	portal := listenerEndpoint{Address: "0.0.0.0", Port: 40000, Protocols: listenerUDP}
	businessUDP := listenerEndpoint{Address: "127.0.0.1", Port: 40000, Protocols: listenerUDP}
	businessTCP := listenerEndpoint{Address: "127.0.0.1", Port: 40000, Protocols: listenerTCP}

	if !endpointsConflict(portal, businessUDP) || !endpointsConflict(businessUDP, portal) {
		t.Fatal("Portal UDP and business UDP must conflict in both directions")
	}
	if endpointsConflict(portal, businessTCP) || endpointsConflict(businessTCP, portal) {
		t.Fatal("Portal UDP and business TCP may share the same numeric port")
	}
}

func TestPortalTunnelOwnEndpointsDetectUDPConflict(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:         1,
		Mode:       TunnelModePortal,
		Listen:     "127.0.0.1",
		ListenPort: 40000,
		RemotePort: 40000,
		Network:    "tcp,udp",
	}
	endpoints := tunnelListenerEndpoints(tunnel)
	if len(endpoints) != 2 || !endpointsConflict(endpoints[0], endpoints[1]) {
		t.Fatal("a Portal tunnel must reject its own business UDP and mKCP UDP on the same port")
	}

	tunnel.Network = "tcp"
	endpoints = tunnelListenerEndpoints(tunnel)
	if endpointsConflict(endpoints[0], endpoints[1]) {
		t.Fatal("business TCP and Portal UDP may share the same numeric port")
	}
}

func TestPortalXHTTPEndpointIsLocalTCP(t *testing.T) {
	tunnel := &model.Tunnel{
		Id:               2,
		Mode:             TunnelModePortal,
		Listen:           "0.0.0.0",
		ListenPort:       18081,
		Network:          "tcp",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		RemoteAddress:    "cdn.example.com",
		RemotePort:       443,
	}
	endpoints := tunnelListenerEndpoints(tunnel)
	if len(endpoints) != 2 {
		t.Fatalf("endpoints = %#v", endpoints)
	}
	portal := endpoints[1]
	if portal.Address != "127.0.0.1" || portal.Port != 26418 || portal.Protocols != listenerTCP {
		t.Fatalf("Portal XHTTP endpoint = %#v, want local TCP 127.0.0.1:26418", portal)
	}
}

func TestDokodemoInboundUsesSettingsNetworkForConflictDetection(t *testing.T) {
	inbound := &model.Inbound{
		Id:             3,
		Listen:         "0.0.0.0",
		Port:           40000,
		Protocol:       model.Dokodemo,
		Settings:       `{"address":"127.0.0.1","port":53,"network":"tcp,udp"}`,
		StreamSettings: `{}`,
		Tag:            "inbound-40000",
	}
	endpoint, err := inboundListenerEndpoint(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Protocols != listenerTCP|listenerUDP {
		t.Fatalf("protocols = %v, want TCP/UDP", endpoint.Protocols)
	}
}

func TestMkcpInboundIsDetectedAsUDP(t *testing.T) {
	inbound := &model.Inbound{
		Port:           40000,
		Protocol:       model.VMess,
		Settings:       `{}`,
		StreamSettings: `{"network":"mkcp"}`,
	}
	endpoint, err := inboundListenerEndpoint(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Protocols != listenerUDP {
		t.Fatalf("protocols = %v, want UDP", endpoint.Protocols)
	}
}

func TestXHTTPH3InboundIsDetectedAsUDP(t *testing.T) {
	for name, streamSettings := range map[string]string{
		"array alpn":  `{"network":"xhttp","security":"tls","tlsSettings":{"alpn":["h3"]},"xhttpSettings":{"path":"/xhttp","mode":"auto"}}`,
		"string alpn": `{"network":"xhttp","security":"tls","tlsSettings":{"alpn":"h3"},"xhttpSettings":{"path":"/xhttp","mode":"auto"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			inbound := &model.Inbound{
				Port:           40000,
				Protocol:       model.VLESS,
				Settings:       `{}`,
				StreamSettings: streamSettings,
			}
			endpoint, err := inboundListenerEndpoint(inbound)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.Protocols != listenerUDP {
				t.Fatalf("protocols = %v, want UDP for XHTTP H3", endpoint.Protocols)
			}
		})
	}
}

func TestXHTTPH1AndH2InboundsAreDetectedAsTCP(t *testing.T) {
	for _, alpn := range []string{"http/1.1", "h2"} {
		t.Run(alpn, func(t *testing.T) {
			inbound := &model.Inbound{
				Port:           40000,
				Protocol:       model.VLESS,
				Settings:       `{}`,
				StreamSettings: `{"network":"xhttp","security":"tls","tlsSettings":{"alpn":["` + alpn + `"]},"xhttpSettings":{"path":"/xhttp","mode":"auto"}}`,
			}
			endpoint, err := inboundListenerEndpoint(inbound)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.Protocols != listenerTCP {
				t.Fatalf("protocols = %v, want TCP for XHTTP %s", endpoint.Protocols, alpn)
			}
		})
	}
}

func TestXHTTPH2AndH3MayShareSameNumericPort(t *testing.T) {
	h2 := listenerEndpoint{Address: "0.0.0.0", Port: 40000, Protocols: listenerTCP}
	h3 := listenerEndpoint{Address: "0.0.0.0", Port: 40000, Protocols: listenerUDP}
	if endpointsConflict(h2, h3) || endpointsConflict(h3, h2) {
		t.Fatal("XHTTP H2 TCP and H3 UDP may share the same numeric port")
	}
}

func TestValidateInboundTransportLimitsXHTTPToVLESS(t *testing.T) {
	streamSettings := `{"network":"xhttp","xhttpSettings":{"path":"/xhttp","mode":"auto"}}`
	if err := validateInboundTransport(&model.Inbound{Protocol: model.VLESS, StreamSettings: streamSettings}); err != nil {
		t.Fatalf("VLESS XHTTP rejected: %v", err)
	}
	if err := validateInboundTransport(&model.Inbound{Protocol: model.VMess, StreamSettings: streamSettings}); err == nil {
		t.Fatal("VMess XHTTP must be rejected by the panel")
	}
}

func TestShadowsocksSettingsNetworkConflictsWithPortalUDPBothDirections(t *testing.T) {
	inbound := &model.Inbound{
		Id:             9,
		Listen:         "0.0.0.0",
		Port:           40000,
		Protocol:       model.Shadowsocks,
		Settings:       `{"method":"aes-128-gcm","password":"secret","network":"tcp,udp"}`,
		StreamSettings: `{}`,
		Tag:            "shadowsocks-40000",
	}
	shadowsocks, err := inboundListenerEndpoint(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if shadowsocks.Protocols != listenerTCP|listenerUDP {
		t.Fatalf("Shadowsocks protocols = %v, want TCP/UDP", shadowsocks.Protocols)
	}
	portal := listenerEndpoint{
		Address:     "0.0.0.0",
		Port:        40000,
		Protocols:   listenerUDP,
		Description: "Portal",
	}
	if !endpointsConflict(shadowsocks, portal) {
		t.Fatal("existing Shadowsocks TCP/UDP must block a new Portal UDP listener")
	}
	if !endpointsConflict(portal, shadowsocks) {
		t.Fatal("existing Portal UDP must block a new Shadowsocks TCP/UDP listener")
	}
}
