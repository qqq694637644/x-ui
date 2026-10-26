package reverseexit

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestDiagnosticPeerCloseRevokesPerConnectionReadiness(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	c := &rsx3Control{diagReady: make(chan struct{})}
	close(c.diagReady) // historical first-handshake flag cannot be revoked
	c.diagOnline.Store(true)
	peer := &DiagnosticPeer{conn: left, control: c, done: make(chan struct{})}
	peer.Close()
	if c.diagOnline.Load() {
		t.Fatal("closed diagnostics stream still authorizes new sessions")
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("peer did not signal closure")
	}
	select {
	case <-c.diagReady:
	default:
		t.Fatal("initial capability is expected to remain recorded")
	}
	peer.Close() // idempotence must not panic
}

func TestDiagnosticPeerUnexpectedRemoteEOFRevokesReadiness(t *testing.T) {
	server, remote := net.Pipe()
	c := &rsx3Control{}
	c.diagOnline.Store(true)
	peer := &DiagnosticPeer{conn: server, control: c, done: make(chan struct{})}
	go peer.readLoop()
	_ = remote.Close()
	select {
	case <-peer.Done():
	case <-time.After(time.Second):
		t.Fatal("remote EOF did not close diagnostic peer")
	}
	if c.diagOnline.Load() {
		t.Fatal("remote EOF did not revoke new-session admission")
	}
}

func TestDiagnosticsPeerDisconnectBlocksNewSessionsAndReconnects(t *testing.T) {
	fixture, stop, logs := startPythonFixture(t, false)
	defer stop()
	cfg := fixtureAgentConfig(fixture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager()
	if err := m.Apply(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	peer := waitDiagnosticPeer(t, m, logs)
	connectSOCKS := func() (net.Conn, byte, error) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fixture.SOCKSPort), time.Second)
		if err != nil {
			return nil, 0, err
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte{5, 1, 0}); err != nil {
			_ = c.Close()
			return nil, 0, err
		}
		var greeting [2]byte
		if _, err := io.ReadFull(c, greeting[:]); err != nil {
			_ = c.Close()
			return nil, 0, err
		}
		addr, err := encodeTarget("127.0.0.1", fixture.ProbePort)
		if err != nil {
			_ = c.Close()
			return nil, 0, err
		}
		if _, err := c.Write(append([]byte{5, 1, 0}, addr...)); err != nil {
			_ = c.Close()
			return nil, 0, err
		}
		var reply [10]byte
		if _, err := io.ReadFull(c, reply[:]); err != nil {
			_ = c.Close()
			return nil, 0, err
		}
		return c, reply[1], nil
	}
	existing, code, err := connectSOCKS()
	if err != nil || code != 0 {
		t.Fatalf("initial RSX3 SOCKS failed: code=%d err=%v logs=%s", code, err, logs.String())
	}
	defer existing.Close()
	echo := func(msg string) {
		t.Helper()
		_ = existing.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := existing.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(existing, buf); err != nil || string(buf) != msg {
			t.Fatalf("established session broke: %q %v", buf, err)
		}
	}
	echo("alive-before-drop")
	peer.Close()
	if peer.control.diagOnline.Load() {
		t.Fatal("Go Agent still considers a disconnected diagnostic peer ready")
	}
	echo("alive-after-drop")
	newSession, code, err := connectSOCKS()
	if newSession != nil {
		_ = newSession.Close()
	}
	if err == nil && code == 0 {
		t.Fatal("new RSX3 business stream was accepted after diagnostics disconnect")
	}
	// The retry worker reauthenticates in the background; existing streams
	// remain alive, while new sessions may resume only after another HMAC.
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		if next := m.DiagnosticPeer(); next != nil && next != peer && next.control.diagOnline.Load() {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if next := m.DiagnosticPeer(); next == nil || next == peer || !next.control.diagOnline.Load() {
		t.Fatal("diagnostics did not reauthenticate after disconnect: " + logs.String())
	}
	echo("alive-after-reauth")
	reconnected, code, err := connectSOCKS()
	if err != nil || code != 0 {
		t.Fatalf("new RSX3 session did not resume after HMAC: %d %v", code, err)
	}
	_ = reconnected.Close()
}
