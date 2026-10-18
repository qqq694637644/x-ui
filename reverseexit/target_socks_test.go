package reverseexit

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type mockLocalSocks struct {
	address     string
	requireAuth bool
	tcpReqs     atomic.Int32
	udpReqs     atomic.Int32
	targets     chan string
	udpDest     chan string
}

func startLocalSocks(t *testing.T, auth ...bool) *mockLocalSocks {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = udp.Close()
	})
	p := &mockLocalSocks{address: ln.Addr().String(),
		targets: make(chan string, 16), udpDest: make(chan string, 16)}
	if len(auth) > 0 {
		p.requireAuth = auth[0]
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, client, err := udp.ReadFromUDP(buf)
			if err != nil {
				return
			}
			packet := append([]byte(nil), buf[:n]...)
			if len(packet) < 4 || !bytes.Equal(packet[:3], []byte{0, 0, 0}) {
				continue
			}
			host, _, _, err := decodeUDP(packet)
			if err != nil {
				continue
			}
			select {
			case p.udpDest <- host:
			default:
			}
			_, _ = udp.WriteToUDP(packet, client)
		}
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(8 * time.Second))
				var hello [3]byte
				if _, err := io.ReadFull(c, hello[:]); err != nil {
					return
				}
				method := byte(0)
				if p.requireAuth {
					method = 2
				}
				if hello != [3]byte{5, 1, method} {
					return
				}
				if _, err := c.Write([]byte{5, method}); err != nil {
					return
				}
				if p.requireAuth {
					var userHeader [2]byte
					if _, err := io.ReadFull(c, userHeader[:]); err != nil || userHeader[0] != 1 {
						return
					}
					user := make([]byte, userHeader[1])
					if _, err := io.ReadFull(c, user); err != nil {
						return
					}
					var passwordLength [1]byte
					if _, err := io.ReadFull(c, passwordLength[:]); err != nil {
						return
					}
					password := make([]byte, passwordLength[0])
					if _, err := io.ReadFull(c, password); err != nil {
						return
					}
					if string(user) != "xray-user" || string(password) != "xray-password" {
						_, _ = c.Write([]byte{1, 1})
						return
					}
					if _, err := c.Write([]byte{1, 0}); err != nil {
						return
					}
				}
				var hdr [4]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
					return
				}
				var n int
				switch hdr[3] {
				case 1:
					n = 6
				case 4:
					n = 18
				case 3:
					var size [1]byte
					if _, err := io.ReadFull(c, size[:]); err != nil {
						return
					}
					n = int(size[0]) + 2
					raw := make([]byte, n)
					if _, err := io.ReadFull(c, raw); err != nil {
						return
					}
					host, _, _, err := decodeAddress(append([]byte{3, size[0]}, raw...))
					if err != nil {
						return
					}
					select {
					case p.targets <- host:
					default:
					}
					n = 0
				default:
					return
				}
				if n > 0 {
					raw := make([]byte, n)
					if _, err := io.ReadFull(c, raw); err != nil {
						return
					}
				}
				switch hdr[1] {
				case 1:
					p.tcpReqs.Add(1)
				case 3:
					p.udpReqs.Add(1)
				default:
					return
				}
				resp := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(resp[8:10], uint16(udp.LocalAddr().(*net.UDPAddr).Port))
				if _, err := c.Write(resp); err != nil {
					return
				}
				if hdr[1] == 1 {
					_ = c.SetDeadline(time.Time{})
					_, _ = io.Copy(c, c)
				} else {
					_, _ = io.Copy(io.Discard, c)
				}
			}(conn)
		}
	}()
	return p
}

func testTargetTCP(t *testing.T, cfg Config, want *mockLocalSocks) {
	t.Helper()
	c, err := targetTCP(context.Background(), cfg, "deepseek.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("hello target")); err != nil {
		t.Fatal(err)
	}
	var reply [12]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || string(reply[:]) != "hello target" {
		t.Fatalf("TCP SOCKS5 echo failed: %v %q", err, reply)
	}
	select {
	case domain := <-want.targets:
		if domain != "deepseek.com" {
			t.Fatalf("target domain was resolved or rewritten: %q", domain)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected SOCKS5 CONNECT missing")
	}
}

func testTargetUDP(t *testing.T, cfg Config, want *mockLocalSocks) {
	t.Helper()
	control, udp, relay, err := targetUDP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	defer udp.Close()
	destination, err := encodeTarget("gemini.google.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	packet := append(append([]byte{0, 0, 0}, destination...), []byte("udp payload")...)
	if _, err := udp.WriteToUDP(packet, relay); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, sender, err := udp.ReadFromUDP(buf)
	if err != nil || !bytes.Equal(packet, buf[:n]) || !sender.IP.IsLoopback() {
		t.Fatalf("UDP SOCKS5 relay did not return requested datagram: %v", err)
	}
	select {
	case domain := <-want.udpDest:
		if domain != "gemini.google.com" {
			t.Fatalf("UDP target was resolved or rewritten: %q", domain)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected UDP SOCKS5 relay missing")
	}
}

func TestTargetSOCKSSeparatedFromWARPTransportAndLegacyFallback(t *testing.T) {
	warp := startLocalSocks(t)
	xray := startLocalSocks(t)
	cfg := DefaultConfig()
	cfg.WARPAddr = warp.address
	cfg.Token = "target-socks-session-token"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	testTargetTCP(t, cfg, warp)
	testTargetUDP(t, cfg, warp)
	if warp.tcpReqs.Load() != 1 || warp.udpReqs.Load() != 1 {
		t.Fatalf("legacy all-WARP case did not dial WARP proxy: TCP=%d UDP=%d",
			warp.tcpReqs.Load(), warp.udpReqs.Load())
	}

	cfg.TargetSOCKSAddr = xray.address
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	testTargetTCP(t, cfg, xray)
	testTargetUDP(t, cfg, xray)
	if xray.tcpReqs.Load() != 1 || xray.udpReqs.Load() != 1 {
		t.Fatalf("configured Xray target did not get TCP+UDP: TCP=%d UDP=%d",
			xray.tcpReqs.Load(), xray.udpReqs.Load())
	}
	if warp.tcpReqs.Load() != 1 || warp.udpReqs.Load() != 1 {
		t.Fatal("target TCP/UDP unexpectedly leaked into the WARP transport proxy")
	}
	// STUN/frpc WARP health still uses WARP even when final targets use Xray.
	if err := probeWARP(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if warp.tcpReqs.Load() != 2 || warp.udpReqs.Load() != 2 {
		t.Fatal("WARP health probe was redirected into Xray")
	}
}

func TestConfiguredTargetFailureNeverFallsBackToWARP(t *testing.T) {
	warp := startLocalSocks(t)
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	missing := closed.Addr().String()
	_ = closed.Close()
	cfg := DefaultConfig()
	cfg.Token = "target-socks-session-token"
	cfg.WARPAddr = warp.address
	cfg.TargetSOCKSAddr = missing
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if conn, err := targetTCP(context.Background(), cfg, "deepseek.com", 443); err == nil {
		_ = conn.Close()
		t.Fatal("configured Xray CONNECT silently fell back")
	}
	if control, sock, _, err := targetUDP(context.Background(), cfg); err == nil {
		_ = control.Close()
		_ = sock.Close()
		t.Fatal("configured Xray UDP silently fell back")
	}
	if warp.tcpReqs.Load() != 0 || warp.udpReqs.Load() != 0 {
		t.Fatal("WARP proxy used as an implicit fallback from broken Xray")
	}
	if err := probeWARP(context.Background(), cfg); err != nil {
		t.Fatalf("WARP transport health was incorrectly tied to Xray: %v", err)
	}
}

func TestTargetSOCKSRejectsNonLocalAndMismatchedCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WARPAddr = "127.0.0.1:40000"
	cfg.Token = "target-socks-session-token"
	for _, bad := range []string{
		"0.0.0.0:40001", "localhost:40001", "8.8.8.8:40001",
		"192.0.2.1:40001", "[::1]:40001", "127.0.0.1:0",
	} {
		wrong := cfg
		wrong.TargetSOCKSAddr = bad
		if err := wrong.Validate(); err == nil {
			t.Fatalf("target proxy allowed nonliteral IPv4 loopback: %q", bad)
		}
	}
	wrong := cfg
	wrong.TargetSOCKSUsername = "user"
	if err := wrong.Validate(); err == nil {
		t.Fatal("accepted target SOCKS5 credentials without target")
	}
	wrong.TargetSOCKSAddr = "127.0.0.1:40001"
	if err := wrong.Validate(); err == nil {
		t.Fatal("accepted target SOCKS5 username without password")
	}
	wrong.TargetSOCKSPassword = "password"
	if err := wrong.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTargetSOCKSIndependentXrayAuthentication(t *testing.T) {
	warp := startLocalSocks(t)
	xray := startLocalSocks(t, true)
	cfg := DefaultConfig()
	cfg.WARPAddr = warp.address
	cfg.Token = "target-socks-session-token"
	cfg.TargetSOCKSAddr = xray.address
	cfg.TargetSOCKSUsername = "xray-user"
	cfg.TargetSOCKSPassword = "xray-password"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	testTargetTCP(t, cfg, xray)
	testTargetUDP(t, cfg, xray)
	if warp.tcpReqs.Load() != 0 || warp.udpReqs.Load() != 0 {
		t.Fatal("target SOCKS5 authentication unexpectedly accessed WARP transport")
	}
	cfg.TargetSOCKSPassword = "incorrect-password"
	if c, err := targetTCP(context.Background(), cfg, "deepseek.com", 443); err == nil {
		_ = c.Close()
		t.Fatal("Xray accepted incorrect SOCKS5 password")
	}
	if warp.tcpReqs.Load() != 0 {
		t.Fatal("Xray authentication failure triggered a WARP fallback")
	}
}
