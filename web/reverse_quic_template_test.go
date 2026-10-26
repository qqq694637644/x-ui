package web

import (
	"strings"
	"testing"
)

func TestReverseQUICHasOwnPageAndLocalSOCKS5ExitControls(t *testing.T) {
	page, err := htmlFS.ReadFile("html/xui/reverse_quic.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, required := range []string{
		"/xui/reverse-quic/config", "/xui/reverse-quic/save", "/xui/reverse-quic/status",
		"refreshStatus", "reverse-quic-card", "QUIC Brutal 反向隧道异常",
		"windowsSpeedCommand", "测速", "windows_reverse_test.ps1",
		"reverse-node.internal", "form.publicHost", "form.publicPort",
		"form.brutalUp", "form.brutalDown",
		"force-brutal", "form.transportProxyTag", "sockopt.dialerProxy",
		"UDP ASSOCIATE", "form.egressMode", "form.socks5Address",
		"form.socks5Username", "form.socks5Password", "importFragment",
		"境外 VPS 主动访问境内", "旧 FRP",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("new reverse QUIC page missing %q", required)
		}
	}
	if strings.Contains(html, "v-model=\"form.protocol\"") ||
		strings.Contains(html, "xtcpVisitor") || strings.Contains(html, "frpsServer") {
		t.Fatal("new QUIC Brutal-only page must not expose FRP or alternate transport")
	}
	sidebar, err := htmlFS.ReadFile("html/xui/common_sider.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidebar), "xui/reverse-quic") ||
		!strings.Contains(string(sidebar), "xui/reverse-socks") {
		t.Fatal("must keep both new reverse QUIC page and old FRP page")
	}
}
