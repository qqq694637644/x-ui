package reverseexit

// RSX2 overseas Agent: only WARP SOCKS5 can open Internet connections.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
const tcpWindowFrames = 32

type stream interface {
	deliver(frame)
	stop()
}

type agent struct {
	conn    net.Conn
	udpConn *net.UDPConn
	udpPeer *net.UDPAddr
	cfg     Config
	sender  *fairWriter
	mu      sync.Mutex
	streams map[uint32]stream
}

// Run retries exclusively through the existing local XTCP Visitor. Every
// destination is reached through WARP SOCKS5, with no direct fallback.
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
			if cfg.Protocol == "rsx2" {
				err = serve(ctx, c, cfg, status)
			} else {
				err = serveRSX3(ctx, c, cfg, status)
			}
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

func serve(ctx context.Context, c net.Conn, cfg Config, status func(string, string)) error {
	defer c.Close()
	if err := authenticate(c, []byte(cfg.Token)); err != nil {
		return err
	}
	bind, _ := net.ResolveUDPAddr("udp4", cfg.UDPBindAddr)
	peer, _ := net.ResolveUDPAddr("udp4", cfg.UDPPeerAddr)
	udpConn, err := net.ListenUDP("udp4", bind)
	if err != nil {
		return fmt.Errorf("bind RSX2 UDP socket: %w", err)
	}
	defer udpConn.Close()
	a := &agent{conn: c, udpConn: udpConn, udpPeer: peer, cfg: cfg,
		streams: make(map[uint32]stream)}
	a.sender = newFairWriter(c)
	defer a.sender.close()
	go a.datagramLoop()
	status("online", "")
	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()
	go func() {
		for {
			status("warp-checking", "")
			err := probeWARP(probeCtx, cfg)
			if probeCtx.Err() != nil {
				return
			}
			if err != nil {
				status("warp-error", err.Error())
			} else {
				status("warp-ready", "")
			}
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-timer.C:
			case <-probeCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			}
		}
	}()
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
			s := &tcpStream{agent: a, id: f.id, in: make(chan frame, tcpWindowFrames+2),
				credits: make(chan struct{}, tcpWindowFrames), done: make(chan struct{})}
			for i := 0; i < tcpWindowFrames; i++ {
				s.credits <- struct{}{}
			}
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
		case tcpData, tcpFIN, tcpRST, tcpWindow, udpClose:
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
			return fmt.Errorf("invalid RSX2 message %d", f.kind)
		}
	}
}

// Session ID (4 BE bytes) + unmodified RFC1928 UDP packet. Neither UDP
// payloads nor their retransmission are ever carried through the TCP mux.
func (a *agent) datagramLoop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := a.udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !from.IP.Equal(a.udpPeer.IP) || from.Port != a.udpPeer.Port || n < 9 || n > udpLinkMax {
			continue
		}
		sid := binary.BigEndian.Uint32(buf[:4])
		if _, _, _, err := decodeUDP(buf[4:n]); sid == 0 || err != nil {
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
		return errors.New("RSX2 UDP datagram exceeds 1100 bytes")
	}
	data := make([]byte, len(packet)+4)
	binary.BigEndian.PutUint32(data[:4], sid)
	copy(data[4:], packet)
	_, err := a.udpConn.WriteToUDP(data, a.udpPeer)
	return err
}

func authenticate(c net.Conn, secret []byte) error {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.SetDeadline(time.Time{})
	var challenge [36]byte
	if _, err := io.ReadFull(c, challenge[:]); err != nil {
		return err
	}
	if string(challenge[:4]) != "RSX2" {
		return errors.New("invalid RSX2 protocol version")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(challenge[4:])
	return writeAll(c, mac.Sum(nil))
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

type tcpStream struct {
	agent   *agent
	id      uint32
	in      chan frame
	credits chan struct{}
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	conn    *net.TCPConn
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
	if f.kind == tcpWindow {
		if len(f.data) != 2 {
			s.stop()
			return
		}
		grant := int(binary.BigEndian.Uint16(f.data))
		if grant < 1 || grant > tcpWindowFrames {
			s.stop()
			return
		}
		for i := 0; i < grant; i++ {
			select {
			case s.credits <- struct{}{}:
			case <-s.done:
				return
			default:
				s.stop() // unexpected extra credit from peer
				return
			}
		}
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.in <- f:
	case <-s.done:
	default:
		s.stop() // peer exceeded its negotiated receive window
		go s.agent.send(frame{tcpRST, s.id, nil})
	}
}

func (s *tcpStream) run(host string, port int) {
	defer s.stop()
	warpStarted := time.Now()
	conn, err := warpTCP(context.Background(), s.agent.cfg, host, port)
	log.Printf("RSX2 metric warp_connect sid=%d duration_ms=%d success=%t",
		s.id, time.Since(warpStarted).Milliseconds(), err == nil)
	if err != nil {
		_ = s.agent.send(frame{tcpResult, s.id, []byte{5}})
		return
	}
	c := conn
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
			select {
			case <-s.done:
				return
			case <-s.credits:
			}
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
	creditsReady := 0
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
				creditsReady++
				if creditsReady >= 8 {
					var grant [2]byte
					binary.BigEndian.PutUint16(grant[:], uint16(creditsReady))
					if err := s.agent.send(frame{tcpWindow, s.id, grant[:]}); err != nil {
						return
					}
					creditsReady = 0
				}
			case tcpFIN:
				if creditsReady > 0 {
					var grant [2]byte
					binary.BigEndian.PutUint16(grant[:], uint16(creditsReady))
					_ = s.agent.send(frame{tcpWindow, s.id, grant[:]})
					creditsReady = 0
				}
				inboundFIN = true
				_ = c.CloseWrite()
			case tcpRST:
				return
			}
		}
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
}

func (s *udpStream) stop() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
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
	}
}

func (s *udpStream) run() {
	defer s.stop()
	control, socket, relay, err := warpUDP(context.Background(), s.agent.cfg)
	if err != nil {
		_ = s.agent.send(frame{udpResult, s.id, []byte{5}})
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
	s.mu.Unlock()
	if err := s.agent.send(frame{udpResult, s.id, []byte{0}}); err != nil {
		return
	}
	go func() {
		// UDP ASSOCIATE lifetime ends when WARP closes this TCP channel.
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
			// Pass the original SOCKS5 UDP header to WARP unchanged.
			_, _ = socket.WriteToUDP(data, relay)
		}
	}
}
