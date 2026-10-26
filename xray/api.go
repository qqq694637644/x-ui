package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Xray 26.3.27 supports the native api.listen gRPC server. x-ui owns a
// loopback-only default endpoint; no dokodemo-door inbound or api routing rule
// is required. Older API inbound compatibility is intentionally not supported.
const defaultStatsAPIListen = "127.0.0.1:62789"

// ConfigureStatsAPI provisions Xray's native gRPC listener for templates that
// omit api.listen, without changing unrelated services or routing rules.
func (c *Config) ConfigureStatsAPI() error {
	var api map[string]json.RawMessage
	if err := json.Unmarshal(c.API, &api); err != nil || api == nil {
		return fmt.Errorf("Xray 26.3.27 requires a JSON api object: %v", err)
	}
	if _, ok := api["listen"]; !ok {
		listen, _ := json.Marshal(defaultStatsAPIListen)
		api["listen"] = listen
	}
	updated, err := json.Marshal(api)
	if err != nil {
		return err
	}
	c.API = updated
	_, err = c.StatsAPIPort()
	return err
}

// StatsAPIPort is the single supported Xray 26.3.27 StatsService discovery
// path. It is deliberately strict: no fallback to legacy tag=api inbounds.
func (c *Config) StatsAPIPort() (int, error) {
	var api struct {
		Tag      string   `json:"tag"`
		Listen   string   `json:"listen"`
		Services []string `json:"services"`
	}
	if err := json.Unmarshal(c.API, &api); err != nil {
		return 0, fmt.Errorf("invalid Xray 26.3.27 api configuration: %w", err)
	}
	if api.Tag != "api" {
		return 0, errors.New("Xray 26.3.27 api.tag must be api")
	}
	stats := false
	for _, service := range api.Services {
		if service == "StatsService" {
			stats = true
		}
	}
	if !stats {
		return 0, errors.New("Xray 26.3.27 api.services must contain StatsService")
	}
	host, portText, err := net.SplitHostPort(api.Listen)
	if err != nil || host != "127.0.0.1" {
		return 0, errors.New("Xray 26.3.27 api.listen must be a local IPv4 TCP endpoint (127.0.0.1:port)")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("Xray 26.3.27 api.listen has invalid TCP port")
	}
	for _, inbound := range c.InboundConfigs {
		if inbound.Tag == "api" {
			return 0, errors.New("legacy Xray API inbound tag=api is unsupported with Xray 26.3.27 api.listen; remove it from the template")
		}
		if inbound.Port == port {
			return 0, fmt.Errorf("Xray 26.3.27 api.listen port %d conflicts with inbound %s", port, strings.TrimSpace(inbound.Tag))
		}
	}
	return port, nil
}
