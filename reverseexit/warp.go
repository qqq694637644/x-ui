package reverseexit

// WARP is an already-running local SOCKS5 server, never a fallback route.
// Target TCP connections and UDP datagrams MUST be delivered to it.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

func warpHandshake(ctx context.Context, cfg Config, command byte, dest []byte) (*net.TCPConn, string, int, error) {
	dialer := net.Dialer{Timeout: 8 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", cfg.WARPAddr)
	if err != nil {
		return nil, "", 0, fmt.Errorf("WARP SOCKS5 unreachable: %w", err)
	}
	c := upstream.(*net.TCPConn)
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	method := byte(0)
	if cfg.WARPUsername != "" {
		method = 2
	}
	if err := writeAll(c, []byte{5, 1, method}); err != nil {
		return nil, "", 0, err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(c, response); err != nil {
		return nil, "", 0, err
	}
	if response[0] != 5 || response[1] != method {
		return nil, "", 0, errors.New("WARP SOCKS5 rejected required authentication method")
	}
	if method == 2 {
		u, p := []byte(cfg.WARPUsername), []byte(cfg.WARPPassword)
		if err := writeAll(c, append(append([]byte{1, byte(len(u))}, u...), append([]byte{byte(len(p))}, p...)...)); err != nil {
			return nil, "", 0, err
		}
		if _, err := io.ReadFull(c, response); err != nil {
			return nil, "", 0, err
		}
		if response[0] != 1 || response[1] != 0 {
			return nil, "", 0, errors.New("WARP SOCKS5 authentication failed")
		}
	}
	if err := writeAll(c, append([]byte{5, command, 0}, dest...)); err != nil {
		return nil, "", 0, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(c, header); err != nil {
		return nil, "", 0, err
	}
	if header[0] != 5 || header[2] != 0 || header[1] != 0 {
		return nil, "", 0, fmt.Errorf("WARP SOCKS5 command %d failed (code %d)", command, header[1])
	}
	var raw []byte
	switch header[3] {
	case 1:
		raw = make([]byte, 4+2)
	case 4:
		raw = make([]byte, 16+2)
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(c, length); err != nil {
			return nil, "", 0, err
		}
		raw = append(length, make([]byte, int(length[0])+2)...)
	default:
		return nil, "", 0, errors.New("WARP SOCKS5 invalid BND address type")
	}
	if header[3] == 3 {
		if _, err := io.ReadFull(c, raw[1:]); err != nil {
			return nil, "", 0, err
		}
	} else if _, err := io.ReadFull(c, raw); err != nil {
		return nil, "", 0, err
	}
	host, port, used, err := decodeAddress(append([]byte{header[3]}, raw...))
	if err != nil || used != len(raw)+1 {
		return nil, "", 0, errors.New("WARP SOCKS5 invalid BND response")
	}
	_ = c.SetDeadline(time.Time{})
	ok = true
	return c, host, port, nil
}

func warpTCP(ctx context.Context, cfg Config, host string, port int) (*net.TCPConn, error) {
	addr, err := encodeTarget(host, port)
	if err != nil {
		return nil, err
	}
	c, _, _, err := warpHandshake(ctx, cfg, 1, addr)
	return c, err
}

// probeWARP validates that a real outbound TCP CONNECT can be established
// through WARP, and that an independent UDP ASSOCIATE is accepted. This is
// stronger than testing whether the local SOCKS5 TCP port merely listens,
// but it does not certify a particular remote public egress IP.
func probeWARP(ctx context.Context, cfg Config) error {
	host, portRaw, _ := net.SplitHostPort(cfg.WARPProbe) // cfg.Validate
	port, _ := strconv.Atoi(portRaw)
	c, err := warpTCP(ctx, cfg, host, port)
	if err != nil {
		return fmt.Errorf("WARP TCP CONNECT probe: %w", err)
	}
	_ = c.Close()
	control, udp, _, err := warpUDP(ctx, cfg)
	if err != nil {
		return fmt.Errorf("WARP UDP ASSOCIATE probe: %w", err)
	}
	_ = control.Close()
	_ = udp.Close()
	return nil
}

// warpUDP holds its SOCKS5 TCP control connection open. The only UDP socket it
// creates is bound to loopback and sends exclusively to WARP's loopback relay.
func warpUDP(ctx context.Context, cfg Config) (*net.TCPConn, *net.UDPConn, *net.UDPAddr, error) {
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, nil, nil, err
	}
	bound := u.LocalAddr().(*net.UDPAddr)
	request, _ := encodeAddress(bound.IP, bound.Port)
	c, host, port, err := warpHandshake(ctx, cfg, 3, request)
	if err != nil {
		u.Close()
		return nil, nil, nil, err
	}
	// A non-local BND address could send native UDP from the VPS real IP.
	// Refuse it, rather than bypassing the locally running WARP SOCKS5.
	if host == "0.0.0.0" || host == "::" {
		host, _, _ = net.SplitHostPort(cfg.WARPAddr)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || ip.To4() == nil || port == 0 {
		c.Close()
		u.Close()
		return nil, nil, nil, errors.New("WARP UDP relay must report an IPv4 loopback address")
	}
	return c, u, &net.UDPAddr{IP: ip.To4(), Port: port}, nil
}

func encodeTarget(host string, port int) ([]byte, error) {
	if port < 0 || port > 65535 || host == "" {
		return nil, errors.New("invalid target")
	}
	var result []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			result = append([]byte{1}, v4...)
		} else {
			result = append([]byte{4}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("target domain too long")
		}
		result = append(append([]byte{3, byte(len(host))}, host...), 0, 0)
		binary.BigEndian.PutUint16(result[len(result)-2:], uint16(port))
		return result, nil
	}
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(port))
	return append(result, b[:]...), nil
}

func formatTarget(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
