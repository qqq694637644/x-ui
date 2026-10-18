package reverseexit

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// DiagnosticDNS sends and verifies a real UDP response through the configured
// local SOCKS5 relay. ASSOCIATE alone is not labelled as UDP reachability.
func DiagnosticDNS(ctx context.Context, cfg Config, useWARP bool) (map[string]interface{}, error) {
	control, sock, relay, err := DiagnosticUDP(ctx, cfg, useWARP)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	defer sock.Close()
	stop := context.AfterFunc(ctx, func() { _ = sock.Close(); _ = control.Close() })
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = sock.SetDeadline(deadline)
	var txn [2]byte
	_, _ = rand.Read(txn[:])
	dns := append(txn[:], []byte{1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}...)
	target, err := encodeTarget("1.1.1.1", 53)
	if err != nil {
		return nil, err
	}
	packet := append(append([]byte{0, 0, 0}, target...), dns...)
	start := time.Now()
	if _, err := sock.WriteToUDP(packet, relay); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, from, err := sock.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 UDP DNS response: %w", err)
		}
		if !from.IP.Equal(relay.IP) || from.Port != relay.Port {
			continue
		}
		host, port, payload, err := decodeUDP(buf[:n])
		if err != nil {
			continue
		}
		if net.ParseIP(host) == nil || port != 53 || len(payload) < 12 || payload[0] != txn[0] || payload[1] != txn[1] || payload[2]&128 == 0 {
			continue
		}
		rcode := payload[3] & 15
		if rcode != 0 {
			return nil, fmt.Errorf("DNS server responded with rcode %d", rcode)
		}
		return map[string]interface{}{"state": "ok", "verified": true, "kind": "SOCKS5 UDP DNS round_trip", "duration_ms": float64(time.Since(start)) / float64(time.Millisecond), "source": host, "answers": binary.BigEndian.Uint16(payload[6:8]), "relay": relay.String()}, nil
	}
}
func DiagnosticConnect(ctx context.Context, cfg Config, target string, useWARP bool) (map[string]interface{}, error) {
	host, portRaw, err := net.SplitHostPort(target)
	if err != nil {
		return nil, errors.New("test target requires host:port")
	}
	port, err := net.LookupPort("tcp", portRaw)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	c, err := DiagnosticTarget(ctx, cfg, host, port, useWARP)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return map[string]interface{}{"state": "ok", "kind": "socks_connect", "duration_ms": float64(time.Since(start)) / float64(time.Millisecond), "target": target, "local": c.LocalAddr().String(), "proxy": c.RemoteAddr().String()}, nil
}
