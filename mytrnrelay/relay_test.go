package mytrnrelay

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeWARP struct {
	listener  net.Listener
	seen      chan string
	mu        sync.Mutex
	conns     []net.Conn
	udp       []*net.UDPConn
	requests  int
	publicBND bool
	done      bool
}

func fakeWARPServer(t *testing.T, publicBND bool) *fakeWARP {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeWARP{listener: ln, seen: make(chan string, 20), publicBND: publicBND}
	go f.accept()
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWARP) accept() {
	for {
		c, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.done {
			f.mu.Unlock()
			_ = c.Close()
			return
		}
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		go f.handle(c)
	}
}

func (f *fakeWARP) handle(c net.Conn) {
	defer c.Close()
	var greet [3]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		return
	}
	if greet != [3]byte{5, 1, 0} {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [10]byte
	if _, err := io.ReadFull(c, request[:]); err != nil {
		return
	}
	if request[0] != 5 || request[1] != 3 || request[3] != 1 {
		return
	}
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		udp.Close()
		return
	}
	f.udp = append(f.udp, udp)
	f.mu.Unlock()
	port := udp.LocalAddr().(*net.UDPAddr).Port
	reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if f.publicBND {
		copy(reply[4:8], []byte{8, 8, 8, 8})
	}
	if _, err := c.Write(reply); err != nil {
		udp.Close()
		return
	}
	if !f.publicBND {
		go f.echoUDP(udp)
	}
	_, _ = io.Copy(io.Discard, c)
	_ = udp.Close()
}

func (f *fakeWARP) echoUDP(udp *net.UDPConn) {
	buffer := make([]byte, 65535)
	for {
		n, src, err := udp.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if n < 10 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 || buffer[3] != 1 {
			continue
		}
		host := net.IP(buffer[4:8]).String()
		port := binary.BigEndian.Uint16(buffer[8:10])
		select {
		case f.seen <- net.JoinHostPort(host, itoa(int(port))):
		default:
		}
		// A mock WARP UDP server returns a framed reply. There is NO remote
		// UDP dial by either the fake server or the real MyTRN relay.
		_, _ = udp.WriteToUDP(buffer[:n], src)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var data [5]byte
	pos := len(data)
	for value > 0 {
		pos--
		data[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(data[pos:])
}

func (f *fakeWARP) Close() {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done = true
	conns := append([]net.Conn(nil), f.conns...)
	udps := append([]*net.UDPConn(nil), f.udp...)
	f.mu.Unlock()
	_ = f.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	for _, u := range udps {
		_ = u.Close()
	}
}

func waitAssociation(t *testing.T, relay *Relay, ready bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if relay.Status().Association == ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WARP association never became %t: %+v", ready, relay.Status())
}

func readEcho(t *testing.T, client *net.UDPConn, expected string) {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte(expected)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	n, err := client.Read(buffer)
	if err != nil {
		t.Fatalf("SOCKS5 UDP echo unavailable: %v", err)
	}
	if string(buffer[:n]) != expected {
		t.Fatalf("response = %q, want %q", buffer[:n], expected)
	}
}

func TestHotEndpointSwitchNoXrayUDPRebind(t *testing.T) {
	f := fakeWARPServer(t, false)
	r, err := New("127.0.0.1:0", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.UpdateTarget("119.98.144.218", 57197); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialUDP("udp4", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	originalPort := client.LocalAddr().(*net.UDPAddr).Port
	readEcho(t, client, "first-QUIC-datagram")
	waitAssociation(t, r, true)
	select {
	case first := <-f.seen:
		if first != "119.98.144.218:57197" {
			t.Fatalf("SOCKS target = %s", first)
		}
	case <-time.After(time.Second):
		t.Fatal("no first SOCKS relay datagram")
	}
	if err := r.UpdateTarget("119.98.144.219", 55001); err != nil {
		t.Fatal(err)
	}
	readEcho(t, client, "second-QUIC-datagram")
	select {
	case second := <-f.seen:
		if second != "119.98.144.219:55001" {
			t.Fatalf("SOCKS target not hot-updated: %s", second)
		}
	case <-time.After(time.Second):
		t.Fatal("no second SOCKS relay datagram")
	}
	if client.LocalAddr().(*net.UDPAddr).Port != originalPort ||
		r.Status().Target != "119.98.144.219:55001" {
		t.Fatal("Xray local socket was rebound or remote destination was stale")
	}
	f.Close()
	waitAssociation(t, r, false)
	_ = client.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	_, _ = client.Write([]byte("must-not-native-fallback"))
	var buf [256]byte
	if n, err := client.Read(buf[:]); err == nil {
		t.Fatalf("WARP unavailable but %d bytes passed via an unexpected path", n)
	}
	if err := r.UpdateTarget("", 0); err != nil {
		t.Fatal(err)
	}
	if r.Status().Target != "" {
		t.Fatal("trust reset retained remote endpoint")
	}
}

func TestFailClosedRejectsRemoteSOCKSUDPEndpoint(t *testing.T) {
	f := fakeWARPServer(t, true)
	r, err := New("127.0.0.1:0", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.UpdateTarget("119.98.144.218", 57197); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialUDP("udp4", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write([]byte("trigger-rejected-SOCKS-BND"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := r.Status()
		if st.Error != "" {
			if st.Association {
				t.Fatalf("remote SOCKS BND activated: %+v", st)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("remote SOCKS5 BND error not surfaced")
}

func TestWARPProxyChangesReconnectWithoutRebindingXray(t *testing.T) {
	first := fakeWARPServer(t, false)
	second := fakeWARPServer(t, false)
	r, err := New("127.0.0.1:0", first.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.UpdateTarget("119.98.144.218", 57197); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialUDP("udp4", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	readEcho(t, client, "first-WARP-association")
	waitAssociation(t, r, true)
	select {
	case <-first.seen:
	case <-time.After(time.Second):
		t.Fatal("old WARP never received initial QUIC payload")
	}
	host, rawPort, _ := net.SplitHostPort(second.listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	if err := r.UpdateWARP(host, port); err != nil {
		t.Fatal(err)
	}
	waitAssociation(t, r, true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _ = client.Write([]byte("second-WARP-association"))
		var buffer [256]byte
		n, err := client.Read(buffer[:])
		if err == nil && string(buffer[:n]) == "second-WARP-association" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new WARP association did not forward: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case addr := <-second.seen:
		if addr != "119.98.144.218:57197" {
			t.Fatalf("new WARP target = %s", addr)
		}
	case <-time.After(time.Second):
		t.Fatal("new WARP did not receive QUIC payload")
	}
}

func TestIndependentSOCKSAssociationPerXrayQUICSocket(t *testing.T) {
	f := fakeWARPServer(t, false)
	r, err := New("127.0.0.1:0", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.UpdateTarget("119.98.144.218", 57197); err != nil {
		t.Fatal(err)
	}
	first, err := net.DialUDP("udp4", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.DialUDP("udp4", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Open two live QUIC-like UDP sockets to the same fixed B relay port.
	// SOCKS5 UDP only identifies the A destination, NOT the source Xray socket.
	// A shared association would allow responses to cross the two sockets.
	var wg sync.WaitGroup
	for _, entry := range []struct {
		conn *net.UDPConn
		body string
	}{{first, "xray-quic-A"}, {second, "xray-quic-B"}} {
		wg.Add(1)
		go func(conn *net.UDPConn, body string) {
			defer wg.Done()
			_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
			_, _ = conn.Write([]byte(body))
			var buf [256]byte
			n, err := conn.Read(buf[:])
			if err != nil || string(buf[:n]) != body {
				t.Errorf("QUIC socket %s got %q err=%v", body, buf[:n], err)
			}
		}(entry.conn, entry.body)
	}
	wg.Wait()
	status := r.Status()
	if status.Clients != 2 || status.Associations != 2 {
		t.Fatalf("Xray source sockets are sharing or losing SOCKS associations: %+v", status)
	}
	f.mu.Lock()
	associations := f.requests
	f.mu.Unlock()
	if associations != 2 {
		t.Fatalf("expected one WARP UDP ASSOCIATE per local QUIC socket, got %d", associations)
	}
}

func TestRejectNativeOrPublicListeners(t *testing.T) {
	for _, cfg := range [][2]string{
		{"0.0.0.0:41001", "127.0.0.1:40000"},
		{"127.0.0.1:0", "8.8.8.8:40000"},
		{"127.0.0.1:0", "0.0.0.0:40000"},
	} {
		r, err := New(cfg[0], cfg[1])
		if err == nil {
			r.Close()
			t.Fatalf("unsafe relay bound: %v", cfg)
		}
	}
}
