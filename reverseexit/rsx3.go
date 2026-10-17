package reverseexit

// RSX3 uses one authenticated control stream for session lifecycle/UDP and
// a separate authenticated XTCP QUIC stream for each SOCKS5 TCP connection.
// Each new TCP data stream is a loopback dial of the SAME frpc Visitor port,
// not a new direct/public UDP dial. All Internet egress remains WARP SOCKS5.
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
	"sync/atomic"
	"time"
)

var (
	rsx3ControlPreface = []byte("RSX3")
	rsx3DataPreface    = []byte("R3D1")
	rsx3ControlDomain  = []byte("RSX3-control-v1")
	rsx3DataDomain     = []byte("RSX3-data-v1")
)

type rsx3Control struct {
	agent   *agent
	epoch   [16]byte
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending map[uint32]context.CancelFunc
	wg      sync.WaitGroup
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
		pending: make(map[uint32]context.CancelFunc)}
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
	status("online", "")
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
			return err
		}
		switch f.kind {
		case openTCP:
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
		default:
			return fmt.Errorf("RSX3 invalid control frame kind %d", f.kind)
		}
	}
}

func (control *rsx3Control) handleTCP(ctx context.Context, sid uint32, host string, port int) {
	started := time.Now()
	beginWARP := time.Now()
	warp, err := warpTCP(ctx, control.agent.cfg, host, port)
	warpMS := time.Since(beginWARP).Milliseconds()
	if err != nil {
		log.Printf("RSX3 metric tcp_error sid=%d step=warp_connect warp_ms=%d type=%T", sid, warpMS, err)
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		return
	}
	defer warp.Close()

	beginDial := time.Now()
	dialer := net.Dialer{Timeout: 8 * time.Second}
	visitor, err := dialer.DialContext(ctx, "tcp", control.agent.cfg.VisitorAddr)
	dialMS := time.Since(beginDial).Milliseconds()
	if err != nil {
		log.Printf("RSX3 metric tcp_error sid=%d step=visitor_stream warp_ms=%d visitor_ms=%d type=%T",
			sid, warpMS, dialMS, err)
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		return
	}
	defer visitor.Close()
	authStart := time.Now()
	err = rsx3AuthenticateData(visitor, []byte(control.agent.cfg.Token), control.epoch, sid)
	authMS := time.Since(authStart).Milliseconds()
	log.Printf("RSX3 metric open_tcp sid=%d warp_ms=%d visitor_ms=%d data_auth_ms=%d ready=%t",
		sid, warpMS, dialMS, authMS, err == nil)
	if err != nil {
		_ = control.agent.send(frame{tcpResult, sid, []byte{5}})
		return
	}

	// A control-side cancel aborts both directions, including idle WARP TCP.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = visitor.Close()
			_ = warp.Close()
		case <-finished:
		}
	}()
	var upstream, downstream atomic.Uint64
	type outcome struct{ err error }
	out := make(chan outcome, 2)
	pump := func(src, dst net.Conn, count *atomic.Uint64) {
		buf := make([]byte, 64*1024)
		n, copyErr := io.CopyBuffer(dst, src, buf)
		count.Add(uint64(n))
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		out <- outcome{copyErr}
	}
	go pump(visitor, warp, &upstream)
	go pump(warp, visitor, &downstream)
	first := <-out
	if first.err != nil {
		// Avoid waiting forever on a peer after a hard I/O failure.
		_ = visitor.Close()
		_ = warp.Close()
	}
	<-out
	log.Printf("RSX3 metric tcp_summary sid=%d upload_bytes=%d download_bytes=%d duration_ms=%d",
		sid, upstream.Load(), downstream.Load(), time.Since(started).Milliseconds())
}
