package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"x-ui/util/json_util"
	"x-ui/xray"
)

// This is the overseas side ONLY. The domestic reverse_node is the Portal and
// listens on the public IPv6 Hysteria2 UDP port. Xray on this VPS is the Bridge:
// it actively dials the Portal and receives reverse SOCKS5 requests.
//
// We intentionally do not reuse the old FRP/RSX3 manager or create a second
// QUIC protocol implementation. Xray 26.3.27 provides both reverse and Brutal.
const reverseQUICSettingKey = "reverseQuicBridgeConfig"

const (
	reverseQUICBridgeTag   = "reverse-node-bridge"
	reverseQUICOutboundTag = "reverse-node-hysteria"
	reverseQUICEgressTag   = "reverse-node-egress"
	reverseQUICDomain      = "reverse-node.internal"
)

var reverseQUICHostLabel = regexp.MustCompile("^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$")
var reverseQUICPin = regexp.MustCompile("^[a-fA-F0-9]{64}$")

// ReverseQUICBridgeConfig is independent of the legacy FRP reverse SOCKS form.
// It only supports Xray-native Hysteria2/QUIC force-brutal.
type ReverseQUICBridgeConfig struct {
	Enabled        bool   `json:"enabled" form:"enabled"`
	Domain         string `json:"domain" form:"domain"`
	PublicHost     string `json:"publicHost" form:"publicHost"`
	PublicPort     int    `json:"publicPort" form:"publicPort"`
	Auth           string `json:"auth" form:"auth"`
	ServerName     string `json:"serverName" form:"serverName"`
	CertificatePin string `json:"certificatePin" form:"certificatePin"`
	BrutalUp       string `json:"brutalUp" form:"brutalUp"`
	BrutalDown     string `json:"brutalDown" form:"brutalDown"`
	EgressMode     string `json:"egressMode" form:"egressMode"`
	SOCKS5Address  string `json:"socks5Address" form:"socks5Address"`
	SOCKS5Username string `json:"socks5Username" form:"socks5Username"`
	SOCKS5Password string `json:"socks5Password" form:"socks5Password"`
}

type ReverseQUICBridgeService struct {
	mu          sync.Mutex
	settings    SettingService
	restartXray func() error // test hook; production uses XrayService.RestartXray
}

func defaultReverseQUICBridgeConfig() ReverseQUICBridgeConfig {
	return ReverseQUICBridgeConfig{
		Enabled:       false,
		Domain:        reverseQUICDomain,
		PublicPort:    18443,
		BrutalUp:      "24mbps", // VPS sends to Windows; Windows downloads.
		BrutalDown:    "16mbps", // VPS receives from Windows; Windows uploads.
		EgressMode:    "direct",
		SOCKS5Address: "127.0.0.1:40001",
	}
}

func normalizeReverseQUICBridgeConfig(c ReverseQUICBridgeConfig) ReverseQUICBridgeConfig {
	c.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	c.PublicHost = strings.TrimSuffix(strings.TrimSpace(c.PublicHost), ".")
	c.ServerName = strings.TrimSuffix(strings.TrimSpace(c.ServerName), ".")
	c.CertificatePin = strings.ToLower(strings.TrimSpace(c.CertificatePin))
	c.BrutalUp = strings.ToLower(strings.TrimSpace(c.BrutalUp))
	c.BrutalDown = strings.ToLower(strings.TrimSpace(c.BrutalDown))
	c.EgressMode = strings.TrimSpace(c.EgressMode)
	c.SOCKS5Address = strings.TrimSpace(c.SOCKS5Address)
	c.SOCKS5Username = strings.TrimSpace(c.SOCKS5Username)
	if c.Domain == "" {
		c.Domain = reverseQUICDomain
	}
	if c.ServerName == "" {
		c.ServerName = c.PublicHost
	}
	if c.EgressMode == "" {
		c.EgressMode = "direct"
	}
	if c.EgressMode == "direct" {
		c.SOCKS5Username = ""
		c.SOCKS5Password = ""
	}
	return c
}

func validateReverseQUICHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return fmt.Errorf("本模式仅支持境内公网 IPv6/DDNS AAAA，不接受 IPv4 地址")
		}
		return nil
	}
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\:@ \t\r\n") {
		return fmt.Errorf("地址应为 DDNS 主机名或 IPv6，不可包含协议、端口或路径")
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) > 63 || !reverseQUICHostLabel.MatchString(part) {
			return fmt.Errorf("非法 DDNS / 反向路由域名 %q", host)
		}
	}
	return nil
}

func validateReverseQUICBridgeConfig(c ReverseQUICBridgeConfig) error {
	if !c.Enabled {
		return nil
	}
	if err := validateReverseQUICHost(c.PublicHost); err != nil {
		return fmt.Errorf("境内 DDNS: %w", err)
	}
	if err := validateReverseQUICHost(c.Domain); err != nil {
		return fmt.Errorf("反向路由域名: %w", err)
	}
	if err := validateReverseQUICHost(c.ServerName); err != nil {
		return fmt.Errorf("TLS ServerName: %w", err)
	}
	if c.PublicPort < 1 || c.PublicPort > 65535 {
		return fmt.Errorf("Hysteria2 UDP 端口必须在 1-65535")
	}
	if len(c.Auth) < 32 || len(c.Auth) > 1024 || strings.ContainsAny(c.Auth, "\t\r\n ") {
		return fmt.Errorf("Hysteria2 auth 必须是 32-1024 字符的共享密钥，不允许空格")
	}
	if c.CertificatePin != "" && !reverseQUICPin.MatchString(c.CertificatePin) {
		return fmt.Errorf("证书 SHA-256 pin 必须是 64 位十六进制，使用自签名证书时必填")
	}
	if err := validateHysteriaBrutalRate(c.BrutalUp); err != nil {
		return fmt.Errorf("VPS Brutal 上传速率: %w", err)
	}
	if err := validateHysteriaBrutalRate(c.BrutalDown); err != nil {
		return fmt.Errorf("VPS Brutal 下载速率: %w", err)
	}
	switch c.EgressMode {
	case "direct":
	case "socks5":
		host, port, err := net.SplitHostPort(c.SOCKS5Address)
		if err != nil {
			return fmt.Errorf("本机 SOCKS5 地址必须为 127.0.0.1:端口，IPv6 回环请用 [::1]:端口")
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("真实出口 SOCKS5 仅允许本机回环地址 127.0.0.1 或 ::1")
		}
		portNum, err := strconv.Atoi(port)
		if err != nil || portNum < 1 || portNum > 65535 {
			return fmt.Errorf("SOCKS5 端口无效")
		}
		if c.SOCKS5Username == "" && c.SOCKS5Password != "" {
			return fmt.Errorf("SOCKS5 密码需要同时提供用户名")
		}
	default:
		return fmt.Errorf("只支持直连或本机 SOCKS5 两种真实出口")
	}
	return nil
}

func (s *ReverseQUICBridgeService) load() (ReverseQUICBridgeConfig, error) {
	raw, err := s.settings.getString(reverseQUICSettingKey)
	if err != nil {
		return ReverseQUICBridgeConfig{}, err
	}
	c := defaultReverseQUICBridgeConfig()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return ReverseQUICBridgeConfig{}, fmt.Errorf("境外 QUIC Bridge 配置损坏: %w", err)
		}
	}
	return normalizeReverseQUICBridgeConfig(c), nil
}

func (s *ReverseQUICBridgeService) Get() (ReverseQUICBridgeConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *ReverseQUICBridgeService) Save(in ReverseQUICBridgeConfig) (ReverseQUICBridgeConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := normalizeReverseQUICBridgeConfig(in)
	if err := validateReverseQUICBridgeConfig(next); err != nil {
		return ReverseQUICBridgeConfig{}, err
	}
	prev, err := s.load()
	if err != nil {
		return ReverseQUICBridgeConfig{}, err
	}
	marshaled, err := json.Marshal(next)
	if err != nil {
		return ReverseQUICBridgeConfig{}, err
	}
	if err := s.settings.setString(reverseQUICSettingKey, string(marshaled)); err != nil {
		return ReverseQUICBridgeConfig{}, err
	}
	if !next.Enabled && !prev.Enabled {
		return next, nil
	}
	restart := s.restartXray
	if restart == nil {
		restart = func() error { return (&XrayService{}).RestartXray(false) }
	}
	if err := restart(); err != nil {
		oldJSON, jsonErr := json.Marshal(prev)
		if jsonErr != nil {
			return ReverseQUICBridgeConfig{}, fmt.Errorf("Xray 配置应用失败: %w (回滚序列化失败: %v)", err, jsonErr)
		}
		if restoreErr := s.settings.setString(reverseQUICSettingKey, string(oldJSON)); restoreErr != nil {
			return ReverseQUICBridgeConfig{}, fmt.Errorf("Xray 应用失败: %w; 数据库恢复也失败: %v", err, restoreErr)
		}
		// RestartXray already restores the previous running process on startup
		// failure. Restore the old persisted settings so future restarts agree.
		return ReverseQUICBridgeConfig{}, fmt.Errorf("境外 Bridge 应用失败，已恢复旧配置: %w", err)
	}
	return next, nil
}

func reverseQUICRawArray(raw json.RawMessage, description string) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []json.RawMessage{}, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s 不是 JSON 数组: %w", description, err)
	}
	return out, nil
}

// This only appends to the existing Xray instance; it does not replace the
// template's outbounds, routing rules or other reverse Portal/Bridge entries.
func applyReverseQUICBridge(config *xray.Config, c ReverseQUICBridgeConfig) error {
	c = normalizeReverseQUICBridgeConfig(c)
	if err := validateReverseQUICBridgeConfig(c); err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}

	reverse := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(config.Reverse)) > 0 && !bytes.Equal(bytes.TrimSpace(config.Reverse), []byte("null")) {
		if err := json.Unmarshal(config.Reverse, &reverse); err != nil {
			return fmt.Errorf("已有 Xray reverse 配置无效: %w", err)
		}
	}
	bridges, err := reverseQUICRawArray(reverse["bridges"], "reverse.bridges")
	if err != nil {
		return err
	}
	portals, err := reverseQUICRawArray(reverse["portals"], "reverse.portals")
	if err != nil {
		return err
	}
	for _, entry := range append(append([]json.RawMessage(nil), bridges...), portals...) {
		var item struct {
			Tag    string `json:"tag"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(entry, &item); err != nil {
			return fmt.Errorf("已有 reverse 项无效: %w", err)
		}
		if item.Tag == reverseQUICBridgeTag || strings.EqualFold(item.Domain, c.Domain) {
			return fmt.Errorf("reverse tag/domain 与已有 Bridge/Portal 冲突: %s / %s", item.Tag, item.Domain)
		}
	}

	outbounds, err := reverseQUICRawArray(json.RawMessage(config.OutboundConfigs), "outbounds")
	if err != nil {
		return err
	}
	for _, outbound := range outbounds {
		var item struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(outbound, &item); err != nil {
			return fmt.Errorf("已有 Xray 出站无效: %w", err)
		}
		if item.Tag == reverseQUICOutboundTag || item.Tag == reverseQUICEgressTag {
			return fmt.Errorf("已有 Xray 出站占用新 Bridge 保留的 tag %q", item.Tag)
		}
	}
	for _, inbound := range config.InboundConfigs {
		if inbound.Tag == reverseQUICBridgeTag || inbound.Tag == reverseQUICOutboundTag || inbound.Tag == reverseQUICEgressTag {
			return fmt.Errorf("已有 Xray 入站占用新 Bridge 保留的 tag %q", inbound.Tag)
		}
	}

	tls := map[string]interface{}{
		"serverName": c.ServerName,
		"alpn":       []string{"h3"},
	}
	if c.CertificatePin != "" {
		tls["pinnedPeerCertSha256"] = c.CertificatePin
		tls["verifyPeerCertByName"] = c.ServerName
	}
	hy2 := map[string]interface{}{
		"tag":      reverseQUICOutboundTag,
		"protocol": "hysteria",
		"settings": map[string]interface{}{
			"version": 2,
			"address": c.PublicHost,
			"port":    c.PublicPort,
		},
		"streamSettings": map[string]interface{}{
			"network":     "hysteria",
			"security":    "tls",
			"tlsSettings": tls,
			"hysteriaSettings": map[string]interface{}{
				"version": 2,
				"auth":    c.Auth,
			},
			"sockopt": map[string]interface{}{"domainStrategy": "UseIPv6"},
			"finalmask": map[string]interface{}{
				"quicParams": map[string]interface{}{
					"congestion": "force-brutal",
					"brutalUp":   c.BrutalUp,
					"brutalDown": c.BrutalDown,
				},
			},
		},
	}
	built, err := json.Marshal(hy2)
	if err != nil {
		return err
	}
	outbounds = append(outbounds, built)
	var egress map[string]interface{}
	if c.EgressMode == "socks5" {
		host, port, _ := net.SplitHostPort(c.SOCKS5Address)
		number, _ := strconv.Atoi(port)
		server := map[string]interface{}{
			"address": host,
			"port":    number,
		}
		if c.SOCKS5Username != "" {
			server["users"] = []interface{}{map[string]interface{}{
				"user": c.SOCKS5Username,
				"pass": c.SOCKS5Password,
			}}
		}
		egress = map[string]interface{}{
			"tag":      reverseQUICEgressTag,
			"protocol": "socks",
			"settings": map[string]interface{}{
				"servers": []interface{}{server},
			},
		}
	} else {
		egress = map[string]interface{}{
			"tag":      reverseQUICEgressTag,
			"protocol": "freedom",
			"settings": map[string]interface{}{},
		}
	}
	built, err = json.Marshal(egress)
	if err != nil {
		return err
	}
	outbounds = append(outbounds, built)

	bridge, err := json.Marshal(map[string]interface{}{
		"tag": reverseQUICBridgeTag, "domain": c.Domain,
	})
	if err != nil {
		return err
	}
	bridges = append(bridges, bridge)
	bridgesJSON, err := json.Marshal(bridges)
	if err != nil {
		return err
	}
	reverse["bridges"] = bridgesJSON
	reverseJSON, err := json.Marshal(reverse)
	if err != nil {
		return err
	}

	router := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(config.RouterConfig)) > 0 && !bytes.Equal(bytes.TrimSpace(config.RouterConfig), []byte("null")) {
		if err := json.Unmarshal(config.RouterConfig, &router); err != nil {
			return fmt.Errorf("已有 Xray routing 配置无效: %w", err)
		}
	}
	rules, err := reverseQUICRawArray(router["rules"], "routing.rules")
	if err != nil {
		return err
	}
	// The registration rule must precede generic domain blocks and the bridge
	// exit rule must precede generic catch-all rules. The latter is essential
	// for a fail-closed local SOCKS5 exit: no fallback to VPS freedom/WARP.
	registration, err := json.Marshal(map[string]interface{}{
		"type": "field", "domain": []string{"full:" + c.Domain},
		"outboundTag": reverseQUICOutboundTag,
	})
	if err != nil {
		return err
	}
	exit, err := json.Marshal(map[string]interface{}{
		"type": "field", "inboundTag": []string{reverseQUICBridgeTag},
		"outboundTag": reverseQUICEgressTag,
	})
	if err != nil {
		return err
	}
	rules = append([]json.RawMessage{registration, exit}, rules...)
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	router["rules"] = rulesJSON
	routerJSON, err := json.Marshal(router)
	if err != nil {
		return err
	}
	outboundsJSON, err := json.Marshal(outbounds)
	if err != nil {
		return err
	}

	config.Reverse = reverseJSON
	config.RouterConfig = routerJSON
	config.OutboundConfigs = outboundsJSON
	return nil
}

// BuildReverseQUICFixtureConfig reuses the production Bridge generator in
// isolated two-process Xray smoke tests. It never reads or modifies the panel
// database; it is not a second implementation of the network protocol.
func BuildReverseQUICFixtureConfig(c ReverseQUICBridgeConfig) (*xray.Config, error) {
	base := &xray.Config{
		LogConfig:       json_util.RawMessage(`{"loglevel":"debug"}`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
		OutboundConfigs: json_util.RawMessage(`[{"tag":"legacy-direct","protocol":"freedom","settings":{}}]`),
	}
	if err := applyReverseQUICBridge(base, c); err != nil {
		return nil, err
	}
	return base, nil
}
