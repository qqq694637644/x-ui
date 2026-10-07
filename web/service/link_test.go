package service

import (
	"net/url"
	"testing"

	"x-ui/database/model"
)

func validManagedInboundForTest(id int, remark string, port int, path string) *model.Inbound {
	return &model.Inbound{
		Id:             id,
		UserId:         1,
		Remark:         remark,
		Enable:         true,
		Listen:         "127.0.0.1",
		Port:           port,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","flow":""}],"decryption":"none"}`,
		StreamSettings: `{"network":"xhttp","security":"none","xhttpSettings":{"path":"` + path + `","host":"","mode":"auto"}}`,
		Tag:            "managed-test",
		Sniffing:       `{"enabled":false}`,
	}
}

func TestGenerateManagedVLESSXHTTPLinkUsesPublicHostAndFixedPath(t *testing.T) {
	inbound := validManagedInboundForTest(1, "primary", 26417, "/q8Fa72Lm9x")
	endpoint := &model.PublicEndpoint{Host: "d8k2m9xq.asdasdasdas.shop", Port: 443}
	link, err := (&LinkService{}).GenerateInboundLink(inbound, endpoint)
	if err != nil {
		t.Fatalf("GenerateInboundLink() error = %v", err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if got := parsed.Hostname(); got != endpoint.Host {
		t.Fatalf("hostname = %q, want %q", got, endpoint.Host)
	}
	if got := parsed.Port(); got != "443" {
		t.Fatalf("port = %q, want 443", got)
	}
	query := parsed.Query()
	checks := map[string]string{
		"type":     "xhttp",
		"path":     "/q8Fa72Lm9x",
		"mode":     "auto",
		"host":     endpoint.Host,
		"security": "tls",
		"sni":      endpoint.Host,
		"alpn":     "http/1.1",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestValidateManagedInboundRejectsUnsupportedShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.Inbound)
	}{
		{name: "vmess", mutate: func(in *model.Inbound) { in.Protocol = model.VMess }},
		{name: "websocket", mutate: func(in *model.Inbound) {
			in.StreamSettings = `{"network":"ws","security":"none","wsSettings":{"path":"/fixed"}}`
		}},
		{name: "internal tls", mutate: func(in *model.Inbound) {
			in.StreamSettings = `{"network":"xhttp","security":"tls","xhttpSettings":{"path":"/fixed","host":"","mode":"auto"}}`
		}},
		{name: "non local listen", mutate: func(in *model.Inbound) { in.Listen = "0.0.0.0" }},
		{name: "internal xhttp host", mutate: func(in *model.Inbound) {
			in.StreamSettings = `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/fixed","host":"internal.example","mode":"auto"}}`
		}},
		{name: "multiple clients", mutate: func(in *model.Inbound) {
			in.Settings = `{"clients":[{"id":"11111111-1111-1111-1111-111111111111"},{"id":"22222222-2222-2222-2222-222222222222"}],"decryption":"none"}`
		}},
		{name: "flow", mutate: func(in *model.Inbound) {
			in.Settings = `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","flow":"xtls-rprx-vision"}],"decryption":"none"}`
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbound := validManagedInboundForTest(1, "strict", 26417, "/fixed")
			tc.mutate(inbound)
			if _, err := validateManagedInbound(inbound); err == nil {
				t.Fatalf("validateManagedInbound() unexpectedly accepted %s", tc.name)
			}
		})
	}
}
