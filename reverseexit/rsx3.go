package reverseexit

// RSX3 uses one authenticated control stream for session lifecycle/UDP and
// a separate authenticated XTCP QUIC stream for each SOCKS5 TCP connection.
// Each new TCP data stream is a loopback dial of the SAME frpc Visitor port,
// not a new direct/public UDP dial. Final target egress optionally goes via
// a distinct loopback Xray SOCKS5 inbound for per-domain routing.
import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"x-ui/rsxdiag"
)

var (
	rsx3ControlPreface = []byte("RSX3")
	rsx3DataPreface    = []byte("R3D1")
	rsx3ControlDomain  = []byte("RSX3-control-v1")
	rsx3DataDomain     = []byte("RSX3-data-v1")
)

type rsx3Control struct {
	agent         *agent
	epoch         [16]byte
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	pending       map[uint32]context.CancelFunc
	wg            sync.WaitGroup
	diagNonce     string
	diagOnce      sync.Once
	diagReadyOnce sync.Once
	diagReady     chan struct{}
	diagMismatch  atomic.Bool
	status        func(string, string)
	benchMu       sync.Mutex
	benchmarks    map[string]*benchmarkPermit
}

func rsx3AuthenticateControl(c net.Conn, secret []byte) ([16]byte, error) {
	var epoch [16]byte
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	defer c.SetDeadline(time.Time{})
	if err := writeAll(c, rsx3ControlPreface); err != nil {
		return epoch, err
	}
	var challenge [32]byte
	if _, err := io.ReadFull(c, challenge[:]); err != nil {
		return epoch, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(rsx3ControlDomain)
	_, _ = mac.Write(challenge[:])
	if err := writeAll(c, mac.Sum(nil)); err != nil {
		return epoch, err
	}
	var response [17]byte
	if _, err := io.ReadFull(c, response[:]); err != nil {
		return epoch, err
	}
	if response[0] != 0 {
		return epoch, errors.New("RSX3 control authentication refused")
	}
	copy(epoch[:], response[1:])
	return epoch, nil
}

func rsx3AuthenticateData(c net.Conn, secret []byte, epoch [16]byte, sid uint32) error {
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	defer c.SetDeadline(time.Time{})
	var request [24]byte
	copy(request[:4], rsx3DataPreface)
	copy(request[4:20], epoch[:])
	binary.BigEndian.PutUint32(request[20:], sid)
	if err := writeAll(c, request[:]); err != nil {
		return err
	}
	var challenge [32]byte
	if _, err := io.ReadFull(c, challenge[:]); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(rsx3DataDomain)
	_, _ = mac.Write(epoch[:])
	_, _ = mac.Write(request[20:])
	_, _ = mac.Write(challenge[:])
	if err := writeAll(c, mac.Sum(nil)); err != nil {
		return err
	}
	var ok [1]byte
	if _, err := io.ReadFull(c, ok[:]); err != nil {
		return err
	}
	if ok[0] != 0 {
		return errors.New("RSX3 data authentication refused")
	}
	return nil
}

func serveRSX3(ctx context.Context, c net.Conn, cfg Config, status func(string, string)) error {
	defer c.Close()
	authStart := time.Now()
	epoch, err := rsx3AuthenticateControl(c, []byte(cfg.Token))
	if err != nil {
		return fmt.Errorf("RSX3 control authentication: %w", err)
	}
	log.Printf("RSX3 metric control_auth_ms=%d", time.Since(authStart).Milliseconds())

	bind, _ := net.ResolveUDPAddr("udp4", cfg.UDPBindAddr)
	peer, _ := net.ResolveUDPAddr("udp4", cfg.UDPPeerAddr)
	udpConn, err := net.ListenUDP("udp4", bind)
	if err != nil {
		return fmt.Errorf("RSX3 local datagram socket: %w", err)
	}
	defer udpConn.Close()
	a := &agent{conn: c, udpConn: udpConn, udpPeer: peer, cfg: cfg,
		streams: make(map[uint32]stream)}
	a.sender = newFairWriter(c)
	defer a.sender.close()
	defer a.stopAll()
	controlCtx, cancel := context.WithCancel(ctx)
	control := &rsx3Control{agent: a, epoch: epoch, ctx: controlCtx, cancel: cancel,
		pending: make(map[uint32]context.CancelFunc), benchmarks: make(map[string]*benchmarkPermit), status: status}
	defer func() {
		cancel()
		control.mu.Lock()
		for _, stop := range control.pending {
			stop()
		}
		control.mu.Unlock()
		control.wg.Wait()
	}()
	go a.datagramLoop()
	if cfg.DiagnosticsEnabled {
		status("diagnostics-negotiating", "require matched RSX3 Diagnostics peer")
		control.diagReady = make(chan struct{})
		control.diagNonce = rsxdiag.ID()
		go func() { _ = a.send(frame{ping, 0, []byte(diagCapabilityRequest + control.diagNonce)}) }()
		go func() {
			select {
			case <-control.diagReady:
				return
			case <-time.After(12 * time.Second):
				control.diagMismatch.Store(true)
				status("error", "RSX3 Diagnostics version/capability mismatch: require matching Windows Python and both frpc versions")
				_ = c.Close()
			case <-controlCtx.Done():
			}
		}()
	} else {
		status("online", "")
	}
	go func() {
		for controlCtx.Err() == nil {
			status("warp-checking", "")
			if err := probeWARP(controlCtx, cfg); err != nil {
				if controlCtx.Err() == nil {
					status("warp-error", err.Error())
				}
			} else {
				status("warp-ready", "")
			}
			select {
			case <-controlCtx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
	// Ensure a panel stop or frpc restart interrupts blocking readFrame.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-controlCtx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	for {
		f, err := readFrame(c)
		if err != nil {
			if control.diagMismatch.Load() {
				return fmt.Errorf("RSX3 Diagnostics version/capability mismatch (check Windows Python, VPS Agent and both frpc): %w", err)
			}
			return err
		}
		switch f.kind {
		case openTCP:
			if cfg.DiagnosticsEnabled {
				select {
				case <-control.diagReady:
				default:
					_ = a.send(frame{tcpResult, f.id, []byte{5}})
					continue
				}
			}
			host, port, used, err := decodeAddress(f.data)
			if err != nil || used != len(f.data) || f.id == 0 {
				_ = a.send(frame{tcpResult, f.id, []byte{5}})
				continue
			}
			control.mu.Lock()
			if len(control.pending) >= maxSessions || control.pending[f.id] != nil {
				control.mu.Unlock()
				_ = a.send(frame{tcpResult, f.id, []byte{5}})
				continue
			}
			jobCtx, cancelJob := context.WithCancel(controlCtx)
			control.pending[f.id] = cancelJob
			control.wg.Add(1)
			control.mu.Unlock()
			go func(sid uint32) {
				defer control.wg.Done()
				defer cancelJob()
				defer func() {
					control.mu.Lock()
					delete(control.pending, sid)
					control.mu.Unlock()
				}()
				control.handleTCP(jobCtx, sid, host, port)
			}(f.id)
		case tcpRST:
			control.mu.Lock()
			if stop := control.pending[f.id]; stop != nil {
				stop()
			}
			control.mu.Unlock()
		case openUDP:
			if cfg.DiagnosticsEnabled {
				select {
				case <-control.diagReady:
				default:
					_ = a.send(frame{udpResult, f.id, []byte{5}})
					continue
				}
			}
			if len(f.data) != 0 {
				continue
			}
			s := &udpStream{agent: a, id: f.id, in: make(chan []byte, 128),
				done: make(chan struct{})}
			if a.register(f.id, s) {
				go s.run()
			}
		case udpClose:
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
		case pong:
			control.diagnosticsPong(f.data)
		default:
			return fmt.Errorf("RSX3 invalid control frame kind %d", f.kind)
		}
	}
}

func (control *rsx3Control) handleTCP(ctx context.Context, sid uint32, host string, port int) {
	started := time.Now()
	span := control.agent.cfg.Meter.Open(hex.EncodeToString(control.epoch[:]), sid, net.JoinHostPort(host, fmt.Sprint(port)))
	var finalError error
	finalOperation := "open_tcp"
	defer func() { span.Finish(finalOperation, finalError) }()
	beginTarget := time.Now()
	target, synthetic, err := control.benchmarkTarget(host)
	if !synthetic {
		target, err = targetTCP(ctx, control.agent.cfg, host, port)
	}
	targetMS := time.Since(beginTarget).Milliseconds()
	if err != nil {
		log.Printf("RSX3 metric tcp_error sid=%d step=target_socks_connect target_ms=%d type=%T", sid, targetMS, err)
		finalOperation = "target_socks_connect"
		finalError = err
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		return
	}
	defer target.Close()

	beginDial := time.Now()
	dialer := net.Dialer{Timeout: 8 * time.Second}
	visitor, err := dialer.DialContext(ctx, "tcp", control.agent.cfg.VisitorAddr)
	dialMS := time.Since(beginDial).Milliseconds()
	if err != nil {
		log.Printf("RSX3 metric tcp_error sid=%d step=visitor_stream target_ms=%d visitor_ms=%d type=%T",
			sid, targetMS, dialMS, err)
		finalOperation = "visitor_stream"
		finalError = err
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		return
	}
	defer visitor.Close()
	authStart := time.Now()
	err = rsx3AuthenticateData(visitor, []byte(control.agent.cfg.Token), control.epoch, sid)
	authMS := time.Since(authStart).Milliseconds()
	log.Printf("RSX3 metric open_tcp sid=%d target_ms=%d visitor_ms=%d data_auth_ms=%d ready=%t",
		sid, targetMS, dialMS, authMS, err == nil)
	if err != nil {
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		finalOperation = "data_auth"
		finalError = err
		return
	}
	span.Ready(time.Duration(targetMS)*time.Millisecond, time.Duration(dialMS)*time.Millisecond, time.Duration(authMS)*time.Millisecond)
	span.Endpoints(visitor, target)
	if span != nil {
		lookupAt := time.Now().UTC().Format(time.RFC3339Nano)
		go func() {
			queryCtx, done := context.WithTimeout(ctx, time.Second)
			defer done()
			local := rsxdiag.LocalClient{Addr: control.agent.cfg.FRPCDiagnosticsAddr, Token: rsxdiag.LocalToken(control.agent.cfg.Token)}
			if link, err := local.Lookup(queryCtx, visitor.LocalAddr().String(), visitor.RemoteAddr().String(), lookupAt); err == nil {
				span.SetLink(link)
			}
		}()
	}

	// A control-side cancel aborts both directions, including idle target TCP.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = visitor.Close()
			_ = target.Close()
		case <-finished:
		}
	}()
	var upstream, downstream atomic.Uint64
	type outcome struct {
		err       error
		direction string
	}
	out := make(chan outcome, 2)
	pump := func(src, dst net.Conn, count *atomic.Uint64, direction string) {
		n, copyErr := span.Copy(dst, src, direction)
		count.Add(uint64(n))
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		out <- outcome{copyErr, direction}
	}
	go pump(visitor, target, &upstream, "upload")
	go pump(target, visitor, &downstream, "download")
	first := <-out
	if first.err != nil {
		finalError = first.err
		finalOperation = first.direction
		// Avoid waiting forever on a peer after a hard I/O failure.
		_ = visitor.Close()
		_ = target.Close()
	}
	second := <-out
	if finalError == nil {
		finalError = second.err
		finalOperation = second.direction
	}
	log.Printf("RSX3 metric tcp_summary sid=%d upload_bytes=%d download_bytes=%d duration_ms=%d",
		sid, upstream.Load(), downstream.Load(), time.Since(started).Milliseconds())
}
