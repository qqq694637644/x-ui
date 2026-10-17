package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"x-ui/reverseexit"
)

const (
	defaultFRPCBinary = "/opt/customfrp/frpc_linux_amd64"
	defaultFRPCConfig = "/etc/x-ui/reverse-socks/frpc.toml"
	frpcManagedMarker = "# Managed by x-ui reverse SOCKS5. Changes are overwritten on save."
)

type reverseFRPCConfig struct {
	FRPCBinaryPath string `json:"frpcBinaryPath"`
	FRPCConfigPath string `json:"frpcConfigPath"`
	FRPSServerAddr string `json:"frpsServerAddr"`
	FRPSServerPort int    `json:"frpsServerPort"`
	FRPSToken      string `json:"frpsToken"`
	XTCPServerName string `json:"xtcpServerName"`
	XTCPSecretKey  string `json:"xtcpSecretKey"`
	STUNServer     string `json:"stunServer"`
}

func defaultReverseFRPCConfig() reverseFRPCConfig {
	return reverseFRPCConfig{
		FRPCBinaryPath: defaultFRPCBinary,
		FRPCConfigPath: defaultFRPCConfig,
		FRPSServerPort: 7000,
		XTCPServerName: "rsx2_node",
	}
}

func (c reverseFRPCConfig) validatePaths() error {
	if err := validManagedPath(c.FRPCBinaryPath, "frpc executable"); err != nil {
		return err
	}
	if err := validManagedPath(c.FRPCConfigPath, "frpc config"); err != nil {
		return err
	}
	if filepath.Clean(c.FRPCBinaryPath) == filepath.Clean(c.FRPCConfigPath) {
		return fmt.Errorf("frpc binary and config file cannot be identical")
	}
	if filepath.Ext(c.FRPCConfigPath) != ".toml" {
		return fmt.Errorf("frpc config path must end in .toml")
	}
	return nil
}

func validManagedPath(path, name string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s must be a clean absolute path", name)
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return fmt.Errorf("%s contains invalid characters", name)
	}
	return nil
}

func (c reverseFRPCConfig) validate() error {
	if err := c.validatePaths(); err != nil {
		return err
	}
	if err := validateFRPCBinary(c.FRPCBinaryPath); err != nil {
		return err
	}
	if c.FRPSServerAddr == "" || strings.ContainsAny(c.FRPSServerAddr, "/ \r\n\t") {
		return fmt.Errorf("frps server address must be an IP or hostname without a port")
	}
	if c.FRPSServerPort < 1 || c.FRPSServerPort > 65535 {
		return fmt.Errorf("frps server port must be 1-65535")
	}
	if c.FRPSToken == "" || c.XTCPSecretKey == "" {
		return fmt.Errorf("frps authentication token and XTCP secret key are required")
	}
	if len(c.FRPSToken) > 4096 || len(c.XTCPSecretKey) > 4096 {
		return fmt.Errorf("frps token or XTCP secret is too long")
	}
	if c.XTCPServerName == "" || strings.ContainsAny(c.XTCPServerName, " \r\n\t") {
		return fmt.Errorf("XTCP server name must match the mainland proxy name")
	}
	return validateSTUNServer(c.STUNServer)
}

// STUN accepts an IPv4 literal OR a DNS hostname. This is a syntax check
// only: validation must never trigger an unexpected host-side DNS request.
// For a hostname the frpc resolver may later make a normal DNS query,
// independently from the SOCKS5 UDP relay used for STUN traffic.
func validateSTUNServer(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("STUN server must be a hostname or IPv4 address with port (e.g. stun.miwifi.com:3478)")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("STUN server requires a port between 1 and 65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return nil
		}
		return fmt.Errorf("STUN server currently requires an IPv4 address or DNS hostname")
	}
	if len(host) > 253 {
		return fmt.Errorf("STUN hostname is too long")
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) == 0 {
		return fmt.Errorf("invalid STUN hostname")
	}
	numericOnly := true
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid STUN hostname %q", host)
		}
		for _, ch := range label {
			switch {
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
				numericOnly = false
			case ch >= '0' && ch <= '9':
			case ch == '-':
				numericOnly = false
			default:
				return fmt.Errorf("invalid STUN hostname %q", host)
			}
		}
	}
	if numericOnly {
		return fmt.Errorf("invalid STUN IPv4 address %q", host)
	}
	return nil
}

func validateFRPCBinary(path string) error {
	if err := validManagedPath(path, "frpc executable"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("frpc executable %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("frpc executable is not a regular file: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("frpc executable is not executable: %s", path)
	}
	return nil
}

func renderManagedFRPC(c reverseFRPCConfig, agent reverseexit.Config) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	if err := agent.Validate(); err != nil {
		return "", err
	}
	_, visitorPort, _ := net.SplitHostPort(agent.VisitorAddr)
	// When WARP has credentials, frpc is explicitly given the same local
	// SOCKS5 identity as the Go exit Agent. No direct fallback is generated.
	u := url.URL{Scheme: "socks5", Host: agent.WARPAddr}
	if agent.WARPUsername != "" {
		u.User = url.UserPassword(agent.WARPUsername, agent.WARPPassword)
	}
	socks := u.String()
	quoted := strconv.Quote
	var b strings.Builder
	for _, line := range []string{
		frpcManagedMarker,
		"# Private XTCP Visitor; requires the custom frpc with natTraversal.udpProxyURL",
		"serverAddr = " + quoted(c.FRPSServerAddr),
		"serverPort = " + strconv.Itoa(c.FRPSServerPort),
		"auth.method = \"token\"",
		"auth.token = " + quoted(c.FRPSToken),
		"transport.protocol = \"tcp\"",
		"transport.wireProtocol = \"v1\"",
		"transport.proxyURL = " + quoted(socks),
		"natHoleSTUNServer = " + quoted(c.STUNServer),
		"",
		"[[visitors]]",
		"name = \"rsx2_outside\"",
		"type = \"xtcp\"",
		"serverName = " + quoted(c.XTCPServerName),
		"secretKey = " + quoted(c.XTCPSecretKey),
		"protocol = \"quic\"",
		"keepTunnelOpen = true",
		"minRetryInterval = 10",
		"maxRetriesAnHour = 120",
		"bindAddr = \"127.0.0.1\"",
		"bindPort = " + visitorPort,
		"",
		"[visitors.datagram]",
		"listenAddr = " + quoted(agent.UDPPeerAddr),
		"peerAddr = " + quoted(agent.UDPBindAddr),
		"",
		"[visitors.natTraversal]",
		"disableAssistedAddrs = true",
		"udpProxyURL = " + quoted(socks),
		"",
	} {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// stageFRPCConfig writes only a PRIVATE temporary file in the final directory.
// The real custom frpc must accept "verify -c <temporary.toml>" before that
// file can replace the existing managed configuration. No generic frpc binary
// or legacy TOML parsing fallback is attempted.
func stageFRPCConfig(ctx context.Context, cfg reverseFRPCConfig, content string) (string, error) {
	if err := cfg.validatePaths(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.FRPCConfigPath), 0700); err != nil {
		return "", fmt.Errorf("create frpc config directory: %w", err)
	}
	// Never overwrite a pre-existing unrelated config, symlink or directory.
	if info, err := os.Lstat(cfg.FRPCConfigPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("refusing to overwrite non-regular frpc config file")
		}
		original, err := os.ReadFile(cfg.FRPCConfigPath)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(string(original), frpcManagedMarker+"\n") {
			return "", fmt.Errorf("refusing to overwrite frpc config not created by x-ui")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(cfg.FRPCConfigPath), ".xui-frpc-*.toml")
	if err != nil {
		return "", fmt.Errorf("create frpc staging file: %w", err)
	}
	temp := f.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(temp)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return "", err
	}
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Do not return output: frpc may echo sensitive token or proxy auth values.
	cmd := exec.CommandContext(timeoutCtx, cfg.FRPCBinaryPath, "verify", "-c", temp)
	if err := cmd.Run(); err != nil {
		if timeoutCtx.Err() != nil {
			return "", fmt.Errorf("custom frpc verify timed out: %w", timeoutCtx.Err())
		}
		return "", fmt.Errorf("custom frpc verify failed (%v); check binary and TOML options", err)
	}
	// A previous custom frpc (or stock frpc) can accept TOML while silently
	// ignoring unknown natTraversal.udpProxyURL fields. Assert the binary
	// actively REJECTS both deliberately unsafe variants before launch.
	// Without this negative capability test the VPS public UDP address could
	// leak despite a superficially successful 'frpc verify'.
	for _, forbidden := range []string{"udpProxyURL = ", "transport.proxyURL = "} {
		lines := strings.Split(content, "\n")
		deleted := false
		for i, line := range lines {
			if strings.HasPrefix(line, forbidden) {
				lines = append(lines[:i], lines[i+1:]...)
				deleted = true
				break
			}
		}
		if !deleted {
			return "", fmt.Errorf("missing mandatory field in generated frpc config: %s", forbidden)
		}
		probe, err := os.CreateTemp(filepath.Dir(cfg.FRPCConfigPath), ".xui-frpc-negative-*.toml")
		if err != nil {
			return "", err
		}
		probeName := probe.Name()
		if err := probe.Chmod(0600); err != nil {
			_ = probe.Close()
			_ = os.Remove(probeName)
			return "", err
		}
		_, writeErr := io.WriteString(probe, strings.Join(lines, "\n"))
		closeErr := probe.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(probeName)
			return "", fmt.Errorf("cannot write frpc negative verification fixture")
		}
		verify := exec.CommandContext(timeoutCtx, cfg.FRPCBinaryPath, "verify", "-c", probeName)
		verifyErr := verify.Run()
		_ = os.Remove(probeName)
		if timeoutCtx.Err() != nil {
			return "", fmt.Errorf("custom frpc privacy verification timed out: %w", timeoutCtx.Err())
		}
		if verifyErr == nil {
			return "", fmt.Errorf("custom frpc binary accepts unsafe %s omission; update to the private XTCP UDP-capable frpc build", strings.TrimSpace(forbidden))
		}
	}
	success = true
	return temp, nil
}

func activateFRPCConfig(cfg reverseFRPCConfig, temp string) error {
	if temp == "" {
		return fmt.Errorf("missing verified frpc staging config")
	}
	// The existing destination was already checked by stageFRPCConfig. Check
	// it again before rename to reject an intervening symlink replacement.
	if info, err := os.Lstat(cfg.FRPCConfigPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("frpc config destination changed to a non-regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temp, cfg.FRPCConfigPath); err != nil {
		return fmt.Errorf("activate verified frpc config: %w", err)
	}
	return os.Chmod(cfg.FRPCConfigPath, 0600)
}

// A foreign frpc might already be bound to this Visitor port. Refuse to
// start an owned frpc (and especially the Go Agent) in that case: otherwise
// the Agent could accidentally connect to a different XTCP Visitor.
func ensureVisitorPortAvailable(addr string) error {
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return fmt.Errorf("XTCP Visitor TCP port %s is in use by another process: %w", addr, err)
	}
	return listener.Close()
}

// frpcRuntime represents only the child process spawned by this x-ui instance.
// PID is observational; no process is ever killed by searching the system.
type frpcRuntime struct {
	State string `json:"state"`
	PID   int    `json:"pid"`
	Error string `json:"error"`
}

type managedFRPC struct {
	control  sync.Mutex
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
	status   frpcRuntime
	onExit   func()
}

func newManagedFRPC(onExit func()) *managedFRPC {
	return &managedFRPC{status: frpcRuntime{State: "stopped"}, onExit: onExit}
}

func (p *managedFRPC) Status() frpcRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *managedFRPC) Start(binary, configPath string) error {
	p.control.Lock()
	defer p.control.Unlock()
	p.mu.Lock()
	if p.cmd != nil {
		p.mu.Unlock()
		return fmt.Errorf("managed frpc is already running")
	}
	p.mu.Unlock()
	if err := validateFRPCBinary(binary); err != nil {
		return err
	}
	cmd := exec.Command(binary, "-c", configPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		p.mu.Lock()
		p.status = frpcRuntime{State: "error", Error: fmt.Sprintf("cannot start managed frpc: %v", err)}
		p.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	p.mu.Lock()
	p.cmd, p.done, p.stopping = cmd, done, false
	p.status = frpcRuntime{State: "running", PID: cmd.Process.Pid}
	p.mu.Unlock()
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		stopping := p.stopping
		if p.cmd == cmd {
			p.cmd = nil
			if stopping {
				p.status = frpcRuntime{State: "stopped"}
			} else {
				description := "custom frpc stopped unexpectedly"
				if err != nil {
					description = fmt.Sprintf("custom frpc exited: %v", err)
				}
				p.status = frpcRuntime{State: "error", Error: description}
			}
		}
		close(done)
		p.mu.Unlock()
		if !stopping && p.onExit != nil {
			p.onExit()
		}
	}()
	return nil
}

func (p *managedFRPC) Stop() {
	p.control.Lock()
	defer p.control.Unlock()
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	if cmd == nil {
		p.status = frpcRuntime{State: "stopped"}
		p.mu.Unlock()
		return
	}
	p.stopping = true
	p.mu.Unlock()

	// POSIX SIGTERM gives quic-go a chance to release its UDP sockets;
	// Windows does not support signaling arbitrary child processes.
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-done
	}
	p.mu.Lock()
	p.status = frpcRuntime{State: "stopped"}
	p.mu.Unlock()
}
