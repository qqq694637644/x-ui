package service

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/json_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

const (
	mytrnID         = 1
	mytrnDialTag    = "mytrn-dial"
	mytrnWarpTag    = "mytrn-warp"
	mytrnInboundTag = "mytrn-data-in"
	mytrnFreedomTag = "mytrn-freedom"
	mytrnServerName = "mytrn-a.test"
	// Defaults preserve the currently working A Python configuration:
	// A sets MTU=1200 and leaves the other mKCP settings to Xray 26.3.27.
	mytrnDefaultKcpMtu              = 1200
	mytrnDefaultKcpTti              = 50
	mytrnDefaultKcpUplinkCapacity   = 5
	mytrnDefaultKcpDownlinkCapacity = 20
	mytrnDefaultKcpBufferSize       = 2
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Mapping changes and panel settings updates must serialize against each
// other. x-ui's existing Xray restart scheduler performs the actual restart.
var mytrnUpdateMu sync.Mutex
var mytrnConfigIssue struct {
	sync.RWMutex
	message string
}

func setMyTRNConfigIssue(message string) {
	mytrnConfigIssue.Lock()
	mytrnConfigIssue.message = message
	mytrnConfigIssue.Unlock()
}

func getMyTRNConfigIssue() string {
	mytrnConfigIssue.RLock()
	defer mytrnConfigIssue.RUnlock()
	return mytrnConfigIssue.message
}

type MyTRNService struct{}

// MyTRNSettings is the authenticated panel's editable data. A blank token
// retains the existing secret. The remote IP/port are exclusively owned by
// the control API.
type MyTRNSettings struct {
	Enable              bool   `json:"enable"`
	Remark              string `json:"remark"`
	UUID                string `json:"uuid"`
	ControlToken        string `json:"controlToken"`
	ControlListen       string `json:"controlListen"`
	ControlPort         int    `json:"controlPort"`
	WarpHost            string `json:"warpHost"`
	WarpPort            int    `json:"warpPort"`
	KcpMtu              int  `json:"kcpMtu"`
	KcpTti              int  `json:"kcpTti"`
	KcpUplinkCapacity   int  `json:"kcpUplinkCapacity"`
	KcpDownlinkCapacity int  `json:"kcpDownlinkCapacity"`
	KcpCongestion       bool `json:"kcpCongestion"`
	KcpReadBufferSize   int  `json:"kcpReadBufferSize"`
	KcpWriteBufferSize  int  `json:"kcpWriteBufferSize"`
	ResetTrust          bool   `json:"resetTrust"`
}

type MyTRNView struct {
	Enable                 bool   `json:"enable"`
	Remark                 string `json:"remark"`
	UUID                   string `json:"uuid"`
	ControlToken           string `json:"controlToken"`
	ControlTokenConfigured bool   `json:"controlTokenConfigured"`
	ControlListen          string `json:"controlListen"`
	ControlPort            int    `json:"controlPort"`
	WarpHost               string `json:"warpHost"`
	WarpPort               int    `json:"warpPort"`
	KcpMtu                 int    `json:"kcpMtu"`
	KcpTti                 int    `json:"kcpTti"`
	KcpUplinkCapacity      int    `json:"kcpUplinkCapacity"`
	KcpDownlinkCapacity    int    `json:"kcpDownlinkCapacity"`
	KcpCongestion          bool   `json:"kcpCongestion"`
	KcpReadBufferSize      int    `json:"kcpReadBufferSize"`
	KcpWriteBufferSize     int    `json:"kcpWriteBufferSize"`
	CertificateFingerprint string `json:"certificateFingerprint"`
	EndpointIP             string `json:"endpointIP"`
	EndpointPort           int    `json:"endpointPort"`
	LastRegistration       int64  `json:"lastRegistration"`
	Status                 string `json:"status"`
	StatusMessage          string `json:"statusMessage"`
	ControlListenerRestartRequired bool `json:"controlListenerRestartRequired"`
	ControlListenerMessage string `json:"controlListenerMessage"`
}

func defaultMyTRN() *model.MyTRN {
	return &model.MyTRN{
		Id: mytrnID, Remark: "MyTRN 反向上网", ControlListen: "127.0.0.1",
		ControlPort: 18080, WarpHost: "127.0.0.1", WarpPort: 40000,
		KcpMtu: mytrnDefaultKcpMtu, KcpTti: mytrnDefaultKcpTti,
		KcpUplinkCapacity: mytrnDefaultKcpUplinkCapacity,
		KcpDownlinkCapacity: mytrnDefaultKcpDownlinkCapacity,
		KcpReadBufferSize: mytrnDefaultKcpBufferSize,
		KcpWriteBufferSize: mytrnDefaultKcpBufferSize,
	}
}

// Existing installations predate the mKCP columns. GORM gives new integer
// columns zero values; restore the exact effective Xray defaults in memory.
func normalizeMyTRNKCP(item *model.MyTRN) {
	if item.KcpMtu == 0 {
		item.KcpMtu = mytrnDefaultKcpMtu
	}
	if item.KcpTti == 0 {
		item.KcpTti = mytrnDefaultKcpTti
	}
	if item.KcpUplinkCapacity == 0 {
		item.KcpUplinkCapacity = mytrnDefaultKcpUplinkCapacity
	}
	if item.KcpDownlinkCapacity == 0 {
		item.KcpDownlinkCapacity = mytrnDefaultKcpDownlinkCapacity
	}
	if item.KcpReadBufferSize == 0 {
		item.KcpReadBufferSize = mytrnDefaultKcpBufferSize
	}
	if item.KcpWriteBufferSize == 0 {
		item.KcpWriteBufferSize = mytrnDefaultKcpBufferSize
	}
}

// Emit unchanged defaults as the original {"mtu":1200} JSON so upgrading
// the panel alone does not unnecessarily restart the working data path.
func mytrnKCPSettings(item *model.MyTRN) map[string]interface{} {
	kcp := map[string]interface{}{"mtu": item.KcpMtu}
	if item.KcpTti != mytrnDefaultKcpTti {
		kcp["tti"] = item.KcpTti
	}
	if item.KcpUplinkCapacity != mytrnDefaultKcpUplinkCapacity {
		kcp["uplinkCapacity"] = item.KcpUplinkCapacity
	}
	if item.KcpDownlinkCapacity != mytrnDefaultKcpDownlinkCapacity {
		kcp["downlinkCapacity"] = item.KcpDownlinkCapacity
	}
	if item.KcpCongestion {
		kcp["congestion"] = true
	}
	if item.KcpReadBufferSize != mytrnDefaultKcpBufferSize {
		kcp["readBufferSize"] = item.KcpReadBufferSize
	}
	if item.KcpWriteBufferSize != mytrnDefaultKcpBufferSize {
		kcp["writeBufferSize"] = item.KcpWriteBufferSize
	}
	return kcp
}

func (s *MyTRNService) Get() (*model.MyTRN, error) {
	item := defaultMyTRN()
	err := database.GetDB().Where("id = ?", mytrnID).First(item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultMyTRN(), nil
	}
	if err == nil {
		normalizeMyTRNKCP(item)
	}
	return item, err
}

func (s *MyTRNService) View() (*MyTRNView, error) {
	item, err := s.Get()
	if err != nil {
		return nil, err
	}
	status, statusMessage := mytrnStatus(item, currentXrayConfig(), xrayApplyFailure())
	if status == "pending_apply" {
		if configIssue := getMyTRNConfigIssue(); configIssue != "" {
			status, statusMessage = "apply_failed", configIssue
		}
	}
	listenerMessage := mytrnControlListenerMessage(item)
	return &MyTRNView{
		Enable: item.Enable, Remark: item.Remark, UUID: item.UUID,
		ControlToken: item.ControlToken,
		ControlTokenConfigured: item.ControlToken != "", ControlListen: item.ControlListen,
		ControlPort: item.ControlPort, WarpHost: item.WarpHost, WarpPort: item.WarpPort,
		KcpMtu: item.KcpMtu, KcpTti: item.KcpTti,
		KcpUplinkCapacity: item.KcpUplinkCapacity, KcpDownlinkCapacity: item.KcpDownlinkCapacity,
		KcpCongestion: item.KcpCongestion, KcpReadBufferSize: item.KcpReadBufferSize,
		KcpWriteBufferSize: item.KcpWriteBufferSize,
		CertificateFingerprint: item.CertificateFingerprint, EndpointIP: item.EndpointIP,
		EndpointPort: item.EndpointPort, LastRegistration: item.LastRegistration,
		Status: status, StatusMessage: statusMessage,
		ControlListenerRestartRequired: isMyTRNControlListenerRestartRequired(item),
		ControlListenerMessage: listenerMessage,
	}, nil
}

// The database describes the DESIRED mapping. Only the configuration of a
// currently running Xray process can prove it has actually been applied.
// Neither state constitutes an end-to-end A -> B -> website health check.
func mytrnStatus(item *model.MyTRN, active *xray.Config, applyError string) (string, string) {
	if !item.Enable {
		return "disabled", ""
	}
	if item.EndpointIP == "" || item.EndpointPort == 0 || item.CertificateFingerprint == "" {
		return "waiting_endpoint", "等待 A Python 通过控制面注册可信公网映射"
	}
	fingerprint, err := parseACertificate(item.CertificatePEM)
	if err != nil || fingerprint != item.CertificateFingerprint {
		return "invalid_certificate", "A TLS 证书已过期、损坏或与已固定指纹不一致；更新 A 证书后在面板明确重置信任"
	}
	if mytrnMatchesRunningConfig(item, active) {
		return "applied", "运行中的 Xray 已加载此映射；尚未收到 A 端实际代理上网成功的验证结果"
	}
	if applyError != "" {
		return "apply_failed", applyError
	}
	return "pending_apply", "公网映射已保存，等待 Xray 完成配置应用"
}

func mytrnMatchesRunningConfig(item *model.MyTRN, active *xray.Config) bool {
	if active == nil || active.MyTRNCertFingerprint != item.CertificateFingerprint {
		return false
	}
	var outbounds []struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Settings struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
			ID      string `json:"id"`
			Reverse struct {
				Tag string `json:"tag"`
			} `json:"reverse"`
			Servers []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"servers"`
		} `json:"settings"`
		StreamSettings struct {
			Network     string                     `json:"network"`
			Security    string                     `json:"security"`
			KcpSettings map[string]json.RawMessage `json:"kcpSettings"`
			Sockopt struct {
				DialerProxy string `json:"dialerProxy"`
			} `json:"sockopt"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal(active.OutboundConfigs, &outbounds); err != nil {
		return false
	}
	var dialOK, warpOK bool
	for _, outbound := range outbounds {
		switch outbound.Tag {
		case mytrnDialTag:
			dialOK = outbound.Protocol == "vless" && outbound.Settings.Address == item.EndpointIP &&
				outbound.Settings.Port == item.EndpointPort && outbound.Settings.ID == item.UUID &&
				outbound.Settings.Reverse.Tag == mytrnInboundTag &&
				outbound.StreamSettings.Network == "kcp" && outbound.StreamSettings.Security == "tls" &&
				mytrnKCPMatches(item, outbound.StreamSettings.KcpSettings) &&
				outbound.StreamSettings.Sockopt.DialerProxy == mytrnWarpTag
		case mytrnWarpTag:
			warpOK = outbound.Protocol == "socks" && len(outbound.Settings.Servers) == 1 &&
				outbound.Settings.Servers[0].Address == item.WarpHost &&
				outbound.Settings.Servers[0].Port == item.WarpPort
		}
	}
	return dialOK && warpOK
}

func mytrnKCPMatches(item *model.MyTRN, active map[string]json.RawMessage) bool {
	actualJSON, err := json.Marshal(active)
	if err != nil {
		return false
	}
	wantedJSON, err := json.Marshal(mytrnKCPSettings(item))
	return err == nil && bytes.Equal(actualJSON, wantedJSON)
}

func validPort(p int) bool { return p > 0 && p <= 65535 }

func constantTimeTokenMatch(expected string, supplied string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func validateMyTRNSettings(item *model.MyTRN) error {
	if len(item.Remark) > 200 {
		return errors.New("MyTRN 备注过长")
	}
	if !validPort(item.ControlPort) || !validPort(item.WarpPort) {
		return errors.New("MyTRN 控制端口/WARP 端口必须在 1-65535")
	}
	if net.ParseIP(item.ControlListen) == nil {
		return errors.New("MyTRN 控制监听地址必须是 IP")
	}
	if net.ParseIP(item.WarpHost) == nil || net.ParseIP(item.WarpHost).To4() == nil {
		return errors.New("MyTRN WARP SOCKS5 地址必须是 IPv4")
	}
	if item.KcpMtu < 576 || item.KcpMtu > 1460 {
		return errors.New("MyTRN mKCP MTU 必须在 576-1460 字节；A Python 当前使用 1200")
	}
	// Xray 26.3.27 divides by (1000/TTI) when computing its window size.
	// Values above 1000 may pass Xray's parser but can divide by zero at runtime.
	if item.KcpTti < 10 || item.KcpTti > 1000 {
		return errors.New("MyTRN mKCP TTI 必须在 10-1000 毫秒")
	}
	if item.KcpUplinkCapacity < 1 || item.KcpUplinkCapacity > 1000 ||
		item.KcpDownlinkCapacity < 1 || item.KcpDownlinkCapacity > 1000 {
		return errors.New("MyTRN mKCP 上下行容量必须在 1-1000 MB/s")
	}
	if item.KcpReadBufferSize < 1 || item.KcpReadBufferSize > 256 ||
		item.KcpWriteBufferSize < 1 || item.KcpWriteBufferSize > 256 {
		return errors.New("MyTRN mKCP 缓冲区必须在 1-256 MB")
	}
	if item.Enable {
		if !uuidPattern.MatchString(item.UUID) {
			return errors.New("MyTRN UUID 必须与 A Python 配置中的标准 VLESS UUID 相同")
		}
		if len(item.ControlToken) < 32 || len(item.ControlToken) > 256 {
			return errors.New("MyTRN 控制 token 至少 32 个字符，最多 256 个字符")
		}
	}
	return nil
}

func (s *MyTRNService) UpdateSettings(edit MyTRNSettings) (bool, error) {
	mytrnUpdateMu.Lock()
	defer mytrnUpdateMu.Unlock()
	old, err := s.Get()
	if err != nil {
		return false, err
	}
	next := *old
	next.Enable, next.Remark, next.UUID = edit.Enable, edit.Remark, strings.TrimSpace(edit.UUID)
	next.ControlListen, next.ControlPort = strings.TrimSpace(edit.ControlListen), edit.ControlPort
	next.WarpHost, next.WarpPort = strings.TrimSpace(edit.WarpHost), edit.WarpPort
	next.KcpMtu, next.KcpTti = edit.KcpMtu, edit.KcpTti
	next.KcpUplinkCapacity, next.KcpDownlinkCapacity = edit.KcpUplinkCapacity, edit.KcpDownlinkCapacity
	next.KcpCongestion = edit.KcpCongestion
	next.KcpReadBufferSize, next.KcpWriteBufferSize = edit.KcpReadBufferSize, edit.KcpWriteBufferSize
	if edit.ControlToken != "" {
		next.ControlToken = edit.ControlToken
	}
	if edit.ResetTrust {
		next.CertificatePEM, next.CertificateFingerprint = "", ""
		next.EndpointIP, next.EndpointPort, next.LastRegistration = "", 0, 0
	}
	if err := validateMyTRNSettings(&next); err != nil {
		return false, err
	}
	// Start a new control listener before committing its address to the DB.
	// The already running listener is not interrupted on a bind failure.
	if err := prepareMyTRNControlListener(&next); err != nil {
		return false, err
	}
	if err := database.GetDB().Save(&next).Error; err != nil {
		discardPreparedMyTRNControlListener()
		return false, err
	}
	applyMyTRNControlListener(&next)
	// Do not restart Xray for a change to the control HTTP listener/token.
	changesData := old.Enable != next.Enable || old.UUID != next.UUID ||
		old.WarpHost != next.WarpHost || old.WarpPort != next.WarpPort ||
		old.KcpMtu != next.KcpMtu || old.KcpTti != next.KcpTti ||
		old.KcpUplinkCapacity != next.KcpUplinkCapacity || old.KcpDownlinkCapacity != next.KcpDownlinkCapacity ||
		old.KcpCongestion != next.KcpCongestion ||
		old.KcpReadBufferSize != next.KcpReadBufferSize || old.KcpWriteBufferSize != next.KcpWriteBufferSize ||
		old.CertificateFingerprint != next.CertificateFingerprint
	return changesData, nil
}

func isPublicIPv4(value string) bool {
	ip := net.ParseIP(value)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Reject 0/8, shared CGNAT, benchmark, multicast and reserved addresses.
	v := ip.To4()
	return v[0] != 0 && v[0] != 10 && v[0] != 127 && v[0] < 224 &&
		!(v[0] == 100 && v[1] >= 64 && v[1] <= 127) &&
		!(v[0] == 198 && (v[1] == 18 || v[1] == 19))
}

func parseACertificate(value string) (string, error) {
	if len(value) == 0 || len(value) > 8192 {
		return "", errors.New("A 证书为空或过长")
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("A 证书必须是单张 PEM X.509 证书")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return "", errors.New("A TLS 证书不在有效期")
	}
	if err := cert.VerifyHostname(mytrnServerName); err != nil {
		return "", fmt.Errorf("A TLS 证书 serverName 不匹配: %w", err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

// MyTRNRegistration is the exact on-wire JSON sent by the proven A Python
// control.py; no legacy API and no change to A is required.
type MyTRNRegistration struct {
	Node        string `json:"node"`
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	Certificate string `json:"certificate"`
}

type MyTRNRegisterResult struct {
	Changed     bool   `json:"changed"`
	Fingerprint string `json:"certificate_sha256"`
}

func (s *MyTRNService) Register(token string, value MyTRNRegistration) (*MyTRNRegisterResult, error) {
	mytrnUpdateMu.Lock()
	defer mytrnUpdateMu.Unlock()
	item, err := s.Get()
	if err != nil {
		return nil, err
	}
	if !item.Enable || !constantTimeTokenMatch(item.ControlToken, token) {
		return nil, errMyTRNUnauthorized
	}
	if value.Node != "a" || !isPublicIPv4(value.IP) || !validPort(value.Port) {
		return nil, errors.New("A endpoint 必须为有效公网 IPv4:port，node=a")
	}
	fingerprint, err := parseACertificate(value.Certificate)
	if err != nil {
		return nil, err
	}
	if item.CertificateFingerprint != "" && item.CertificateFingerprint != fingerprint {
		return nil, errMyTRNCertificateChanged
	}
	changed := item.EndpointIP != value.IP || item.EndpointPort != value.Port || item.CertificateFingerprint == ""
	item.CertificatePEM, item.CertificateFingerprint = value.Certificate, fingerprint
	item.EndpointIP, item.EndpointPort = value.IP, value.Port
	item.LastRegistration = time.Now().Unix()
	if err := database.GetDB().Save(item).Error; err != nil {
		return nil, err
	}
	if changed {
		(&XrayService{}).SetToNeedRestart()
	}
	return &MyTRNRegisterResult{Changed: changed, Fingerprint: fingerprint}, nil
}

var (
	errMyTRNUnauthorized       = errors.New("unauthorized")
	errMyTRNCertificateChanged = errors.New("A certificate changed: explicit re-trust required")
)

func mytrnCertificatePath() (string, error) {
	return filepath.Abs(filepath.Join("bin", "mytrn-a-cert.pem"))
}

func ensureMyTRNCertificate(value string) (string, error) {
	path, err := mytrnCertificatePath()
	if err != nil {
		return "", err
	}
	if content, err := os.ReadFile(path); err == nil && string(content) == value {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mytrn-cert-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(value); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// mergeMyTRNConfig leaves existing inbounds, outbounds, Caddy, Portal and
// routing intact. It adds two outbound records and one exact inbound-tag rule
// after existing block rules. The first template freedom is reused, not copied.
func mergeMyTRNConfig(conf *xray.Config, item *model.MyTRN, certFile string) error {
	if !item.Enable || item.EndpointIP == "" {
		return nil
	}
	if err := validateMyTRNSettings(item); err != nil {
		return err
	}
	if !isPublicIPv4(item.EndpointIP) || !validPort(item.EndpointPort) || certFile == "" {
		return errors.New("MyTRN endpoint 或 A 证书缺失")
	}
	var outbound []map[string]interface{}
	if err := json.Unmarshal(conf.OutboundConfigs, &outbound); err != nil {
		return fmt.Errorf("xray outbounds 无效: %w", err)
	}
	if len(outbound) == 0 {
		return errors.New("xray 配置缺少已存在的 freedom 出站")
	}
	seen := make(map[string]bool)
	for _, in := range conf.InboundConfigs {
		if in.Tag != "" {
			seen[in.Tag] = true
		}
	}
	for _, item := range outbound {
		if tag, ok := item["tag"].(string); ok && tag != "" {
			if seen[tag] {
				return fmt.Errorf("Xray tag 冲突: %s", tag)
			}
			seen[tag] = true
		}
	}
	for _, tag := range []string{mytrnDialTag, mytrnWarpTag, mytrnInboundTag} {
		if seen[tag] {
			return fmt.Errorf("MyTRN tag 冲突: %s", tag)
		}
	}
	first := outbound[0]
	if first["protocol"] != "freedom" {
		return errors.New("MyTRN 要求模板首条出站是既有 freedom，拒绝创建第二条")
	}
	freedomTag, _ := first["tag"].(string)
	if freedomTag == "" {
		if seen[mytrnFreedomTag] {
			return fmt.Errorf("MyTRN freedom tag 冲突: %s", mytrnFreedomTag)
		}
		freedomTag = mytrnFreedomTag
		first["tag"] = freedomTag
	}
	dial := map[string]interface{}{
		"tag": mytrnDialTag, "protocol": "vless",
		"settings": map[string]interface{}{
			"address": item.EndpointIP, "port": item.EndpointPort,
			"id": item.UUID, "encryption": "none",
			"reverse": map[string]interface{}{"tag": mytrnInboundTag},
		},
		"streamSettings": map[string]interface{}{
			"network": "kcp", "security": "tls", "kcpSettings": mytrnKCPSettings(item),
			"tlsSettings": map[string]interface{}{
				"serverName": mytrnServerName, "allowInsecure": false,
				"disableSystemRoot": true,
				"certificates":      []interface{}{map[string]interface{}{"certificateFile": certFile, "usage": "verify"}},
			},
			"sockopt": map[string]interface{}{"dialerProxy": mytrnWarpTag},
		},
	}
	warp := map[string]interface{}{
		"tag": mytrnWarpTag, "protocol": "socks",
		"settings": map[string]interface{}{"servers": []interface{}{map[string]interface{}{
			"address": item.WarpHost, "port": item.WarpPort,
		}}},
	}
	outbound = append(outbound, dial, warp)
	data, err := json.Marshal(outbound)
	if err != nil {
		return err
	}
	var routing map[string]json.RawMessage
	if err := json.Unmarshal(conf.RouterConfig, &routing); err != nil {
		return fmt.Errorf("xray routing 无效: %w", err)
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(routing["rules"], &rules); err != nil {
		return fmt.Errorf("xray routing.rules 无效: %w", err)
	}
	if err := rejectShadowedMyTRNRouting(rules); err != nil {
		return err
	}
	rule, err := json.Marshal(map[string]interface{}{
		"type": "field", "inboundTag": []string{mytrnInboundTag}, "outboundTag": freedomTag,
	})
	if err != nil {
		return err
	}
	rules = append(rules, rule)
	routing["rules"], err = json.Marshal(rules)
	if err != nil {
		return err
	}
	rawRouting, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	conf.OutboundConfigs = json_util.RawMessage(data)
	conf.RouterConfig = json_util.RawMessage(rawRouting)
	conf.MyTRNCertFingerprint = item.CertificateFingerprint
	return nil
}

// An unscoped catch-all rule placed before our reverse-in rule would swallow
// all MyTRN traffic. Refuse that template rather than silently routing A's
// requests to its unrelated default outbound. Scoped API and existing
// geoip:private/BitTorrent block rules remain in their original order.
func rejectShadowedMyTRNRouting(rules []json.RawMessage) error {
	for i, raw := range rules {
		var rule map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rule); err != nil {
			return fmt.Errorf("xray routing.rules[%d] 无效: %w", i, err)
		}
		var inbounds []string
		if tags, ok := rule["inboundTag"]; ok {
			if err := json.Unmarshal(tags, &inbounds); err != nil {
				return fmt.Errorf("routing.rules[%d].inboundTag 无效: %w", i, err)
			}
		}
		for _, tag := range inbounds {
			if tag == mytrnInboundTag {
				return fmt.Errorf("routing.rules[%d] 已使用 MyTRN 专用反向入站 tag，拒绝覆盖", i)
			}
		}
		if len(inbounds) != 0 {
			continue
		}
		// Source/target/identity-specific policies are not unconditional.
		// Merely specifying network=tcp,udp does NOT narrow a catch-all.
		restricted := false
		for _, field := range []string{"domain", "ip", "port", "sourcePort", "source", "user", "protocol", "attrs"} {
			value := strings.TrimSpace(string(rule[field]))
			if value == "" || value == "null" || value == "[]" || value == `""` {
				continue
			}
			if field == "port" || field == "sourcePort" {
				var ports string
				if json.Unmarshal(rule[field], &ports) == nil &&
					(ports == "0-65535" || ports == "1-65535" || ports == "0-65535,1-65535") {
					continue
				}
			}
			if field == "ip" || field == "source" {
				var ranges []string
				if json.Unmarshal(rule[field], &ranges) == nil && len(ranges) > 0 {
					wildcard := false
					for _, r := range ranges {
						if r == "0.0.0.0/0" || r == "::/0" {
							wildcard = true
						}
						}
					if wildcard {
						continue
					}
				}
			}
			if field == "domain" {
				var domains []string
				if json.Unmarshal(rule[field], &domains) == nil && len(domains) > 0 {
					wildcard := false
					for _, d := range domains {
						if d == "regexp:.*" || d == "regexp:^.*$" {
							wildcard = true
						}
					}
					if wildcard {
						continue
					}
				}
			}
			if value != "" {
				restricted = true
				break
			}
		}
		if !restricted {
			return fmt.Errorf("routing.rules[%d] 是无 inboundTag 的宽泛匹配规则，会覆盖 MyTRN 数据路由", i)
		}
	}
	return nil
}

func (s *MyTRNService) ApplyToXrayConfig(config *xray.Config) error {
	item, err := s.Get()
	if err != nil || !item.Enable || item.EndpointIP == "" {
		if err == nil {
			setMyTRNConfigIssue("")
		}
		return err
	}
	fingerprint, err := parseACertificate(item.CertificatePEM)
	if err != nil || fingerprint != item.CertificateFingerprint {
		// MyTRN's invalid certificate must not prevent the unrelated
		// existing VLESS inbounds from cold-starting. The panel reports
		// invalid_certificate and requires explicit re-trust.
		setMyTRNConfigIssue("A TLS 证书无效，MyTRN 数据面未生成；更新 A 证书后重置信任")
		logger.Warning("MyTRN data plane disabled: A TLS certificate invalid or expired")
		return nil
	}
	certificate, err := ensureMyTRNCertificate(item.CertificatePEM)
	if err != nil {
		setMyTRNConfigIssue("无法写入 MyTRN A TLS 公钥证书文件：" + err.Error())
		logger.Warning("MyTRN data plane disabled: could not stage A TLS certificate:", err)
		// Keep the existing VLESS inbounds up while retrying the MyTRN
		// certificate staging on the normal restart scheduler. When the
		// filesystem recovers, the next generated config includes MyTRN.
		(&XrayService{}).SetToNeedRestart()
		return nil
	}
	if err := mergeMyTRNConfig(config, item, certificate); err != nil {
		return err
	}
	setMyTRNConfigIssue("")
	return nil
}

// ControlAddress describes only the ordinary Go HTTP listener, not any Xray
// reverse tag or public Caddy route.
func ControlAddress(item *model.MyTRN) string {
	return net.JoinHostPort(item.ControlListen, strconv.Itoa(item.ControlPort))
}
