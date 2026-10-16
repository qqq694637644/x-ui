package reverseexit

// Exit agent: the outside VPS actively connects to the frpc XTCP Visitor TCP
// port. The inside node owns the local SOCKS5 port; this side dials Internet
// TCP/UDP destinations. No changes to frp NAT traversal or WARP are required.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

const maxSessions = 1024

type stream interface {
	deliver(frame)
	stop()
}

type agent struct {
	conn    net.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	streams map[uint32]stream
}

// Run retries the visitor connection indefinitely until ctx is canceled.
// token is a shared secret (minimum 16 bytes) also configured on the NAT node.
func Run(ctx context.Context, visitor, token string, retry time.Duration) error {
	if len(token) < 16 {
		return errors.New("REVERSE_SOCKS_TOKEN must contain at least 16 characters")
	}
	if retry <= 0 {
		return errors.New("retry must be positive")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for ctx.Err() == nil {
		c, err := dialer.DialContext(ctx, "tcp", visitor)
		if err == nil {
			log.Printf("reverse SOCKS: connected to XTCP visitor %s", visitor)
			err = serve(ctx, c, []byte(token))
		}
		if ctx.Err() != nil {
			break
		}
		log.Printf("reverse SOCKS: connection lost: %v; retrying in %s", err, retry)
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
	return ctx.Err()
}

func serve(ctx context.Context, c net.Conn, secret []byte) error {
	defer c.Close()
	if err := authenticate(c, secret); err != nil {
		return err
	}
	a := &agent{conn: c, streams: make(map[uint32]stream)}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	defer close(done)
	defer a.stopAll()
	for {
		f, err := readFrame(c)
		if err != nil {
			return err
		}
		switch f.kind {
		case openTCP:
			host, port, used, err := decodeAddress(f.data)
			if err != nil || used != len(f.data) {
				_ = a.send(frame{tcpResult, f.id, []byte{5}})
				continue
			}
			s := &tcpStream{agent: a, id: f.id, in: make(chan frame, 32),
				done: make(chan struct{})}
			if !a.register(f.id, s) {
				_ = a.send(frame{tcpResult, f.id, []byte{5}})
				continue
			}
			go s.run(host, port)
		case openUDP:
			if len(f.data) != 0 {
				continue
			}
			s := &udpStream{agent: a, id: f.id, in: make(chan []byte, 128),
				done: make(chan struct{})}
			if a.register(f.id, s) {
				go s.run()
			}
		case tcpData, tcpFIN, tcpRST, udpData, udpClose:
			a.mu.Lock()
			s := a.streams[f.id]
			a.mu.Unlock()
			if s != nil {
				s.deliver(f)
			}
		case ping:
			if err := a.send(frame{pong, f.id, f.data}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid RSX1 message %d", f.kind)
		}
	}
}

func authenticate(c net.Conn, secret []byte) error {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.SetDeadline(time.Time{})
	var challenge [36]byte
	if _, err := io.ReadFull(c, challenge[:]); err != nil {
		return err
	}
	if string(challenge[:4]) != "RSX1" {
		return errors.New("invalid RSX1 protocol version")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(challenge[4:])
	return writeAll(c, mac.Sum(nil))
}

func (a *agent) send(f frame) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := a.conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	err := writeFrame(a.conn, f)
	if err != nil {
		a.conn.Close()
	}
	return err
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

type tcpStream struct {
	agent *agent
	id    uint32
	in    chan frame
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	conn  *net.TCPConn
}

func (s *tcpStream) stop() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
		}
		s.mu.Unlock()
		s.agent.unregister(s.id, s)
	})
}

func (s *tcpStream) deliver(f frame) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.in <- f:
	case <-s.done:
	default:
		s.stop() // A slow destination must not stall all mux sessions.
		go s.agent.send(frame{tcpRST, s.id, nil})
	}
}

func (s *tcpStream) run(host string, port int) {
	defer s.stop()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)),
		10*time.Second)
	if err != nil {
		_ = s.agent.send(frame{tcpResult, s.id, []byte{5}})
		return
	}
	c := conn.(*net.TCPConn)
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		c.Close()
		return
	default:
	}
	s.conn = c
	s.mu.Unlock()
	defer c.Close()
	if s.agent.send(frame{tcpResult, s.id, []byte{0}}) != nil {
		return
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, tcpChunk)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				if s.agent.send(frame{tcpData, s.id, append([]byte(nil), buf[:n]...)}) != nil {
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					_ = s.agent.send(frame{tcpFIN, s.id, nil})
				} else {
					_ = s.agent.send(frame{tcpRST, s.id, nil})
				}
				return
			}
		}
	}()
	inboundFIN := false
	for {
		if inboundFIN && readDone == nil {
			return
		}
		select {
		case <-s.done:
			return
		case <-readDone:
			readDone = nil
		case f := <-s.in:
			switch f.kind {
			case tcpData:
				if inboundFIN {
					return
				}
				if err := writeAll(c, f.data); err != nil {
					_ = s.agent.send(frame{tcpRST, s.id, nil})
					return
				}
			case tcpFIN:
				inboundFIN = true
				_ = c.CloseWrite()
			case tcpRST:
				return
			}
		}
	}
}

type udpStream struct {
	agent *agent
	id    uint32
	in    chan []byte
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	ipv4  *net.UDPConn
	ipv6  *net.UDPConn
}

func (s *udpStream) stop() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		if s.ipv4 != nil {
			s.ipv4.Close()
		}
		if s.ipv6 != nil {
			s.ipv6.Close()
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
	if f.kind != udpData {
		return
	}
	select {
	case <-s.done:
	default:
		select {
		case s.in <- f.data:
		default:
			// UDP is allowed to drop packets under pressure.
		}
	}
}

func (s *udpStream) readPackets(c *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		data, err := encodeUDP(addr, buf[:n])
		if err == nil {
			if s.agent.send(frame{udpData, s.id, data}) != nil {
				return
			}
		}
	}
}

func (s *udpStream) run() {
	defer s.stop()
	ipv4, err := net.ListenUDP("udp4", nil)
	if err != nil {
		_ = s.agent.send(frame{udpClose, s.id, nil})
		return
	}
	ipv6, _ := net.ListenUDP("udp6", nil) // IPv6 may be disabled; IPv4 remains usable.
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		ipv4.Close()
		if ipv6 != nil {
			ipv6.Close()
		}
		return
	default:
	}
	s.ipv4, s.ipv6 = ipv4, ipv6
	s.mu.Unlock()
	go s.readPackets(ipv4)
	if ipv6 != nil {
		go s.readPackets(ipv6)
	}
	for {
		select {
		case <-s.done:
			return
		case data := <-s.in:
			host, port, body, err := decodeUDP(data)
			if err != nil {
				continue
			}
			addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				continue
			}
			if addr.IP.To4() != nil {
				_, _ = ipv4.WriteToUDP(body, addr)
			} else if ipv6 != nil {
				_, _ = ipv6.WriteToUDP(body, addr)
			}
		}
	}
}
