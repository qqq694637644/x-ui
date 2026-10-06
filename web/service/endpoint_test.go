package service

import (
	"path/filepath"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
)

func TestManagedRoutesCarryPortalAcrossEndpointHostGroup(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "managed-routes.db")); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{
		UserId:         1,
		Enable:         true,
		Protocol:       model.VLESS,
		Listen:         "127.0.0.1",
		Port:           26417,
		Settings:       `{"clients":[{"id":"11111111-1111-1111-1111-111111111111"}],"decryption":"none"}`,
		StreamSettings: `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/business","host":"","mode":"auto"}}`,
		Tag:            "portal-managed-route",
		Sniffing:       `{}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []*model.PublicEndpoint{
		{InboundId: inbound.Id, Host: "old.example.com", Port: 443, Security: "tls", Status: model.EndpointStatusDraining, CreatedAt: 1},
		{InboundId: inbound.Id, Host: "new.example.com", Port: 443, Security: "tls", Status: model.EndpointStatusActive, CreatedAt: 2},
	} {
		if err := database.GetDB().Create(endpoint).Error; err != nil {
			t.Fatal(err)
		}
	}
	portal := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		RemoteAddress:    "new.example.com",
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "11111111-1111-1111-1111-111111111111",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	if err := database.GetDB().Create(portal).Error; err != nil {
		t.Fatal(err)
	}

	routes, err := (&EndpointService{}).managedRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatalf("managed route count = %d, want 4 (business + portal for old and new host)", len(routes))
	}
	seen := map[string]bool{}
	for _, route := range routes {
		seen[route.Host+"|"+route.Path] = true
	}
	for _, key := range []string{
		"old.example.com|/business",
		"old.example.com|/portal-fixed",
		"new.example.com|/business",
		"new.example.com|/portal-fixed",
	} {
		if !seen[key] {
			t.Fatalf("missing managed route %s: %#v", key, routes)
		}
	}
}
