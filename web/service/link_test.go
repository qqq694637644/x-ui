package service

import (
	"net/url"
	"strings"
	"testing"

	"x-ui/database/model"
)

func TestGenerateVLESSXHTTPLinkUsesPublicHostAndFixedInboundPath(t *testing.T) {
	inbound := &model.Inbound{
		Remark:         "primary",
		Protocol:       model.VLESS,
		Settings:       `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","flow":""}],"decryption":"none"}`,
		StreamSettings: `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/q8Fa72Lm9x","host":"","mode":"auto"}}`,
	}
	endpoint := &model.PublicEndpoint{Host: "d8k2m9xq.asdasdasdas.shop", Port: 443, Security: "tls"}
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
	if got := query.Get("type"); got != "xhttp" {
		t.Fatalf("type = %q, want xhttp", got)
	}
	if got := query.Get("path"); got != "/q8Fa72Lm9x" {
		t.Fatalf("path = %q, want fixed inbound path", got)
	}
	if got := query.Get("mode"); got != "auto" {
		t.Fatalf("mode = %q, want auto", got)
	}
	if got := query.Get("host"); got != endpoint.Host {
		t.Fatalf("host = %q, want public endpoint host %q", got, endpoint.Host)
	}
	if got := query.Get("security"); got != "tls" {
		t.Fatalf("security = %q, want tls", got)
	}
	if got := query.Get("sni"); got != endpoint.Host {
		t.Fatalf("sni = %q, want public endpoint host %q", got, endpoint.Host)
	}
}

func TestGenerateVLESSXHTTPLinkDoesNotInventRandomPath(t *testing.T) {
	inbound := &model.Inbound{
		Protocol:       model.VLESS,
		Settings:       `{"clients":[{"id":"11111111-1111-1111-1111-111111111111"}],"decryption":"none"}`,
		StreamSettings: `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/stable-path","host":"","mode":"auto"}}`,
	}
	endpoint := &model.PublicEndpoint{Host: "r1.example.com", Port: 443, Security: "tls"}
	link, err := (&LinkService{}).GenerateInboundLink(inbound, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "path=%2Fstable-path") {
		t.Fatalf("link does not preserve fixed path: %s", link)
	}
}
