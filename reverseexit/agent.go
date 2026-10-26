package reverseexit

// RSX3 overseas Agent: target connections only use a configured
// loopback SOCKS5 proxy (WARP by default, or Xray for domain routing).

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const maxSessions = 1024

type stream interface {
	deliver(frame)
	stop()
}

// directTransport replaces only the FRPC Visitor and localhost datagram hop.
// TCP/UDP session authentication and SOCKS5 relay stay identical to RSX3.
type directTransport interface {
	OpenStream(context.Context) (net.Conn, error)
	SendDatagram([]byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
}

type agent struct {
	conn      net.Conn
	udpConn   *net.UDPConn
	udpPeer   *net.UDPAddr
	direct    directTransport
	directCtx context.Context
	cfg       Config
	sender    *fairWriter
	mu        sync.Mutex
	streams   map[uint32]stream
}

// Run retries exclusively through the existing local XTCP Visitor. Destinations
// are reached through the explicitly selected local SOCKS5 server, never
// through a direct-connect fallback in the Go Agent.
func Run(ctx context.Context, cfg Config, onState func(string, string)) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	status := func(state, message string) {
		if onState != nil {
			onState(state, message)
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for ctx.Err() == nil {
		status("connecting", "")
		c, err := dialer.DialContext(ctx, "tcp", cfg.VisitorAddr)
		if err == nil {
			log.Printf("reverse SOCKS: connected to XTCP visitor %s", cfg.VisitorAddr)
			err = serveRSX3(ctx, c, cfg, status)
		}
		if ctx.Err() != nil {
			break
		}
		status("error", err.Error())
		log.Printf("reverse SOCKS: connection lost: %v; retrying in %s", err, cfg.RetryInterval)
		select {
		case <-ctx.Done():
		case <-time.After(cfg.RetryInterval):
		}
	}
	return ctx.Err()
}

// Session ID (4 BE bytes) + unmodified RFC1928 UDP packet. Neither UDP
// payloads nor their retransmission are ever carried through the TCP mux.
func (a *agent) datagramLoop() {
	if a.direct != nil {
		for {
			data, err := a.direct.ReceiveDatagram(a.directCtx)
			if err != nil {
				return
			}
			if len(data) < 9 || len(data) > udpLinkMax {
				continue
			}
			sid := binary.BigEndian.Uint32(data[:4])
			if sid == 0 {
				continue
			}
			if _, _, _, err := decodeUDP(data[4:]); err != nil {
				continue
			}
			a.mu.Lock()
			s := a.streams[sid]
			a.mu.Unlock()
			if udp, ok := s.(*udpStream); ok {
				udp.deliverDatagram(append([]byte(nil), data[4:]...))
			}
		}
	}
	buf := make([]byte, 65535)
	for {
		n, from, err := a.udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !from.IP.Equal(a.udpPeer.IP) || from.Port != a.udpPeer.Port || n < 9 || n > udpLinkMax {
			a.cfg.Meter.UDP("", "invalid_local_frame", n)
			continue
		}
		sid := binary.BigEndian.Uint32(buf[:4])
		if _, _, _, err := decodeUDP(buf[4:n]); sid == 0 || err != nil {
			a.cfg.Meter.UDP("", "invalid_socks_datagram", n)
			continue
		}
		a.mu.Lock()
		s := a.streams[sid]
		a.mu.Unlock()
		if udp, ok := s.(*udpStream); ok {
			udp.deliverDatagram(append([]byte(nil), buf[4:n]...))
		}
	}
}

func (a *agent) sendUDP(sid uint32, packet []byte) error {
	if len(packet)+4 > udpLinkMax {
		a.cfg.Meter.UDP("", "oversize", len(packet))
		return errors.New("RSX3 UDP datagram exceeds 1100 bytes")
	}
	data := make([]byte, len(packet)+4)
	binary.BigEndian.PutUint32(data[:4], sid)
	copy(data[4:], packet)
	if a.direct != nil {
		return a.direct.SendDatagram(data)
	}
	_, err := a.udpConn.WriteToUDP(data, a.udpPeer)
	if err == nil {
		a.cfg.Meter.UDP("received", "", len(packet))
	} else {
		a.cfg.Meter.UDP("", "local_write", len(packet))
	}
	return err
}

func (a *agent) send(f frame) error {
	if a.sender == nil {
		return net.ErrClosed
	}
	return a.sender.send(f)
}

func (a *agent) register(id uint32, s stream) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.streams) >= maxSessions || id == 0 || a.streams[id] != nil {
		return false
	}
	a.streams[id] = s
	return true
}

func (a *agent) unregister(id uint32, s stream) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.streams[id] == s {
		delete(a.streams, id)
	}
}

func (a *agent) stopAll() {
	a.mu.Lock()
	all := make([]stream, 0, len(a.streams))
	for _, s := range a.streams {
		all = append(all, s)
	}
	a.mu.Unlock()
	for _, s := range all {
		s.stop()
	}
}

type udpStream struct {
	agent   *agent
	id      uint32
	in      chan []byte
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	control *net.TCPConn
	socket  *net.UDPConn
	relay   *net.UDPAddr
	counted bool
}

func (s *udpStream) stop() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		if s.counted {
			s.agent.cfg.Meter.UDPActive(-1)
			s.counted = false
		}
		if s.control != nil {
			_ = s.control.Close()
		}
		if s.socket != nil {
			_ = s.socket.Close()
		}
		s.mu.Unlock()
		s.agent.unregister(s.id, s)
	})
}

func (s *udpStream) deliver(f frame) {
	if f.kind == udpClose {
		s.stop()
		return
	}
}

func (s *udpStream) deliverDatagram(data []byte) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.in <- data:
	default:
		// Drop unreliable UDP instead of blocking all TCP control sessions.
		s.agent.cfg.Meter.UDP("", "queue_full", len(data))
	}
}

func (s *udpStream) run() {
	defer s.stop()
	if s.agent.cfg.DirectQUIC && s.agent.cfg.DirectTarget {
		s.runNativeUDP()
		return
	}
	control, socket, relay, err := targetUDP(context.Background(), s.agent.cfg)
	if err != nil {
		_ = s.agent.send(frame{udpResult, s.id, []byte{5}})
		s.agent.cfg.Meter.UDP("", "associate_failed", 0)
		return
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		_ = control.Close()
		_ = socket.Close()
		return
	default:
	}
	s.control, s.socket, s.relay = control, socket, relay
	s.counted = true
	s.agent.cfg.Meter.UDPActive(1)
	s.mu.Unlock()
	if err := s.agent.send(frame{udpResult, s.id, []byte{0}}); err != nil {
		return
	}
	go func() {
		// UDP ASSOCIATE lifetime ends when the target SOCKS server closes TCP.
		_, _ = io.Copy(io.Discard, control)
		s.stop()
		_ = s.agent.send(frame{udpClose, s.id, nil})
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := socket.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !addr.IP.Equal(relay.IP) || addr.Port != relay.Port {
				continue
			}
			if _, _, _, err := decodeUDP(buf[:n]); err != nil {
				continue
			}
			if err := s.agent.sendUDP(s.id, buf[:n]); err != nil {
				// Oversized data does not fall back to reliable TCP.
				if n+4 > udpLinkMax {
					continue
				}
				return
			}
		}
	}()
	for {
		select {
		case <-s.done:
			return
		case data := <-s.in:
			if _, _, _, err := decodeUDP(data); err != nil {
				continue
			}
			// Xray/WARP receives the original SOCKS5 UDP target header.
			if _, err := socket.WriteToUDP(data, relay); err != nil {
				s.agent.cfg.Meter.UDP("", "relay_write", len(data))
			} else {
				s.agent.cfg.Meter.UDP("sent", "", len(data))
			}
		}
	}
}

// runNativeUDP is an explicit direct-mode egress. Each requested UDP target
// gets an independent connected socket, so responses cannot be spoofed by
// unrelated destinations and the original SOCKS5 address can be reconstructed.
func (s *udpStream) runNativeUDP() {
	peers := make(map[string]*net.UDPConn)
	defer func() {
		for _, c := range peers {
			_ = c.Close()
		}
	}()
	if err := s.agent.send(frame{udpResult, s.id, []byte{0}}); err != nil {
		return
	}
	for {
		select {
		case <-s.done:
			return
		case packet := <-s.in:
			host, port, payload, err := decodeUDP(packet)
			if err != nil {
				continue
			}
			key := net.JoinHostPort(host, fmt.Sprint(port))
			c := peers[key]
			if c == nil {
				// Bound a single UDP association, even when applications supply many destinations.
				if len(peers) >= 128 {
					continue
				}
				addr, err := net.ResolveUDPAddr("udp", key)
				if err != nil {
					continue
				}
				c, err = net.DialUDP("udp", nil, addr)
				if err != nil {
					continue
				}
				peers[key] = c
				go func(peer *net.UDPConn, h string, p int) {
					buf := make([]byte, 65535)
					for {
						n, err := peer.Read(buf)
						if err != nil {
							return
						}
						head, err := encodeTarget(h, p)
						if err != nil {
							return
						}
						packet := append(append([]byte{0, 0, 0}, head...), buf[:n]...)
						_ = s.agent.sendUDP(s.id, packet)
					}
				}(c, host, port)
			}
			_, _ = c.Write(payload)
		}
	}
}
