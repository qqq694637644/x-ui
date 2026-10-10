package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"x-ui/database/model"
	"x-ui/xray"
)

const endpointHealthCheckTimeout = 20 * time.Second

func checkVLESSXHTTPEndpointHealth(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error {
	spec, err := validateManagedInbound(inbound)
	if err != nil {
		return err
	}
	if endpoint == nil {
		return fmt.Errorf("public endpoint is required")
	}
	proxyPort, err := reserveLocalPort()
	if err != nil {
		return fmt.Errorf("reserve health-check proxy port: %w", err)
	}
	configData, err := buildEndpointHealthCheckConfig(spec, endpoint, proxyPort)
	if err != nil {
		return err
	}
	configFile, err := os.CreateTemp("", "xui-endpoint-healthcheck-*.json")
	if err != nil {
		return fmt.Errorf("create health-check config: %w", err)
	}
	configPath := configFile.Name()
	defer os.Remove(configPath)
	if _, err := configFile.Write(configData); err != nil {
		_ = configFile.Close()
		return fmt.Errorf("write health-check config: %w", err)
	}
	if err := configFile.Close(); err != nil {
		return fmt.Errorf("close health-check config: %w", err)
	}

	binaryPath, err := resolveXrayBinaryPath()
	if err != nil {
		return err
	}
	if err := validateEndpointHealthCheckConfig(binaryPath, configPath); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), endpointHealthCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "-c", configPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start temporary Xray health-check client: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	if err := waitForHealthCheckProxy(ctx, proxyAddr, done); err != nil {
		return err
	}

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	targetHost := endpoint.Host
	if endpoint.Port != 443 {
		targetHost = net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	}
	target := "https://" + targetHost + normalizeManagedPath(healthPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("build real-chain health-check request: %w", err)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("VLESS/XHTTP real-chain request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("VLESS/XHTTP real-chain request returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func validateEndpointHealthCheckConfig(binaryPath string, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "run", "-test", "-config", configPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("validate temporary Xray health-check config timed out: %w", ctx.Err())
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("temporary Xray health-check config is not supported by %s: %s", binaryPath, message)
}

func buildEndpointHealthCheckConfig(spec *managedInboundSpec, endpoint *model.PublicEndpoint, proxyPort int) ([]byte, error) {
	if spec == nil || endpoint == nil {
		return nil, fmt.Errorf("health-check spec and endpoint are required")
	}
	if proxyPort <= 0 || proxyPort > 65535 {
		return nil, fmt.Errorf("invalid health-check proxy port: %d", proxyPort)
	}
	config := map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"inbounds": []interface{}{
			map[string]interface{}{
				"listen":   "127.0.0.1",
				"port":     proxyPort,
				"protocol": "http",
				"settings": map[string]interface{}{},
				"tag":      "endpoint-healthcheck-in",
			},
		},
		"outbounds": []interface{}{
			map[string]interface{}{
				"tag":      "endpoint-healthcheck-out",
				"protocol": "vless",
				"settings": map[string]interface{}{
					"vnext": []interface{}{
						map[string]interface{}{
							"address": endpoint.Host,
							"port":    endpoint.Port,
							"users": []interface{}{
								map[string]interface{}{
									"id":         spec.ClientID,
									"encryption": "none",
								},
							},
						},
					},
				},
				"streamSettings": map[string]interface{}{
					"network":  "xhttp",
					"security": "tls",
					"tlsSettings": map[string]interface{}{
						"serverName": endpoint.Host,
						"alpn":       []string{"http/1.1"},
					},
					"xhttpSettings": map[string]interface{}{
						"path": spec.Path,
						"host": endpoint.Host,
						"mode": spec.Mode,
					},
				},
			},
		},
		"routing": map[string]interface{}{
			"rules": []interface{}{
				map[string]interface{}{
					"type":        "field",
					"inboundTag":  []string{"endpoint-healthcheck-in"},
					"outboundTag": "endpoint-healthcheck-out",
				},
			},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal health-check config: %w", err)
	}
	return data, nil
}

func reserveLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func waitForHealthCheckProxy(ctx context.Context, address string, done <-chan error) error {
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("temporary Xray health-check client exited before becoming ready")
			}
			return fmt.Errorf("temporary Xray health-check client exited before becoming ready: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("temporary Xray health-check client startup timed out: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("temporary Xray health-check client did not open its local proxy")
		case <-ticker.C:
		}
	}
}

func resolveXrayBinaryPath() (string, error) {
	candidates := make([]string, 0, 4)
	if configured := os.Getenv("XUI_HEALTHCHECK_XRAY_BIN"); configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, xray.GetBinaryPath())
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), xray.GetBinaryPath()))
	}
	candidates = append(candidates, filepath.Join("..", "..", xray.GetBinaryPath()))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			abs, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return abs, nil
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("Xray health-check binary %s for %s/%s was not found", xray.GetBinaryPath(), runtime.GOOS, runtime.GOARCH)
}
