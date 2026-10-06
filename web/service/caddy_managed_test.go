package service

import (
	"strings"
	"testing"
)

func TestRenderManagedCaddyKeepsFixedPath(t *testing.T) {
	block, probes, err := RenderManagedCaddy(
		"asdasdasdas.shop",
		443,
		"/etc/caddy/wildcard.crt",
		"/etc/caddy/wildcard.key",
		[]ManagedRoute{{
			Host:         "abc.asdasdasdas.shop",
			Path:         "/q8Fa72Lm9x",
			Network:      "xhttp",
			UpstreamHost: "127.0.0.1",
			UpstreamPort: 26417,
		}},
	)
	if err != nil {
		t.Fatalf("RenderManagedCaddy() error = %v", err)
	}
	for _, expected := range []string{
		"*.asdasdasdas.shop {",
		"host abc.asdasdasdas.shop",
		"path /q8Fa72Lm9x*",
		"reverse_proxy h2c://127.0.0.1:26417",
		"path /q8Fa72Lm9x/__xui_health",
	} {
		if !strings.Contains(block, expected) {
			t.Fatalf("managed block missing %q:\n%s", expected, block)
		}
	}
	if len(probes) != 1 || probes[0].Path != "/q8Fa72Lm9x/__xui_health" {
		t.Fatalf("unexpected probes: %#v", probes)
	}
}

func TestReplaceManagedCaddyBlockPreservesManualConfig(t *testing.T) {
	original := "manual.example.com {\n    respond \"manual\" 200\n}\n"
	first := managedCaddyBegin + "\n# first\n" + managedCaddyEnd
	combined, err := ReplaceManagedCaddyBlock(original, first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(combined, `respond "manual" 200`) || !strings.Contains(combined, "# first") {
		t.Fatalf("combined config lost manual or managed content:\n%s", combined)
	}
	second := managedCaddyBegin + "\n# second\n" + managedCaddyEnd
	replaced, err := ReplaceManagedCaddyBlock(combined, second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replaced, `respond "manual" 200`) || !strings.Contains(replaced, "# second") {
		t.Fatalf("replacement lost content:\n%s", replaced)
	}
	if strings.Contains(replaced, "# first") {
		t.Fatalf("old managed block survived replacement:\n%s", replaced)
	}
}

func TestRenderManagedCaddyAllowsEmptyRoutesWithoutDomain(t *testing.T) {
	block, _, err := RenderManagedCaddy("", 443, "", "", nil)
	if err != nil {
		t.Fatalf("empty managed block returned error: %v", err)
	}
	if !strings.Contains(block, "# no managed endpoints") {
		t.Fatalf("unexpected empty block: %s", block)
	}
}
