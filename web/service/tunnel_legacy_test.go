package service

import (
	"path/filepath"
	"testing"
	"x-ui/database"
	"x-ui/database/model"
)

func TestLegacyDirectTunnelShortIDCanBeToggledAndSaved(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "legacy-tunnel.db")); err != nil {
		t.Fatal(err)
	}
	legacy := &model.Tunnel{
		UserId:              1,
		Enable:              true,
		Mode:                "",
		Listen:              "127.0.0.1",
		ListenPort:          18081,
		Network:             "tcp",
		TargetAddress:       "127.0.0.1",
		TargetPort:          18081,
		RemoteAddress:       "198.51.100.20",
		RemotePort:          40000,
		Protocol:            "vmess",
		UUID:                "my-home-xray",
		KcpFinalMaskType:    "none",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   20,
		KcpDownlinkCapacity: 100,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	if err := database.GetDB().Create(legacy).Error; err != nil {
		t.Fatal(err)
	}

	service := &TunnelService{}
	loaded, err := service.GetTunnel(legacy.Id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mode != TunnelModeDirect {
		t.Fatalf("legacy mode = %q, want direct", loaded.Mode)
	}
	if loaded.UUID != "717ca3f3-97cd-589b-b805-3acd24b97366" {
		t.Fatalf("loaded UUID = %q", loaded.UUID)
	}
	loaded.Enable = false
	if err := service.UpdateTunnel(loaded, 1); err != nil {
		t.Fatalf("UpdateTunnel() rejected legacy short ID: %v", err)
	}

	var stored model.Tunnel
	if err := database.GetDB().First(&stored, legacy.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enable {
		t.Fatal("tunnel enable state was not updated")
	}
	if stored.UUID != "717ca3f3-97cd-589b-b805-3acd24b97366" {
		t.Fatalf("stored UUID = %q, want canonical Xray value", stored.UUID)
	}
}
