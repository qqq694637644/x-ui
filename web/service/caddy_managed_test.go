package service

import (
	"strings"
	"testing"
)

func TestRenderManagedCaddyStrictXHTTPAndFixedPath(t *testing.T) {
	block, err := RenderManagedCaddy(
		"asdasdasdas.shop",
		443,
		"/etc/caddy/wildcard.crt",
		"/etc/caddy/wildcard.key",
		[]ManagedRoute{{
			Host:         "abc.asdasdasdas.shop",
			Path:         "/q8Fa72Lm9x",
			UpstreamHost: "127.0.0.1",
			UpstreamPort: 26417,
		}},
	)
	if err != nil {
		t.Fatalf("RenderManagedCaddy() error = %v", err)
	}
	for _, expected := range []string{
		"*.asdasdasdas.shop {",
		"tls /etc/caddy/wildcard.crt /etc/caddy/wildcard.key",
		"host abc.asdasdasdas.shop",
		"path /q8Fa72Lm9x*",
		"reverse_proxy h2c://127.0.0.1:26417",
		"path /q8Fa72Lm9x/__xui_health",
	} {
		if !strings.Contains(block, expected) {
			t.Fatalf("managed block missing %q:\n%s", expected, block)
		}
	}
}

func TestRenderManagedCaddyRequiresWildcardTLS(t *testing.T) {
	_, err := RenderManagedCaddy("asdasdasdas.shop", 443, "", "", []ManagedRoute{{
		Host: "abc.asdasdasdas.shop", Path: "/x", UpstreamHost: "127.0.0.1", UpstreamPort: 26417,
	}})
	if err == nil {
		t.Fatal("managed route unexpectedly rendered without wildcard TLS certificate")
	}
}

func TestRenderManagedCaddyLongestPathFirst(t *testing.T) {
	block, err := RenderManagedCaddy("asdasdasdas.shop", 443, "/c", "/k", []ManagedRoute{
		{Host: "abc.asdasdasdas.shop", Path: "/portal", UpstreamHost: "127.0.0.1", UpstreamPort: 26417},
		{Host: "abc.asdasdasdas.shop", Path: "/portal-long", UpstreamHost: "127.0.0.1", UpstreamPort: 26418},
	})
	if err != nil {
		t.Fatal(err)
	}
	longIndex := strings.Index(block, "path /portal-long*")
	shortIndex := strings.Index(block, "path /portal*")
	if longIndex < 0 || shortIndex < 0 || longIndex > shortIndex {
		t.Fatalf("longer path must be rendered before shorter prefix:\n%s", block)
	}
}

func TestReplaceOwnedManagedCaddyDestructivelyTakesOverZone(t *testing.T) {
	original := `other.example.net {
    respond "keep" 200
}

cdn.asdasdasdas.shop {
    respond "legacy-a" 200
}

aaasdfsd.asdasdasdas.shop:443 {
    respond "legacy-b" 200
}

*.asdasdasdas.shop {
    respond "legacy-wildcard" 200
}
`
	block := managedCaddyBegin + "\n*.asdasdasdas.shop {\n    respond \"managed\" 200\n}\n" + managedCaddyEnd
	got, err := ReplaceOwnedManagedCaddy(original, "asdasdasdas.shop", block)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `respond "keep" 200`) {
		t.Fatalf("unrelated Caddy site was removed:\n%s", got)
	}
	for _, removed := range []string{"legacy-a", "legacy-b", "legacy-wildcard"} {
		if strings.Contains(got, removed) {
			t.Fatalf("legacy owned site %q survived destructive takeover:\n%s", removed, got)
		}
	}
	if strings.Count(got, managedCaddyBegin) != 1 || !strings.Contains(got, `respond "managed" 200`) {
		t.Fatalf("managed site missing or duplicated:\n%s", got)
	}
}

func TestReplaceOwnedManagedCaddyRejectsMixedSiteHeader(t *testing.T) {
	original := "cdn.asdasdasdas.shop, other.example.net {\n    respond \"mixed\" 200\n}\n"
	block := managedCaddyBegin + "\n# replacement\n" + managedCaddyEnd
	if _, err := ReplaceOwnedManagedCaddy(original, "asdasdasdas.shop", block); err == nil {
		t.Fatal("mixed managed/unmanaged site header unexpectedly accepted")
	}
}

func TestRenderManagedCaddyRejectsNonLocalUpstream(t *testing.T) {
	_, err := RenderManagedCaddy("asdasdasdas.shop", 443, "/c", "/k", []ManagedRoute{{
		Host: "abc.asdasdasdas.shop", Path: "/x", UpstreamHost: "0.0.0.0", UpstreamPort: 26417,
	}})
	if err == nil {
		t.Fatal("non-local upstream unexpectedly accepted")
	}
}

func TestRenderManagedCaddyRejectsNestedOrForeignHost(t *testing.T) {
	for _, host := range []string{"nested.a.asdasdasdas.shop", "outside.example.net"} {
		_, err := RenderManagedCaddy("asdasdasdas.shop", 443, "/c", "/k", []ManagedRoute{{
			Host: host, Path: "/x", UpstreamHost: "127.0.0.1", UpstreamPort: 26417,
		}})
		if err == nil {
			t.Fatalf("host %s unexpectedly accepted", host)
		}
	}
}

func TestRenderManagedCaddyAllowsEmptyRoutesWithoutDomain(t *testing.T) {
	block, err := RenderManagedCaddy("", 443, "", "", nil)
	if err != nil {
		t.Fatalf("empty managed block returned error: %v", err)
	}
	if !strings.Contains(block, "# no managed endpoints") {
		t.Fatalf("unexpected empty block: %s", block)
	}
}
