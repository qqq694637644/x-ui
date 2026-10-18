package reverseexit

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestWARPHealthProbeChecksTCPAndUDPSeparately(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var tcpCount, udpCount atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				greeting := make([]byte, 3)
				if _, e := io.ReadFull(c, greeting); e != nil {
					return
				}
				if greeting[0] != 5 || greeting[2] != 0 {
					return
				}
				if _, e = c.Write([]byte{5, 0}); e != nil {
					return
				}
				head := make([]byte, 4)
				if _, e = io.ReadFull(c, head); e != nil {
					return
				}
				var addrLength int
				switch head[3] {
				case 1:
					addrLength = 6
				case 4:
					addrLength = 18
				case 3:
					length := []byte{0}
					if _, e = io.ReadFull(c, length); e != nil {
						return
					}
					addrLength = int(length[0]) + 2
				default:
					return
				}
				target := make([]byte, addrLength)
				if _, e = io.ReadFull(c, target); e != nil {
					return
				}
				if head[1] == 1 {
					tcpCount.Add(1)
				} else if head[1] == 3 {
					udpCount.Add(1)
				} else {
					return
				}
				response := []byte{5, 0, 0, 1, 127, 0, 0, 1}
				response = binary.BigEndian.AppendUint16(response, 40002)
				if _, e = c.Write(response); e != nil {
					return
				}
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	cfg := DefaultConfig()
	cfg.Token = "probe-test-shared-token-xyz"
	cfg.WARPAddr = ln.Addr().String()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := probeWARP(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if tcpCount.Load() != 1 || udpCount.Load() != 1 {
		t.Fatalf("health probe skipped TCP CONNECT or UDP ASSOCIATE: %d / %d", tcpCount.Load(), udpCount.Load())
	}
	ln.Close()
	<-done
	if err := probeWARP(context.Background(), cfg); err == nil {
		t.Fatal("reported healthy after WARP listener disappeared")
	}
}

func TestManagerTracksXTCPAndWARPIndependently(t *testing.T) {
	manager := NewManager()
	manager.setStatus("online", "")
	status := manager.Status()
	if status.State != "online" || status.WARPState != "unknown" {
		t.Fatalf("status=%+v", status)
	}
	manager.setStatus("warp-ready", "")
	status = manager.Status()
	if status.State != "online" || status.WARPState != "ready" {
		t.Fatalf("status=%+v", status)
	}
	manager.setStatus("warp-error", "WARP SOCKS5 dial failed")
	status = manager.Status()
	if status.State != "online" || status.WARPState != "unavailable" || status.WARPError == "" {
		t.Fatalf("expected XTCP online but WARP unavailable: %+v", status)
	}
	manager.setStatus("error", "XTCP disconnected")
	status = manager.Status()
	if status.State != "error" || status.WARPState != "unknown" {
		t.Fatalf("stale WARP status after XTCP disconnect: %+v", status)
	}
}
