package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
)

func TestReverseSocksWebConfigSecretsAndLifecycle(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "reverse-socks.db")); err != nil {
		t.Fatal(err)
	}
	service := NewReverseSocksService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	form := ReverseSocksForm{
		Enabled:      false,
		VisitorAddr:  "127.0.0.1:6000",
		UDPBindAddr:  "127.0.0.1:6002",
		UDPPeerAddr:  "127.0.0.1:6001",
		WARPAddr:     "127.0.0.1:40000",
		WARPUsername: "myuser",
		WARPPassword: "super-secret-warp-password",
		Token:        "super-secret-rsx2-token-123",
	}
	view, err := service.Save(form)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasToken || !view.HasWARPPassword || view.Enabled {
		t.Fatalf("unexpected saved view: %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), form.Token) || strings.Contains(string(encoded), form.WARPPassword) {
		t.Fatal("Web JSON exposed an RSX2/WARP secret")
	}
	form.WARPPassword = ""
	form.Token = ""
	form.Enabled = true
	if _, err := service.Save(form); err != nil {
		t.Fatal(err)
	}
	saved, err := service.load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "super-secret-rsx2-token-123" ||
		saved.WARPPassword != "super-secret-warp-password" || !saved.Enabled {
		t.Fatal("update cleared stored secrets or did not enable service")
	}
	view, err = service.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.Status.State != "stopped" {
		t.Fatalf("stop did not stop managed worker: %+v", view.Status)
	}
	form.Enabled = true
	form.WARPAddr = ""
	if _, err := service.Save(form); err == nil {
		t.Fatal("enabled without mandatory WARP endpoint")
	}
	view, err = service.Get()
	if err != nil || view.Enabled {
		t.Fatalf("invalid update damaged saved config: %+v, err=%v", view, err)
	}
	// Configuration survives a simulated panel restart.
	other := NewReverseSocksService()
	if err := other.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer other.Stop()
	view, err = other.Get()
	if err != nil || view.WARPAddr != "127.0.0.1:40000" || !view.HasToken {
		t.Fatalf("restart did not restore saved config: %+v, err=%v", view, err)
	}
}
