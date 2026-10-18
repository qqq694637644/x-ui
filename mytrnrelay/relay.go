// Package mytrnrelay carries Xray Hysteria/QUIC UDP sockets over
// mandatory loopback SOCKS5 UDP ASSOCIATE. It never dials an A endpoint itself.
//
// Xray sends to a fixed 127.0.0.1 UDP port; authenticated control-plane
// registrations update only the SOCKS5 destination header. Closing or losing
// WARP fails closed: packets are dropped until another SOCKS association works.
// Each *local Xray UDP source port* owns its own SOCKS5 UDP association,
// preventing QUIC packets from different sockets from sharing response paths.
package mytrnrelay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const ListenAddress = "127.0.0.1:41001"

const (
	maxClients       = 64
	clientIdle       = 3 * time.Minute
	clientQueueDepth = 32
)

type association struct {
	tcp   net.Conn
	udp   *net.UDPConn
	relay *net.UDPAddr
	once  sync.Once
}

func (a *association) close() {
	a.once.Do(func() {
		_ = a.tcp.Close()
		_ = a.udp.Close()
	})
}

type Relay struct {
	local  *net.UDPConn
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.RWMutex
	warp    string
	target  *net.UDPAddr
	clients map[string]*clientSession
}

type clientSession struct {
	address      *net.UDPAddr
	lastActivity time.Time
	queue        chan []byte
	active       *association
	lastError    string
}

type Status struct {
	Running      bool   `json:"running"`
	Association  bool   `json:"association"`
	Clients      int    `json:"clients"`
	Associations int    `json:"associations"`
	Target       string `json:"target"`
	Error        string `json:"error,omitempty"`
}

// New only binds a UDP socket on loopback; no public-facing port is opened.
// Start a replacement via Close/New if the local listening address must change.
func New(listenAddr, warpAddr string) (*Relay, error) {
	host, port, err := net.SplitHostPort(warpAddr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || ip == nil || ip.To4() == nil || !ip.IsLoopback() {
		return nil, errors.New("WARP SOCKS5 TCP endpoint must be an IPv4 loopback address")
	}
	localAddr, err := net.ResolveUDPAddr("udp4", listenAddr)
	if err != nil || localAddr.IP == nil || localAddr.IP.To4() == nil || !localAddr.IP.IsLoopback() {
		return nil, errors.New("MyTRN relay listener must be IPv4 loopback")
	}
	conn, err := net.ListenUDP("udp4", localAddr)
	if err != nil {
		return nil, fmt.Errorf("listen MyTRN relay %s: %w", listenAddr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Relay{local: conn, ctx: ctx, cancel: cancel, warp: warpAddr,
		clients: make(map[string]*clientSession)}
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.readLocal() }()
	return r, nil
}

func (r *Relay) Addr() *net.UDPAddr { return r.local.LocalAddr().(*net.UDPAddr) }

func (r *Relay) Status() Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := Status{Running: r.ctx.Err() == nil, Clients: len(r.clients)}
	for _, client := range r.clients {
		if client.active != nil {
			st.Associations++
		}
		if st.Error == "" && client.lastError != "" {
			st.Error = client.lastError
		}
	}
	st.Association = st.Associations > 0
	if r.target != nil {
		st.Target = r.target.String()
	}
	return st
}

// UpdateTarget only alters the SOCKS5 UDP destination header. Existing QUIC
// sockets, stream state and the entire x-ui Xray process remain untouched.
// Empty host/port clears the target when an administrator revokes trust.
func (r *Relay) UpdateTarget(host string, port int) error {
	var next *net.UDPAddr
	if host != "" || port != 0 {
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil || port < 1 || port > 65535 {
			return errors.New("MyTRN target must be IPv4:port")
		}
		next = &net.UDPAddr{IP: ip.To4(), Port: port}
	}
	r.mu.Lock()
	r.target = next
	r.mu.Unlock()
	return nil
}

// UpdateWARP tears down the previous association. New packets are dropped
// until a SOCKS5 UDP association succeeds with the new loopback proxy.
func (r *Relay) UpdateWARP(host string, port int) error {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || !ip.IsLoopback() || port < 1 || port > 65535 {
		return errors.New("WARP SOCKS5 proxy must be IPv4 loopback:port")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	r.mu.Lock()
	if r.warp == addr {
		r.mu.Unlock()
		return nil
	}
	r.warp = addr
	old := make([]*association, 0, len(r.clients))
	for _, client := range r.clients {
		if client.active != nil {
			old = append(old, client.active)
			client.active = nil
		}
		client.lastError = "WARP association reconnecting"
	}
	r.mu.Unlock()
	for _, assoc := range old {
		assoc.close()
	}
	return nil
}

func (r *Relay) Close() {
	r.cancel()
	_ = r.local.Close()
	r.mu.Lock()
	old := make([]*association, 0, len(r.clients))
	for _, client := range r.clients {
		if client.active != nil {
			old = append(old, client.active)
			client.active = nil
		}
	}
	r.mu.Unlock()
	for _, assoc := range old {
		assoc.close()
	}
	r.wg.Wait()
}

func (r *Relay) readLocal() {
	buffer := make([]byte, 65535)
	for {
		n, client, err := r.local.ReadFromUDP(buffer)
		if err != nil {
			// A dead loopback listener must not be reported as healthy.
			r.cancel()
			return
		}
		if n == 0 || client.IP == nil || !client.IP.IsLoopback() {
			continue
		}
		r.mu.Lock()
		if r.ctx.Err() != nil || r.target == nil {
			r.mu.Unlock()
			continue
		}
		key := client.String()
		session := r.clients[key]
		if session == nil && len(r.clients) < maxClients {
			session = &clientSession{address: client, lastActivity: time.Now(),
				queue: make(chan []byte, clientQueueDepth)}
			r.clients[key] = session
			r.wg.Add(1)
			go func() { defer r.wg.Done(); r.manageClient(key, session) }()
		}
		if session != nil {
			session.lastActivity = time.Now()
		}
		r.mu.Unlock()
		if session == nil {
			continue // maxClients: bounded resource exhaustion protection.
		}
		packet := append([]byte(nil), buffer[:n]...)
		select {
		case session.queue <- packet:
		default: // QUIC retransmits; never stall other Xray sessions.
		}
	}
}

func (r *Relay) readUpstream(session *clientSession, a *association, done chan<- struct{}) {
	defer func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}()
	buffer := make([]byte, 65535)
	for {
		n, source, err := a.udp.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if !source.IP.Equal(a.relay.IP) || source.Port != a.relay.Port || n < 10 {
			continue
		}
		if buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 || buffer[3] != 1 {
			continue // No fragments, domains or IPv6 surprises.
		}
		ip := net.IP(buffer[4:8])
		port := int(binary.BigEndian.Uint16(buffer[8:10]))
		r.mu.RLock()
		target := r.target
		match := session.active == a && target != nil &&
			target.IP.Equal(ip) && target.Port == port
		r.mu.RUnlock()
		if match {
			_, _ = r.local.WriteToUDP(buffer[10:n], session.address)
		}
	}
}

// One association per Xray UDP source guarantees QUIC response demultiplexing
// without ever attempting to parse, modify or decrypt QUIC Connection IDs.
func (r *Relay) manageClient(key string, session *clientSession) {
	defer func() {
		r.mu.Lock()
		if r.clients[key] == session {
			delete(r.clients, key)
		}
		r.mu.Unlock()
	}()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for r.ctx.Err() == nil {
		r.mu.RLock()
		warp := r.warp
		idle := time.Since(session.lastActivity) > clientIdle
		r.mu.RUnlock()
		if idle {
			return
		}
		a, err := openSOCKSAssociation(r.ctx, warp)
		if err != nil {
			r.mu.Lock()
			session.lastError = err.Error()
			r.mu.Unlock()
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(time.Second):
			case <-ticker.C:
			}
			continue
		}
		r.mu.Lock()
		// UpdateWARP may have raced with establishment.
		if r.ctx.Err() != nil || r.warp != warp {
			r.mu.Unlock()
			a.close()
			continue
		}
		session.active = a
		session.lastError = ""
		r.mu.Unlock()

		done := make(chan struct{}, 2)
		go r.readUpstream(session, a, done)
		go func() {
			_, _ = io.Copy(io.Discard, a.tcp) // SOCKS TCP lifetime controls UDP.
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		expired := false
	activeLoop:
		for {
			select {
			case packet := <-session.queue:
				r.mu.RLock()
				target, valid := r.target, session.active == a
				r.mu.RUnlock()
				if !valid || target == nil {
					continue // WARP disconnected or trust revoked: drop.
				}
				frame := make([]byte, len(packet)+10)
				frame[3] = 1 // SOCKS5 UDP: RSV, FRAG=0, IPv4, IP, port.
				copy(frame[4:8], target.IP.To4())
				binary.BigEndian.PutUint16(frame[8:10], uint16(target.Port))
				copy(frame[10:], packet)
				if _, err := a.udp.WriteToUDP(frame, a.relay); err != nil {
					break activeLoop
				}
			case <-done:
				break activeLoop
			case <-ticker.C:
				r.mu.RLock()
				expired = time.Since(session.lastActivity) > clientIdle
				r.mu.RUnlock()
				if expired {
					break activeLoop
				}
			case <-r.ctx.Done():
				break activeLoop
			}
		}
		a.close()
		r.mu.Lock()
		if session.active == a {
			session.active = nil
			session.lastError = "WARP SOCKS5 UDP association closed"
		}
		r.mu.Unlock()
		if expired {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func openSOCKSAssociation(ctx context.Context, proxy string) (*association, error) {
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*association, error) {
		_ = udp.Close()
		return nil, err
	}
	dialer := net.Dialer{Timeout: 4 * time.Second}
	tcp, err := dialer.DialContext(ctx, "tcp4", proxy)
	if err != nil {
		return fail(fmt.Errorf("WARP SOCKS5 unreachable: %w", err))
	}
	ready := false
	defer func() {
		if !ready {
			_ = tcp.Close()
		}
	}()
	_ = tcp.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err = tcp.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	var response [4]byte
	if _, err = io.ReadFull(tcp, response[:2]); err != nil {
		return fail(err)
	}
	if response[0] != 5 || response[1] != 0 {
		return fail(errors.New("WARP SOCKS5 requires no-auth negotiation"))
	}
	source := udp.LocalAddr().(*net.UDPAddr)
	request := []byte{5, 3, 0, 1, 127, 0, 0, 1, byte(source.Port >> 8), byte(source.Port)}
	if _, err = tcp.Write(request); err != nil {
		return fail(err)
	}
	if _, err = io.ReadFull(tcp, response[:]); err != nil {
		return fail(err)
	}
	if response != [4]byte{5, 0, 0, 1} {
		return fail(errors.New("WARP SOCKS5 refused UDP ASSOCIATE or returned non-IPv4 BND"))
	}
	var bound [6]byte
	if _, err = io.ReadFull(tcp, bound[:]); err != nil {
		return fail(err)
	}
	ip := net.IPv4(bound[0], bound[1], bound[2], bound[3])
	port := int(binary.BigEndian.Uint16(bound[4:]))
	if ip.Equal(net.IPv4zero) {
		host, _, _ := net.SplitHostPort(proxy)
		ip = net.ParseIP(host).To4()
	}
	if ip == nil || !ip.IsLoopback() || port == 0 {
		return fail(errors.New("WARP SOCKS5 UDP relay must bind IPv4 loopback"))
	}
	_ = tcp.SetDeadline(time.Time{})
	ready = true
	return &association{tcp: tcp, udp: udp, relay: &net.UDPAddr{IP: ip, Port: port}}, nil
}
