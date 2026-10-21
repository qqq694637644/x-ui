package reverseexit

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"x-ui/rsxdiag"
)

type fixtureConfig struct {
	Token               string `json:"token"`
	SOCKSPort           int    `json:"socks_port"`
	VisitorPort         int    `json:"visitor_port"`
	WARPPort            int    `json:"warp_port"`
	ProbePort           int    `json:"probe_port"`
	ExitUDP             int    `json:"exit_udp"`
	VisitorUDP          int    `json:"visitor_udp"`
	FrpcDiagnosticsPort int    `json:"frpc_diagnostics_port"`
	RealFRPC            bool   `json:"real_frpc"`
	LogsDir             string `json:"logs_dir"`
}
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 256<<10 {
		l.buf.Reset()
	}
	return l.buf.Write(p)
}
func (l *lockedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }
func startPythonFixture(t *testing.T, disabled bool) (fixtureConfig, func(), *lockedLog) {
	t.Helper()
	var cfg fixtureConfig
	root := os.Getenv("RSX_DIAG_NODE_DIR")
	if root == "" {
		t.Skip("RSX_DIAG_NODE_DIR must point at the matching nat_node checkout")
	}
	python := os.Getenv("RSX_DIAG_PYTHON")
	if python == "" {
		python = "python"
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, python, "-S", "-m", "tests.diagnostics_fixture")
	cmd.Dir = root
	cmd.Env = os.Environ()
	if disabled {
		cmd.Env = append(cmd.Env, "RSX_DIAG_NODE_DISABLED=1")
	}
	logs := &lockedLog{}
	cmd.Stderr = logs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_, _ = input.Write([]byte("{\"command\":\"stop\"}\n"))
			_ = input.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				cancel()
				<-done
			}
			cancel()
		})
	}
	t.Cleanup(stop)
	line := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 8192), 64<<10)
		if scanner.Scan() {
			line <- append([]byte(nil), scanner.Bytes()...)
		} else {
			line <- nil
		}
	}()
	select {
	case raw := <-line:
		if json.Unmarshal(raw, &cfg) != nil || cfg.Token == "" {
			t.Fatalf("fixture startup: %q logs=%s", raw, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Python fixture startup timeout: " + logs.String())
	}
	return cfg, stop, logs
}
func fixtureAgentConfig(f fixtureConfig) Config {
	cfg := DefaultConfig()
	cfg.Token = f.Token
	cfg.VisitorAddr = fmt.Sprintf("127.0.0.1:%d", f.VisitorPort)
	cfg.WARPAddr = fmt.Sprintf("127.0.0.1:%d", f.WARPPort)
	cfg.WARPProbe = fmt.Sprintf("127.0.0.1:%d", f.ProbePort)
	cfg.UDPBindAddr = fmt.Sprintf("127.0.0.1:%d", f.ExitUDP)
	cfg.UDPPeerAddr = fmt.Sprintf("127.0.0.1:%d", f.VisitorUDP)
	cfg.FRPCDiagnosticsAddr = fmt.Sprintf("127.0.0.1:%d", f.FrpcDiagnosticsPort)
	cfg.DiagnosticsEnabled = true
	cfg.RetryInterval = 250 * time.Millisecond
	return cfg
}
func waitDiagnosticPeer(t *testing.T, m *Manager, logs *lockedLog) *DiagnosticPeer {
	t.Helper()
	until := time.Now().Add(45 * time.Second)
	for time.Now().Before(until) {
		if p := m.DiagnosticPeer(); p != nil {
			return p
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("diagnostic peer not negotiated: %+v logs=%s", m.Status(), logs.String())
	return nil
}
func callDiagnostic(t *testing.T, p *DiagnosticPeer, method string, params interface{}, timeout time.Duration) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, err := p.Call(ctx, method, params)
	if err != nil {
		t.Fatalf("diagnostic %s failed: %v", method, err)
	}
	return raw
}
func requireVerified(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var v struct {
		State    string `json:"state"`
		Verified bool   `json:"verified"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(raw, &v) != nil || v.State != "ok" || !v.Verified {
		t.Fatalf("test did not actually verify data: %s", raw)
	}
}
func TestDiagnosticsGoPythonRPCAndRealFRPC(t *testing.T) {
	fixture, stop, logs := startPythonFixture(t, false)
	defer stop()
	cfg := fixtureAgentConfig(fixture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager()
	if err := manager.Apply(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	peer := waitDiagnosticPeer(t, manager, logs)
	raw := callDiagnostic(t, peer, "ping", nil, 5*time.Second)
	if !bytes.Contains(raw, []byte(rsxdiag.Schema)) {
		t.Fatal("missing capabilities")
	}
	raw = callDiagnostic(t, peer, "snapshot", nil, 5*time.Second)
	if !json.Valid(raw) || !bytes.Contains(raw, []byte("python")) {
		t.Fatal("Python stats missing")
	}
	// Benchmark goes through the actual Windows SOCKS5 -> RSX3 -> local
	// Visitor path. Reserved target bypass is permitted once by the manager.
	for _, direction := range []string{"download", "upload"} {
		testCtx, done := context.WithTimeout(ctx, 30*time.Second)
		result, err := peer.Benchmark(testCtx, 1_000_000, direction)
		done()
		if err != nil {
			t.Fatalf("%s benchmark: %v logs=%s", direction, err, logs.String())
		}
		requireVerified(t, result)
	}
	raw = callDiagnostic(t, peer, "socks_udp", nil, 8*time.Second)
	requireVerified(t, raw)
	raw = callDiagnostic(t, peer, "snapshot", nil, 5*time.Second)
	var state struct {
		Metrics struct {
			Attempts uint64 `json:"attempts"`
			Download uint64 `json:"download_bytes"`
		} `json:"metrics"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Metrics.Attempts < 2 || state.Metrics.Download < 1_000_000 {
		t.Fatalf("real byte counts missing: %s", raw)
	}
	// Bad HMAC/preface must not replace or kill the authenticated business control.
	bad, err := net.DialTimeout("tcp", cfg.VisitorAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.SetDeadline(time.Now().Add(time.Second))
	_, _ = bad.Write([]byte("R3G1" + strings.Repeat("z", 16)))
	_ = bad.Close()
	time.Sleep(20 * time.Millisecond)
	_ = callDiagnostic(t, peer, "ping", nil, 3*time.Second)
	if !fixture.RealFRPC {
		var v map[string]interface{}
		_ = json.Unmarshal(raw, &v)
		frpc, _ := v["frpc"].(map[string]interface{})
		if frpc["state"] != "unavailable" {
			t.Fatal("missing local frpc was falsely marked healthy")
		}
		return
	}
	local := rsxdiag.LocalClient{Addr: cfg.FRPCDiagnosticsAddr, Token: rsxdiag.LocalToken(cfg.Token)}
	for _, method := range []string{"quic_udp", "raw_benchmark"} {
		params := map[string]interface{}{}
		if method == "raw_benchmark" {
			params = map[string]interface{}{"direction": "download", "bytes": 1_000_000}
		}
		requireVerified(t, callDiagnostic(t, peer, method, params, 30*time.Second))
	}
	captureCtx, done := context.WithTimeout(ctx, 5*time.Second)
	_, err = local.Do(captureCtx, "POST", "/v1/capture", map[string]interface{}{"seconds": 30}, 1<<20)
	done()
	if err != nil {
		t.Fatal(err)
	}
	_ = callDiagnostic(t, peer, "capture_start", map[string]interface{}{"seconds": 30}, 5*time.Second)
	captureCtx, done = context.WithTimeout(ctx, 30*time.Second)
	result, err := peer.Benchmark(captureCtx, 1_000_000, "download")
	done()
	if err != nil {
		t.Fatal(err)
	}
	requireVerified(t, result)
	captureCtx, done = context.WithTimeout(ctx, 5*time.Second)
	_, err = local.Do(captureCtx, "DELETE", "/v1/capture", nil, 1<<20)
	done()
	if err != nil {
		t.Fatal(err)
	}
	_ = callDiagnostic(t, peer, "capture_stop", nil, 5*time.Second)
	captureCtx, done = context.WithTimeout(ctx, 10*time.Second)
	qlog, err := local.Do(captureCtx, "GET", "/v1/capture/data", nil, 48<<20)
	done()
	if err != nil || !bytes.Contains(qlog, []byte("transport:packet")) {
		t.Fatalf("offshore native qlog missing: %v", err)
	}
	manifestRaw := callDiagnostic(t, peer, "capture_manifest", nil, 25*time.Second)
	var manifest struct {
		ID    string `json:"id"`
		Bytes int    `json:"bytes"`
	}
	if json.Unmarshal(manifestRaw, &manifest) != nil || manifest.Bytes == 0 {
		t.Fatal("node raw report unavailable")
	}
	archive := []byte{}
	for len(archive) < manifest.Bytes {
		raw := callDiagnostic(t, peer, "capture_chunk", map[string]interface{}{"id": manifest.ID, "offset": len(archive)}, 5*time.Second)
		var chunk struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(raw, &chunk) != nil {
			t.Fatal("invalid chunk")
		}
		part, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || len(part) == 0 {
			t.Fatal("invalid chunk contents")
		}
		archive = append(archive, part...)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	foundQlog := false
	for _, f := range zipReader.File {
		if strings.HasSuffix(f.Name, ".sqlog") {
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("transport:packet")) {
				foundQlog = true
			}
		}
	}
	if !foundQlog {
		t.Fatal("node archive did not include real QUIC qlog")
	}
	t.Log("REAL_OFFICIAL_FRPS_069_BOTH_QUIC_METRICS_DIAGNOSTIC_RPC_TCP_UDP_BENCHMARK_CAPTURE_PASS")
}
func TestDiagnosticsRejectsPeerWithoutRequiredCapability(t *testing.T) {
	fixture, stop, logs := startPythonFixture(t, true)
	defer stop()
	cfg := fixtureAgentConfig(fixture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager()
	if err := m.Apply(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	// Deliberately mismatched Diagnostics cannot become online and must
	// reject application sessions rather than falling back to old forwarding.
	time.Sleep(14 * time.Second)
	if m.DiagnosticPeer() != nil {
		t.Fatal("unsupported diagnostics capability was treated as verified")
	}
	if m.Status().State == "online" {
		t.Fatal("mismatched diagnostics peer silently became online")
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fixture.SOCKSPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	_, _ = c.Write([]byte{5, 1, 0})
	var auth [2]byte
	if _, err := io.ReadFull(c, auth[:]); err != nil {
		t.Fatal(err)
	}
	address, err := encodeTarget("127.0.0.1", fixture.ProbePort)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write(append([]byte{5, 1, 0}, address...))
	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err == nil && reply[1] == 0 {
		t.Fatalf("unverified legacy-style node forwarded application data: %v %s", reply, logs.String())
	}
}
func TestUnauthorizedDiagnosticSyntheticTargetNeverDials(t *testing.T) {
	c := &rsx3Control{benchmarks: map[string]*benchmarkPermit{}}
	for _, target := range []string{rsxdiag.ID() + ".rsxdiag.invalid", "untrusted.rsxdiag.invalid"} {
		conn, handled, err := c.benchmarkTarget(target)
		if !handled || err == nil || conn != nil {
			t.Fatal("unauthorized target accepted")
		}
	}
	if _, handled, _ := c.benchmarkTarget("example.com"); handled {
		t.Fatal("ordinary target intercepted")
	}
}
func TestDiagnosticPeerRequestBounds(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p := &DiagnosticPeer{conn: a, epoch: "test", pending: map[string]chan diagnosticResponse{}, done: make(chan struct{})}
	for i := 0; i < 4; i++ {
		p.pending[strconv.Itoa(i)] = make(chan diagnosticResponse, 1)
	}
	if _, err := p.Call(context.Background(), "ping", nil); err == nil {
		t.Fatal("unbounded RPC accepted")
	}
	p.Close()
	if _, err := p.Call(context.Background(), "ping", nil); err == nil {
		t.Fatal("closed RPC accepted")
	}
}
func TestDiagnosticsFixtureRequiresKnownPath(t *testing.T) {
	if root := os.Getenv("RSX_DIAG_NODE_DIR"); root != "" {
		if _, err := os.Stat(filepath.Join(root, "tests", "diagnostics_fixture.py")); err != nil {
			t.Fatal(errors.New("wrong companion source checkout: " + err.Error()))
		}
	}
}
