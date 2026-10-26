package service

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/json_util"
)

func TestReverseQUICOutboundStatsPreserveExistingPolicy(t *testing.T) {
	cfg := testReverseQUICBase()
	cfg.Policy = json_util.RawMessage(`{"levels":{"0":{"handshake":3}},"system":{"statsInboundUplink":true}}`)
	cfg.Stats = json_util.RawMessage(`{"existing":"value"}`)
	if err := applyReverseQUICBridge(cfg, testReverseQUICBridgeConfig()); err != nil {
		t.Fatal(err)
	}
	var policy map[string]interface{}
	if err := json.Unmarshal(cfg.Policy, &policy); err != nil {
		t.Fatal(err)
	}
	if policy["levels"].(map[string]interface{})["0"].(map[string]interface{})["handshake"] != float64(3) {
		t.Fatal("existing traffic policy lost")
	}
	system := policy["system"].(map[string]interface{})
	if system["statsInboundUplink"] != true ||
		system["statsOutboundUplink"] != true ||
		system["statsOutboundDownlink"] != true {
		t.Fatalf("native Xray outbound counters not enabled without preserving existing policy: %#v", system)
	}
	if string(cfg.Stats) != `{"existing":"value"}` {
		t.Fatalf("existing StatsService configuration was overwritten: %s", cfg.Stats)
	}
}

func TestReverseQUICSOCKSLookupRejectsNonLocalAndUnknownTag(t *testing.T) {
	cfg := testReverseQUICBase()
	warp, err := lookupReverseSOCKSOutbound(cfg, "warp")
	if err != nil || warp.Address != "127.0.0.1:40000" {
		t.Fatalf("WARP lookup: %#v, %v", warp, err)
	}
	if _, err := lookupReverseSOCKSOutbound(cfg, "missing"); err == nil {
		t.Fatal("missing SOCKS5 tag must fail")
	}
	cfg.OutboundConfigs = json_util.RawMessage(`[{"tag":"warp","protocol":"socks","settings":{"servers":[{"address":"8.8.8.8","port":1080}]}}]`)
	if _, err := lookupReverseSOCKSOutbound(cfg, "warp"); err == nil {
		t.Fatal("status must never dial an arbitrary remote SOCKS5")
	}
}

func TestReverseQUICSOCKS5UDPAssociateActiveProbe(t *testing.T) {
	for _, usePassword := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-auth", true: "username-password"}[usePassword], func(t *testing.T) {
			server, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			errCh := make(chan error, 1)
			go func() {
				conn, err := server.Accept()
				if err != nil {
					errCh <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				reply := func(packet []byte) error {
					_, err := conn.Write(packet)
					return err
				}
				header, err := socksRead(conn, 2)
				if err != nil {
					errCh <- err
					return
				}
				method, err := socksRead(conn, int(header[1]))
				if err != nil {
					errCh <- err
					return
				}
				expected := byte(0)
				if usePassword {
					expected = 2
				}
				if header[0] != 5 || method[0] != expected {
					errCh <- io.ErrUnexpectedEOF
					return
				}
				if err := reply([]byte{5, expected}); err != nil {
					errCh <- err
					return
				}
				if usePassword {
					h, err := socksRead(conn, 2)
					if err != nil {
						errCh <- err
						return
					}
					name, err := socksRead(conn, int(h[1]))
					if err != nil {
						errCh <- err
						return
					}
					n, err := socksRead(conn, 1)
					if err != nil {
						errCh <- err
						return
					}
					secret, err := socksRead(conn, int(n[0]))
					if err != nil {
						errCh <- err
						return
					}
					if string(name) != "demo" || string(secret) != "demo-pass" {
						errCh <- io.ErrUnexpectedEOF
						return
					}
					if err := reply([]byte{1, 0}); err != nil {
						errCh <- err
						return
					}
				}
				cmd, err := socksRead(conn, 10)
				if err != nil {
					errCh <- err
					return
				}
				if cmd[0] != 5 || cmd[1] != 3 {
					errCh <- io.ErrUnexpectedEOF
					return
				}
				// RFC 1928 UDP ASSOCIATE success; the control connection is
				// kept open until the caller finishes reading the response.
				errCh <- reply([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
			}()
			proxy := reverseQUICSOCKS{Address: server.Addr().String()}
			if usePassword {
				proxy.Username, proxy.Password = "demo", "demo-pass"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := checkSOCKS5(ctx, proxy, true); err != nil {
				t.Fatal(err)
			}
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReverseQUICSOCKS5UDPRejectsUnsupportedMethod(t *testing.T) {
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		conn, err := server.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 3)
		io.ReadFull(conn, buf)
		conn.Write([]byte{5, 0})
		cmd := make([]byte, 10)
		io.ReadFull(conn, cmd)
		conn.Write([]byte{5, 7, 0, 1, 127, 0, 0, 1, 0, 0})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = checkSOCKS5(ctx, reverseQUICSOCKS{Address: server.Addr().String()}, true)
	if err == nil || !strings.Contains(err.Error(), "REP=7") {
		t.Fatalf("SOCKS5 UDP unsupported must be explicit, got %v", err)
	}
}

func TestReverseQUICStatusDisabledIsNotFalseOnline(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "reverse-quic-status-disabled.db")); err != nil {
		t.Fatal(err)
	}
	status, err := (&ReverseQUICBridgeService{}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.State != "disabled" {
		t.Fatalf("disabled bridge must not be presented as online: %#v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "portalOnline") || strings.Contains(string(raw), "registered") {
		t.Fatal("status must never claim registered without native Xray proof")
	}
}

func TestReverseQUICStatusEnabledButXrayStopped(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "reverse-quic-status-down.db")); err != nil {
		t.Fatal(err)
	}
	cfg := testReverseQUICBridgeConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.Setting{Key: reverseQUICSettingKey, Value: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	reverseQUICSidecar.Lock()
	previous := reverseQUICSidecar.process
	reverseQUICSidecar.process = nil
	reverseQUICSidecar.Unlock()
	defer func() {
		reverseQUICSidecar.Lock()
		reverseQUICSidecar.process = previous
		reverseQUICSidecar.Unlock()
	}()
	status, err := (&ReverseQUICBridgeService{}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "down" || status.XrayRunning || !status.Enabled {
		t.Fatalf("stopped Xray must be reported offline: %+v", status)
	}
}
