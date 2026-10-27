package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestNativeHysteria2FormUsesExistingInboundModal(t *testing.T) {
	raw, err := htmlFS.ReadFile("html/xui/form/protocol/hysteria.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("hysteria").Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, "form/hysteria", nil); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"inbound.settings.clients[0].auth",
		"inbound.stream.hysteria.brutalUp", "inbound.stream.hysteria.brutalDown",
		"regenerateHysteriaAuth", "force-brutal"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("Hysteria2 form missing %q", value)
		}
	}
	modal, err := htmlFS.ReadFile("html/xui/form/inbound.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(modal), `{{template "form/hysteria"}}`) ||
		!strings.Contains(string(modal), "Protocols.HYSTERIA") {
		t.Fatal("Hysteria2 form is not integrated into the existing inbound modal")
	}
}
