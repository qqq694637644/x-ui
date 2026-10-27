package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"x-ui/logger"
	"x-ui/xray"
)

const reverseQUICSidecarConfigPath = "bin/reverse-quic-bridge.json"

type reverseQUICSidecarManager struct {
	sync.Mutex
	process   *xray.Process
	logLines  []string
	lastError string
}

var reverseQUICSidecar reverseQUICSidecarManager

var reverseQUICInteresting = []string{
	"reverse", "bridge", "hysteria", "quic", "brutal", "dialer",
	"worker", "mux", "failed", "error", "timeout", "refused",
	"reset", "disconnect",
}

func sanitizeReverseQUICLog(line string, cfg ReverseQUICBridgeConfig) string {
	for _, secret := range []string{cfg.Auth, cfg.CertificatePin, cfg.SOCKS5Username, cfg.SOCKS5Password} {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[REDACTED]")
		}
	}
	return line
}

func appendReverseQUICLogLocked(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	reverseQUICSidecar.logLines = append(reverseQUICSidecar.logLines, line)
	if len(reverseQUICSidecar.logLines) > 120 {
		reverseQUICSidecar.logLines = reverseQUICSidecar.logLines[len(reverseQUICSidecar.logLines)-120:]
	}
}

func collectReverseQUICLogLocked(cfg ReverseQUICBridgeConfig) {
	if reverseQUICSidecar.process == nil {
		return
	}
	logText := reverseQUICSidecar.process.GetResult()
	for _, raw := range strings.Split(logText, "\n") {
		line := sanitizeReverseQUICLog(strings.TrimSpace(raw), cfg)
		if line == "" {
			continue
		}
		appendReverseQUICLogLocked(line)
		lower := strings.ToLower(line)
		for _, key := range reverseQUICInteresting {
			if strings.Contains(lower, key) {
				// Reverse Xray logs are explicitly forwarded to the same
				// x-ui journal that 'x-ui -> 12' displays. Do not log
				// the generated configuration or authentication tokens.
				logger.Infof("reverse QUIC sidecar: %s", line)
				break
			}
		}
	}
}

func monitorReverseQUICSidecar(ctx context.Context, proc *xray.Process, cfg ReverseQUICBridgeConfig) {
	ticker := time.NewTicker(1100 * time.Millisecond)
	defer ticker.Stop()
	warpTicker := time.NewTicker(2 * time.Second)
	defer warpTicker.Stop()
	warpDown := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-warpTicker.C:
			reverseQUICSidecar.Lock()
			current := reverseQUICSidecar.process == proc && proc.IsRunning()
			reverseQUICSidecar.Unlock()
			if !current {
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			probeErr := checkSOCKS5(probeCtx, reverseQUICSOCKS{Address: cfg.TransportSOCKSAddress}, true)
			cancel()
			previouslyDown := warpDown
			warpDown, shouldRestart := reverseQUICWarpRecoveryTransition(warpDown, probeErr)
			if warpDown && !previouslyDown {
				logger.Warningf("reverse QUIC WARP SOCKS5 %s unavailable (UDP ASSOCIATE): %v", cfg.TransportSOCKSAddress, probeErr)
				reverseQUICSidecar.Lock()
				if reverseQUICSidecar.process == proc {
					appendReverseQUICLogLocked("WARP SOCKS5 " + cfg.TransportSOCKSAddress + " 掉线；QUIC/UDP 不允许直连兜底")
				}
				reverseQUICSidecar.Unlock()
			}
			if shouldRestart {
				logger.Infof("reverse QUIC WARP SOCKS5 %s restored; recycling isolated Bridge to renew QUIC session", cfg.TransportSOCKSAddress)
				reverseQUICSidecar.Lock()
				if reverseQUICSidecar.process == proc {
					appendReverseQUICLogLocked("WARP SOCKS5 已恢复，正在重启独立 Bridge 以重新建立 QUIC 会话")
				}
				reverseQUICSidecar.Unlock()
				if err := restartReverseQUICSidecarAfterWarpRecovery(proc); err != nil {
					logger.Warning("reverse QUIC Bridge recovery restart failed:", err)
				}
				return
			}
		case <-ticker.C:
			reverseQUICSidecar.Lock()
			if reverseQUICSidecar.process != proc {
				reverseQUICSidecar.Unlock()
				return
			}
			collectReverseQUICLogLocked(cfg)
			if !proc.IsRunning() {
				msg := "独立 Reverse QUIC Xray 进程意外退出"
				if err := proc.GetErr(); err != nil {
					msg += ": " + err.Error()
				}
				reverseQUICSidecar.lastError = msg
				appendReverseQUICLogLocked(msg)
				logger.Warning(msg)
				reverseQUICSidecar.Unlock()
				return
			}
			reverseQUICSidecar.Unlock()
		}
	}
}

// Xray 26.3.27's Hysteria2 UDP socket may not recover after the local
// SOCKS5 process is killed and restarted. An explicit Bridge-only restart is
// required on a detected WARP outage -> recovery transition.
func reverseQUICWarpRecoveryTransition(wasDown bool, probeErr error) (isDown, restartBridge bool) {
	if probeErr != nil {
		return true, false
	}
	return false, wasDown
}

func restartReverseQUICSidecarAfterWarpRecovery(previous *xray.Process) error {
	cfg, err := (&ReverseQUICBridgeService{}).load()
	if err != nil || !cfg.Enabled {
		return err
	}
	return applyReverseQUICSidecarWithOptions(cfg, true, previous)
}

func stopReverseQUICSidecarLocked(cfg ReverseQUICBridgeConfig) error {
	if reverseQUICSidecar.process == nil {
		return nil
	}
	collectReverseQUICLogLocked(cfg)
	if reverseQUICSidecar.process.IsRunning() {
		if err := reverseQUICSidecar.process.Stop(); err != nil {
			return err
		}
	}
	collectReverseQUICLogLocked(cfg)
	reverseQUICSidecar.process = nil
	return nil
}

// applyReverseQUICSidecar validates a complete independent Xray configuration
// before disrupting the running sidecar, and rolls back the prior process if
// a new child exits unexpectedly on startup. It never restarts x-ui's main Xray.
func applyReverseQUICSidecar(cfg ReverseQUICBridgeConfig) error {
	return applyReverseQUICSidecarWithOptions(cfg, false, nil)
}

func applyReverseQUICSidecarWithOptions(cfg ReverseQUICBridgeConfig, forceRestart bool, expectedProcess *xray.Process) error {
	reverseQUICSidecar.Lock()
	defer reverseQUICSidecar.Unlock()
	// A save or restart may have replaced this process since the health probe.
	// Do not interrupt a newly configured Bridge with a stale recovery event.
	if expectedProcess != nil && reverseQUICSidecar.process != expectedProcess {
		return nil
	}

	if !cfg.Enabled {
		if err := stopReverseQUICSidecarLocked(cfg); err != nil {
			return fmt.Errorf("关闭独立反向 Bridge 失败: %w", err)
		}
		reverseQUICSidecar.lastError = ""
		appendReverseQUICLogLocked("QUIC Bridge 已停用，主 Xray 和旧 FRP 没有改变")
		return nil
	}
	next, err := BuildIsolatedReverseQUICConfig(cfg)
	if err != nil {
		return err
	}
	if err := xray.ValidateConfig(next); err != nil {
		return fmt.Errorf("独立 QUIC Bridge 配置校验失败: %w", err)
	}
	old := reverseQUICSidecar.process
	if !forceRestart && old != nil && old.IsRunning() && old.GetConfig().Equals(next) {
		return nil
	}
	var previous *xray.Config
	if old != nil && old.IsRunning() {
		previous = old.GetConfig()
	}
	if err := stopReverseQUICSidecarLocked(cfg); err != nil {
		return fmt.Errorf("停止旧反向进程失败（未覆盖主 Xray）: %w", err)
	}
	created := xray.NewIsolatedProcess(next, reverseQUICSidecarConfigPath)
	if err := created.Start(); err != nil {
		reverseQUICSidecar.lastError = sanitizeReverseQUICLog(err.Error(), cfg)
		appendReverseQUICLogLocked("独立 QUIC Bridge 启动失败: " + reverseQUICSidecar.lastError)
		if previous != nil {
			rollback := xray.NewIsolatedProcess(previous, reverseQUICSidecarConfigPath)
			if restoreErr := rollback.Start(); restoreErr == nil {
				reverseQUICSidecar.process = rollback
				go monitorReverseQUICSidecar(context.Background(), rollback, cfg)
				return fmt.Errorf("独立 QUIC Bridge 启动失败，已恢复之前运行的配置: %w", err)
			} else {
				return fmt.Errorf("独立 QUIC Bridge 启动失败: %w；恢复旧进程也失败: %v", err, restoreErr)
			}
		}
		return err
	}
	reverseQUICSidecar.process = created
	reverseQUICSidecar.lastError = ""
	appendReverseQUICLogLocked("独立 QUIC Bridge Xray 已启动，QUIC UDP 通过 WARP SOCKS5 " + cfg.TransportSOCKSAddress + " 发往 " + cfg.PublicHost)
	logger.Infof("reverse QUIC sidecar started; WARP SOCKS5=%s; endpoint=%s UDP/%d; egress=%s",
		cfg.TransportSOCKSAddress, cfg.PublicHost, cfg.PublicPort, cfg.EgressMode)
	go monitorReverseQUICSidecar(context.Background(), created, cfg)
	return nil
}

func restartReverseQUICSidecarFromSettings() error {
	cfg, err := (&ReverseQUICBridgeService{}).load()
	if err != nil {
		return err
	}
	return applyReverseQUICSidecar(cfg)
}

func StartReverseQUICSidecar() error {
	return restartReverseQUICSidecarFromSettings()
}

func StopReverseQUICSidecar() error {
	cfg, _ := (&ReverseQUICBridgeService{}).load()
	reverseQUICSidecar.Lock()
	defer reverseQUICSidecar.Unlock()
	if err := stopReverseQUICSidecarLocked(cfg); err != nil {
		return err
	}
	return nil
}

func EnsureReverseQUICSidecar() error {
	cfg, err := (&ReverseQUICBridgeService{}).load()
	if err != nil || !cfg.Enabled {
		return err
	}
	reverseQUICSidecar.Lock()
	running := reverseQUICSidecar.process != nil && reverseQUICSidecar.process.IsRunning()
	reverseQUICSidecar.Unlock()
	if running {
		return nil
	}
	return applyReverseQUICSidecar(cfg)
}

func reverseQUICSidecarSnapshot() (*xray.Process, string, []string) {
	reverseQUICSidecar.Lock()
	defer reverseQUICSidecar.Unlock()
	cfg, err := (&ReverseQUICBridgeService{}).load()
	if err == nil {
		collectReverseQUICLogLocked(cfg)
	}
	lines := append([]string(nil), reverseQUICSidecar.logLines...)
	return reverseQUICSidecar.process, reverseQUICSidecar.lastError, lines
}

func (s *ReverseQUICBridgeService) Diagnostics() ([]string, error) {
	cfg, err := s.Get()
	if err != nil {
		return nil, err
	}
	reverseQUICSidecar.Lock()
	defer reverseQUICSidecar.Unlock()
	collectReverseQUICLogLocked(cfg)
	lines := append([]string(nil), reverseQUICSidecar.logLines...)
	for i := range lines {
		lines[i] = sanitizeReverseQUICLog(lines[i], cfg)
	}
	if len(lines) == 0 {
		return []string{"暂无独立 QUIC Bridge 日志。请先启用并保存配置。"}, nil
	}
	return lines, nil
}

// No external target hosts are accepted by this diagnostics endpoint.
var errReverseQUICSidecarNotRunning = errors.New("独立 QUIC Bridge Xray 未运行")
