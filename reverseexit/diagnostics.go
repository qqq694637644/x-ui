package reverseexit

// Diagnostics are negotiated using the existing PING/PONG opcode, then moved
// to a separate authenticated XTCP stream. An absent or disconnected
// authenticated peer blocks new sessions when Diagnostics is enabled.
// Bulk diagnostics never uses the RSX3 lifecycle frame queue.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"x-ui/rsxdiag"
)

// Deliberately incompatible with the former optional capability probe:
// Diagnostics v2 requires authenticated bidirectional peer readiness.
const diagCapabilityRequest = "RD-CAP/2 "
const diagCapabilityReply = "RD-OK/2 "
const diagPreface = "R3G1"

// Bind blocking socket I/O to a context without requiring a newer context
// package than the panel's supported Go toolchain. A completed handshake
// disarms the callback so later cancellation does not close an active stream.
func onContextCancelled(ctx context.Context, callback func()) func() bool {
	if ctx.Done() == nil {
		return func() bool { return true }
	}
	var state atomic.Int32
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if state.CompareAndSwap(0, 1) {
				callback()
			}
		case <-done:
		}
	}()
	return func() bool {
		if state.CompareAndSwap(0, 2) {
			close(done)
			return true
		}
		return false
	}
}

type diagnosticResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}
type DiagnosticPeer struct {
	conn    net.Conn
	epoch   string
	cfg     Config
	control *rsx3Control
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan diagnosticResponse
	done    chan struct{}
	once    sync.Once
}

func (p *DiagnosticPeer) Epoch() string         { return p.epoch }
func (p *DiagnosticPeer) Done() <-chan struct{} { return p.done }
func (p *DiagnosticPeer) Close() {
	p.once.Do(func() {
		// Revoke readiness synchronously with closure, not when the retry
		// worker eventually receives <-p.Done().
		if p.control != nil {
			p.control.diagOnline.Store(false)
		}
		_ = p.conn.Close()
		close(p.done)
	})
}
func (p *DiagnosticPeer) write(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > rsxdiag.MaxFrame {
		return errors.New("diagnostic message exceeds limit")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if err := writeAll(p.conn, header[:]); err != nil {
		return err
	}
	return writeAll(p.conn, raw)
}
func (p *DiagnosticPeer) readLoop() {
	defer p.Close()
	for {
		var header [4]byte
		if _, err := io.ReadFull(p.conn, header[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(header[:])
		if n == 0 || n > rsxdiag.MaxFrame {
			return
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(p.conn, raw); err != nil {
			return
		}
		var response diagnosticResponse
		if json.Unmarshal(raw, &response) != nil || len(response.ID) != 32 {
			return
		}
		p.mu.Lock()
		ch := p.pending[response.ID]
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- response:
			default:
			}
		}
	}
}
func (p *DiagnosticPeer) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if p == nil {
		return nil, errors.New("node diagnostics not negotiated")
	}
	select {
	case <-p.done:
		return nil, errors.New("node diagnostic connection closed")
	default:
	}
	id := rsxdiag.ID()
	ch := make(chan diagnosticResponse, 1)
	p.mu.Lock()
	if len(p.pending) >= 4 {
		p.mu.Unlock()
		return nil, errors.New("diagnostic request capacity")
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	timeout := 8.0
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline).Seconds()
		if timeout > 125 {
			timeout = 125
		}
		if timeout <= 0 {
			return nil, ctx.Err()
		}
	}
	if err := p.write(map[string]interface{}{"id": id, "method": method, "params": params, "timeout": timeout}); err != nil {
		p.Close()
		return nil, err
	}
	select {
	case response := <-ch:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Result, nil
	case <-ctx.Done():
		go func() { _ = p.write(map[string]interface{}{"id": id, "method": "cancel"}) }()
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("node diagnostic stream closed")
	}
}
func (c *rsx3Control) diagnosticsPong(data []byte) {
	if !c.agent.cfg.DiagnosticsEnabled || c.diagNonce == "" || !bytes.Equal(data, []byte(diagCapabilityReply+c.diagNonce)) {
		return
	}
	c.diagOnce.Do(func() { go c.diagnosticWorker() })
}
func (c *rsx3Control) diagnosticWorker() {
	for c.ctx.Err() == nil {
		p, err := connectDiagnosticPeer(c.ctx, c)
		if err != nil {
			c.agent.cfg.Meter.Event("diagnostics_connect", err.Error(), "")
		} else {
			c.diagOnline.Store(true)
			c.diagReadyOnce.Do(func() {
				close(c.diagReady)
			})
			if c.status != nil {
				c.status("online", "")
			}
			if c.agent.cfg.DiagnosticPeerChanged != nil {
				c.agent.cfg.DiagnosticPeerChanged(nil, p)
			}
			c.agent.cfg.Meter.Event("diagnostics_ready", "independent authenticated node diagnostics", p.epoch)
			go p.readLoop()
			select {
			case <-p.Done():
			case <-c.ctx.Done():
				p.Close()
			}
			// The transport is allowed to finish existing sessions, but the
			// missing diagnostics stream forbids new TCP/UDP opens.
			if c.ctx.Err() == nil && c.status != nil {
				c.status("diagnostics-unavailable", "authenticated diagnostics stream lost; new RSX3 sessions refused until reconnect")
			}
			if c.agent.cfg.DiagnosticPeerChanged != nil {
				c.agent.cfg.DiagnosticPeerChanged(p, nil)
			}
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
func connectDiagnosticPeer(ctx context.Context, c *rsx3Control) (*DiagnosticPeer, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.agent.cfg.VisitorAddr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	stop := onContextCancelled(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeAll(conn, append([]byte(diagPreface), c.epoch[:]...)); err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(c.agent.cfg.Token))
	_, _ = mac.Write([]byte("RSX3-diag-v1"))
	_, _ = mac.Write(c.epoch[:])
	_, _ = mac.Write(nonce[:])
	if err := writeAll(conn, mac.Sum(nil)); err != nil {
		return nil, err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return nil, err
	}
	if ack[0] != 0 {
		return nil, errors.New("diagnostic HMAC refused")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &DiagnosticPeer{conn: conn, epoch: hex.EncodeToString(c.epoch[:]), cfg: c.agent.cfg, control: c, pending: make(map[string]chan diagnosticResponse), done: make(chan struct{})}, nil
}

type benchmarkPermit struct {
	expires   time.Time
	bytes     int64
	direction string
	conn      net.Conn
}

func (p *DiagnosticPeer) Benchmark(ctx context.Context, n int64, direction string) (json.RawMessage, error) {
	if n != 1_000_000 && n != 10_000_000 && n != 50_000_000 {
		return nil, errors.New("benchmark size must be 1,10,50 MB")
	}
	if direction != "upload" && direction != "download" {
		return nil, errors.New("invalid benchmark direction")
	}
	host := rsxdiag.ID() + ".rsxdiag.invalid"
	c := p.control
	c.benchMu.Lock()
	if len(c.benchmarks) != 0 {
		c.benchMu.Unlock()
		return nil, errors.New("another RSX3 benchmark is active")
	}
	permit := &benchmarkPermit{expires: time.Now().Add(125 * time.Second), bytes: n, direction: direction}
	c.benchmarks[host] = permit
	c.benchMu.Unlock()
	defer func() {
		c.benchMu.Lock()
		delete(c.benchmarks, host)
		if permit.conn != nil {
			_ = permit.conn.Close()
		}
		c.benchMu.Unlock()
	}()
	return p.Call(ctx, "socks_benchmark", map[string]interface{}{"host": host, "bytes": n, "direction": direction})
}

// benchmarkTarget is a one-use, random authorization issued only by a logged-in
// diagnostic task. Reserved hostnames without permits are never forwarded to
// Xray/WARP. This is not a general direct-connect exception.
func (c *rsx3Control) benchmarkTarget(host string) (net.Conn, bool, error) {
	if !strings.HasSuffix(host, ".rsxdiag.invalid") {
		return nil, false, nil
	}
	c.benchMu.Lock()
	permit := c.benchmarks[host]
	if permit == nil || time.Now().After(permit.expires) || permit.conn != nil {
		c.benchMu.Unlock()
		return nil, true, errors.New("unauthorized diagnostic target")
	}
	client, server := net.Pipe()
	permit.conn = server
	c.benchMu.Unlock()
	go func() {
		defer server.Close()
		_ = server.SetDeadline(permit.expires)
		var header [9]byte
		if _, err := io.ReadFull(server, header[:]); err != nil {
			return
		}
		direction := byte(0)
		if permit.direction == "upload" {
			direction = 1
		}
		if header[0] != direction || int64(binary.BigEndian.Uint64(header[1:])) != permit.bytes {
			return
		}
		hash := sha256.New()
		remaining := permit.bytes
		if direction == 0 {
			buf := make([]byte, 32768)
			for i := range buf {
				buf[i] = byte(i % 251)
			}
			for remaining > 0 {
				k := int64(len(buf))
				if remaining < k {
					k = remaining
				}
				if err := writeAll(server, buf[:int(k)]); err != nil {
					return
				}
				_, _ = hash.Write(buf[:int(k)])
				remaining -= k
			}
		} else {
			if _, err := io.CopyN(hash, server, remaining); err != nil {
				return
			}
		}
		_ = writeAll(server, hash.Sum(nil))
	}()
	return client, true, nil
}

// DiagnosticTarget is used by active tasks to exercise the same SOCKS egress
// as application traffic, preserving domain names. It never directly dials
// Internet targets and does not modify Xray routing.
func DiagnosticTarget(ctx context.Context, cfg Config, host string, port int, useWARP bool) (*net.TCPConn, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target port")
	}
	if useWARP {
		return warpTCP(ctx, cfg, host, port)
	}
	return targetTCP(ctx, cfg, host, port)
}
func DiagnosticUDP(ctx context.Context, cfg Config, useWARP bool) (*net.TCPConn, *net.UDPConn, *net.UDPAddr, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, nil, err
	}
	if useWARP {
		return warpUDP(ctx, cfg)
	}
	return targetUDP(ctx, cfg)
}
