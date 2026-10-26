package service

import (
	"context"
	"path/filepath"
	"testing"

	"x-ui/database"
)

func TestDirectSocksSettingsIsolationAndSecretMasking(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "direct-v6.db")); err != nil {
		t.Fatal(err)
	}
	legacy := NewReverseSocksService()
	if err := legacy.settings.setString(reverseSocksSettingKey, `{"enabled":false,"marker":"keep-legacy"}`); err != nil {
		t.Fatal(err)
	}

	direct := NewDirectSocksService()
	if err := direct.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer direct.Stop()
	input := DirectSocksSettings{Enabled: false, EgressMode: "native",
		Hostname: "rsx3.example.test", Port: 8080, TargetMbps: 16, MaxMbps: 24,
		CertSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Token:      "my-secret-token-with-length"}
	view, err := direct.Save(input)
	if err != nil {
		t.Fatal(err)
	}
	if view.Token != "" || !view.HasToken || view.Enabled || view.EgressMode != "native" {
		t.Fatalf("secret leaked or invalid response: %+v", view)
	}
	raw, err := legacy.settings.getString(reverseSocksSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if raw != `{"enabled":false,"marker":"keep-legacy"}` {
		t.Fatalf("direct config overwrote legacy setting: %s", raw)
	}
	input.Token = ""
	input.Hostname = "another.example.test"
	if _, err := direct.Save(input); err != nil {
		t.Fatal(err)
	}
	saved, err := direct.load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "my-secret-token-with-length" || saved.Hostname != input.Hostname {
		t.Fatalf("blank token did not preserve saved credential")
	}
	invalidRate := input
	invalidRate.Enabled = true
	invalidRate.TargetMbps, invalidRate.MaxMbps = 32, 16
	if _, err := direct.Save(invalidRate); err == nil {
		t.Fatal("accepted invalid bounded Brutal rates")
	}
	bad := input
	bad.EgressMode = "unexpected"
	if _, err := direct.Save(bad); err == nil {
		t.Fatal("accepted invalid egress mode")
	}
	current, err := direct.load()
	if err != nil || current.EgressMode != "native" {
		t.Fatalf("invalid config replaced saved configuration: %+v %v", current, err)
	}
}
