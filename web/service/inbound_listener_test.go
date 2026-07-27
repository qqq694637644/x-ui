package service

import (
	"path/filepath"
	"testing"
	"x-ui/database"
	"x-ui/database/model"
)

func newTestInbound(listen string, port int, network string, protocol model.Protocol) *model.Inbound {
	settings := `{"network":"` + network + `"}`
	if protocol == model.Shadowsocks {
		settings = `{"method":"aes-128-gcm","password":"secret","network":"` + network + `"}`
	}
	return &model.Inbound{
		Listen:         listen,
		Port:           port,
		Protocol:       protocol,
		Settings:       settings,
		StreamSettings: `{}`,
		Sniffing:       `{}`,
	}
}

func TestInboundServiceAllowsNonOverlappingSameNumericPorts(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "inbounds.db")); err != nil {
		t.Fatal(err)
	}
	service := &InboundService{}

	tcp := newTestInbound("0.0.0.0", 19000, "tcp", model.Dokodemo)
	udp := newTestInbound("0.0.0.0", 19000, "udp", model.Dokodemo)
	if err := service.AddInbound(tcp); err != nil {
		t.Fatalf("AddInbound(TCP) error = %v", err)
	}
	if err := service.AddInbound(udp); err != nil {
		t.Fatalf("AddInbound(UDP same numeric port) error = %v", err)
	}
	if tcp.Tag == udp.Tag {
		t.Fatalf("same-port inbounds received duplicate tag %q", tcp.Tag)
	}

	loopback := newTestInbound("127.0.0.1", 19001, "tcp", model.Dokodemo)
	lan := newTestInbound("192.168.1.10", 19001, "tcp", model.Dokodemo)
	if err := service.AddInbound(loopback); err != nil {
		t.Fatalf("AddInbound(loopback) error = %v", err)
	}
	if err := service.AddInbound(lan); err != nil {
		t.Fatalf("AddInbound(non-overlapping IP) error = %v", err)
	}
}

func TestInboundServiceRejectsShadowsocksUDPAgainstPortal(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	portal := &model.Tunnel{
		Mode:       TunnelModePortal,
		Listen:     "127.0.0.1",
		ListenPort: 18081,
		Network:    "tcp",
		RemotePort: 40000,
	}
	if err := db.Create(portal).Error; err != nil {
		t.Fatal(err)
	}

	shadowsocks := newTestInbound("0.0.0.0", 40000, "tcp,udp", model.Shadowsocks)
	if err := (&InboundService{}).AddInbound(shadowsocks); err == nil {
		t.Fatal("Shadowsocks TCP/UDP was allowed to overlap Portal UDP")
	}
}
