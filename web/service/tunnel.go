package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/util/json_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

const (
	TunnelModeDirect = "direct"
	TunnelModePortal = "portal"

	PortalTransportMkcp  = "mkcp"
	PortalTransportXHTTP = "xhttp"
)

type tunnelProbeStatus struct {
	Status    string
	Message   string
	Timestamp time.Time
}

var tunnelProbeStatuses sync.Map

type TunnelService struct {
}

func (s *TunnelService) GetTunnels(userId int) ([]*model.Tunnel, error) {
	return s.GetTunnelsTraced(userId, "-")
}

func (s *TunnelService) GetTunnelsTraced(userId int, traceID string) ([]*model.Tunnel, error) {
	started := time.Now()
	logger.Infof("[tunnel-trace] trace=%s event=service.start user_id=%d", traceID, userId)

	db := database.GetDB()
	var tunnels []*model.Tunnel
	dbStarted := time.Now()
	err := db.Model(model.Tunnel{}).Where("user_id = ?", userId).Find(&tunnels).Error
	dbElapsed := time.Since(dbStarted)
	logger.Infof("[tunnel-trace] trace=%s event=db.end user_id=%d count=%d elapsed_ms=%.3f error=%t", traceID, userId, len(tunnels), float64(dbElapsed.Microseconds())/1000, err != nil && err != gorm.ErrRecordNotFound)
	if err != nil && err != gorm.ErrRecordNotFound {
		logger.Infof("[tunnel-trace] trace=%s event=service.end success=false total_ms=%.3f", traceID, float64(time.Since(started).Microseconds())/1000)
		return nil, err
	}

	hydrateStarted := time.Now()
	for _, tunnel := range tunnels {
		s.normalizeTunnel(tunnel)
		s.applyProbeStatus(tunnel)
		logger.Infof(
			"[tunnel-trace] trace=%s event=tunnel.info id=%d enable=%t mode=%s portal_transport=%s protocol=%s network=%s listen=%s:%d target=%s:%d remote=%s:%d portal_listen=%d xhttp_path=%q probe_status=%s",
			traceID,
			tunnel.Id,
			tunnel.Enable,
			tunnel.Mode,
			tunnel.PortalTransport,
			tunnel.Protocol,
			tunnel.Network,
			tunnel.Listen,
			tunnel.ListenPort,
			tunnel.TargetAddress,
			tunnel.TargetPort,
			tunnel.RemoteAddress,
			tunnel.RemotePort,
			tunnel.PortalListenPort,
			tunnel.XHttpPath,
			tunnel.Status,
		)
	}
	hydrateElapsed := time.Since(hydrateStarted)
	logger.Infof("[tunnel-trace] trace=%s event=hydrate.end count=%d elapsed_ms=%.3f", traceID, len(tunnels), float64(hydrateElapsed.Microseconds())/1000)
	logger.Infof("[tunnel-trace] trace=%s event=service.end success=true count=%d db_ms=%.3f hydrate_ms=%.3f total_ms=%.3f", traceID, len(tunnels), float64(dbElapsed.Microseconds())/1000, float64(hydrateElapsed.Microseconds())/1000, float64(time.Since(started).Microseconds())/1000)
	return tunnels, nil
}

func (s *TunnelService) GetAllEnabledTunnels() ([]*model.Tunnel, error) {
	db := database.GetDB()
	var tunnels []*model.Tunnel
	err := db.Model(model.Tunnel{}).Where("enable = ?", true).Find(&tunnels).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	for _, tunnel := range tunnels {
		s.normalizeTunnel(tunnel)
	}
	return tunnels, nil
}

func (s *TunnelService) checkPortalUUIDExist(tunnel *model.Tunnel, ignoreId int) (bool, error) {
	if tunnel.Mode != TunnelModePortal {
		return false, nil
	}
	db := database.GetDB()
	query := db.Model(model.Tunnel{}).Where("mode = ?", TunnelModePortal)
	if ignoreId > 0 {
		query = query.Where("id != ?", ignoreId)
	}
	var tunnels []*model.Tunnel
	if err := query.Find(&tunnels).Error; err != nil {
		return false, err
	}
	for _, existing := range tunnels {
		normalized, err := model.NormalizeUUID(existing.UUID)
		if err == nil && normalized == tunnel.UUID {
			return true, nil
		}
	}
	return false, nil
}

func (s *TunnelService) normalizeTunnel(tunnel *model.Tunnel) {
	tunnel.Mode = strings.ToLower(strings.TrimSpace(tunnel.Mode))
	tunnel.Protocol = strings.ToLower(strings.TrimSpace(tunnel.Protocol))
	tunnel.Network = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tunnel.Network), " ", ""))
	tunnel.Listen = strings.TrimSpace(tunnel.Listen)
	tunnel.TargetAddress = strings.TrimSpace(tunnel.TargetAddress)
	tunnel.RemoteAddress = strings.TrimSpace(tunnel.RemoteAddress)
	tunnel.UUID = strings.TrimSpace(tunnel.UUID)
	tunnel.PortalTransport = strings.ToLower(strings.TrimSpace(tunnel.PortalTransport))
	tunnel.XHttpPath = strings.TrimSpace(tunnel.XHttpPath)
	if normalizedUUID, err := model.NormalizeUUID(tunnel.UUID); err == nil {
		tunnel.UUID = normalizedUUID
	}
	tunnel.KcpFinalMaskType = strings.TrimSpace(tunnel.KcpFinalMaskType)

	if tunnel.Mode == "" {
		tunnel.Mode = TunnelModeDirect
	}
	if tunnel.PortalTransport == "" {
		tunnel.PortalTransport = PortalTransportMkcp
	}
	if tunnel.Protocol == "" {
		if tunnel.Mode == TunnelModePortal {
			if tunnel.PortalTransport == PortalTransportXHTTP {
				tunnel.Protocol = "vless"
			} else {
				tunnel.Protocol = "vmess"
			}
		} else {
			tunnel.Protocol = "vless"
		}
	}
	if tunnel.PortalListenPort == 0 {
		if tunnel.PortalTransport == PortalTransportMkcp {
			tunnel.PortalListenPort = tunnel.RemotePort
		}
	}
	if tunnel.XHttpPath == "" {
		tunnel.XHttpPath = "/portal-xhttp"
	}
	if tunnel.Network == "" {
		tunnel.Network = "tcp"
	}
	if tunnel.KcpFinalMaskType == "" {
		tunnel.KcpFinalMaskType = "none"
	}
	if tunnel.KcpMtu == 0 {
		tunnel.KcpMtu = 1350
	}
	if tunnel.KcpTti == 0 {
		tunnel.KcpTti = 20
	}
	if tunnel.KcpUplinkCapacity == 0 {
		tunnel.KcpUplinkCapacity = 5
	}
	if tunnel.KcpDownlinkCapacity == 0 {
		tunnel.KcpDownlinkCapacity = 20
	}
	if tunnel.KcpReadBufferSize == 0 {
		tunnel.KcpReadBufferSize = 2
	}
	if tunnel.KcpWriteBufferSize == 0 {
		tunnel.KcpWriteBufferSize = 2
	}
}

func (s *TunnelService) checkTunnel(tunnel *model.Tunnel) error {
	if tunnel.Mode != TunnelModeDirect && tunnel.Mode != TunnelModePortal {
		return common.NewError("隧道模式仅支持 direct 或 portal:", tunnel.Mode)
	}
	if tunnel.ListenPort <= 0 || tunnel.ListenPort > 65535 {
		return common.NewError("本地监听端口不合法:", tunnel.ListenPort)
	}
	if tunnel.TargetPort <= 0 || tunnel.TargetPort > 65535 {
		return common.NewError("目标端口不合法:", tunnel.TargetPort)
	}
	if tunnel.TargetAddress == "" {
		return common.NewError("目标地址不能为空")
	}
	normalizedUUID, err := model.NormalizeUUID(tunnel.UUID)
	if err != nil {
		return common.NewError("UUID 不合法: ", err)
	}
	tunnel.UUID = normalizedUUID
	if tunnel.Network != "tcp" && tunnel.Network != "udp" && tunnel.Network != "tcp,udp" {
		return common.NewError("本地入口网络仅支持 tcp、udp 或 tcp,udp:", tunnel.Network)
	}

	if tunnel.Mode == TunnelModePortal {
		switch tunnel.PortalTransport {
		case PortalTransportMkcp:
			if tunnel.Protocol != "vmess" {
				return common.NewError("Portal mKCP 模式只支持 VMess")
			}
			if tunnel.RemotePort <= 0 || tunnel.RemotePort > 65535 {
				return common.NewError("Portal mKCP 端口不合法:", tunnel.RemotePort)
			}
		case PortalTransportXHTTP:
			if tunnel.Protocol != "vless" {
				return common.NewError("Portal XHTTP 模式只支持 VLESS")
			}
			if tunnel.RemoteAddress == "" {
				return common.NewError("Portal XHTTP CDN 域名不能为空")
			}
			if tunnel.RemotePort <= 0 || tunnel.RemotePort > 65535 {
				return common.NewError("Portal XHTTP 公网端口不合法:", tunnel.RemotePort)
			}
			if tunnel.PortalListenPort <= 0 || tunnel.PortalListenPort > 65535 {
				return common.NewError("Portal XHTTP 本地监听端口不合法:", tunnel.PortalListenPort)
			}
			if !strings.HasPrefix(tunnel.XHttpPath, "/") {
				return common.NewError("Portal XHTTP 路径必须以 / 开头")
			}
		default:
			return common.NewError("Portal 传输仅支持 mkcp 或 xhttp:", tunnel.PortalTransport)
		}
	} else {
		if tunnel.RemotePort <= 0 || tunnel.RemotePort > 65535 {
			return common.NewError("远端端口不合法:", tunnel.RemotePort)
		}
		if tunnel.RemoteAddress == "" {
			return common.NewError("远端地址不能为空")
		}
		if tunnel.Protocol != "vless" && tunnel.Protocol != "vmess" {
			return common.NewError("隧道协议仅支持 vless 或 vmess:", tunnel.Protocol)
		}
	}

	if tunnel.Mode != TunnelModePortal || tunnel.PortalTransport == PortalTransportMkcp {
		if tunnel.KcpTti < 10 || tunnel.KcpTti > 5000 {
			return common.NewError("mKCP tti 必须在 10 到 5000 之间")
		}
		if tunnel.KcpMtu <= 0 || tunnel.KcpUplinkCapacity <= 0 || tunnel.KcpDownlinkCapacity <= 0 || tunnel.KcpReadBufferSize <= 0 || tunnel.KcpWriteBufferSize <= 0 {
			return common.NewError("mKCP 参数必须大于 0")
		}
		if !isValidKcpFinalMaskType(tunnel.KcpFinalMaskType) {
			return common.NewError("FinalMask UDP header 不支持:", tunnel.KcpFinalMaskType)
		}
	}
	return nil
}

func isValidKcpFinalMaskType(maskType string) bool {
	switch strings.TrimSpace(maskType) {
	case "", "none", "header-srtp", "header-utp", "header-wechat", "header-dtls", "header-wireguard":
		return true
	default:
		return false
	}
}

func (s *TunnelService) AddTunnel(tunnel *model.Tunnel) error {
	s.normalizeTunnel(tunnel)
	if err := s.checkTunnel(tunnel); err != nil {
		return err
	}
	if err := checkTunnelListenerConflicts(tunnel, 0); err != nil {
		return err
	}
	exist, err := s.checkPortalUUIDExist(tunnel, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("Portal UUID 已被其他隧道使用:", tunnel.UUID)
	}
	db := database.GetDB()
	return db.Save(tunnel).Error
}

func (s *TunnelService) DelTunnel(id int, userId int) error {
	db := database.GetDB()
	result := db.Where("id = ? and user_id = ?", id, userId).Delete(model.Tunnel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return common.NewError("隧道不存在或无权限:", id)
	}
	tunnelProbeStatuses.Delete(id)
	return nil
}

func (s *TunnelService) GetTunnel(id int, userId int) (*model.Tunnel, error) {
	db := database.GetDB()
	tunnel := &model.Tunnel{}
	err := db.Model(model.Tunnel{}).Where("user_id = ?", userId).First(tunnel, id).Error
	if err != nil {
		return nil, err
	}
	s.normalizeTunnel(tunnel)
	s.applyProbeStatus(tunnel)
	return tunnel, nil
}

func (s *TunnelService) UpdateTunnel(tunnel *model.Tunnel, userId int) error {
	s.normalizeTunnel(tunnel)
	if err := s.checkTunnel(tunnel); err != nil {
		return err
	}
	if err := checkTunnelListenerConflicts(tunnel, tunnel.Id); err != nil {
		return err
	}
	exist, err := s.checkPortalUUIDExist(tunnel, tunnel.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("Portal UUID 已被其他隧道使用:", tunnel.UUID)
	}

	oldTunnel, err := s.GetTunnel(tunnel.Id, userId)
	if err != nil {
		return err
	}

	oldTunnel.Enable = tunnel.Enable
	oldTunnel.Mode = tunnel.Mode
	oldTunnel.Remark = tunnel.Remark
	oldTunnel.Listen = tunnel.Listen
	oldTunnel.ListenPort = tunnel.ListenPort
	oldTunnel.Network = tunnel.Network
	oldTunnel.TargetAddress = tunnel.TargetAddress
	oldTunnel.TargetPort = tunnel.TargetPort
	oldTunnel.RemoteAddress = tunnel.RemoteAddress
	oldTunnel.RemotePort = tunnel.RemotePort
	oldTunnel.Protocol = tunnel.Protocol
	oldTunnel.UUID = tunnel.UUID
	oldTunnel.PortalTransport = tunnel.PortalTransport
	oldTunnel.PortalListenPort = tunnel.PortalListenPort
	oldTunnel.XHttpPath = tunnel.XHttpPath
	oldTunnel.KcpFinalMaskType = tunnel.KcpFinalMaskType
	oldTunnel.KcpMtu = tunnel.KcpMtu
	oldTunnel.KcpTti = tunnel.KcpTti
	oldTunnel.KcpUplinkCapacity = tunnel.KcpUplinkCapacity
	oldTunnel.KcpDownlinkCapacity = tunnel.KcpDownlinkCapacity
	oldTunnel.KcpCongestion = tunnel.KcpCongestion
	oldTunnel.KcpReadBufferSize = tunnel.KcpReadBufferSize
	oldTunnel.KcpWriteBufferSize = tunnel.KcpWriteBufferSize

	db := database.GetDB()
	tunnelProbeStatuses.Delete(tunnel.Id)
	return db.Save(oldTunnel).Error
}

func (s *TunnelService) applyProbeStatus(tunnel *model.Tunnel) {
	tunnel.Status = "not_tested"
	tunnel.StatusMessage = "尚未进行 TCP 探测"
	tunnel.ProbeTime = ""
	if status, ok := tunnelProbeStatuses.Load(tunnel.Id); ok {
		probe := status.(tunnelProbeStatus)
		tunnel.Status = probe.Status
		tunnel.StatusMessage = probe.Message
		tunnel.ProbeTime = probe.Timestamp.Local().Format("2006-01-02 15:04:05")
	}
}

func (s *TunnelService) ProbeTunnel(id int, userId int) (*model.Tunnel, error) {
	tunnel, err := s.GetTunnel(id, userId)
	if err != nil {
		return nil, err
	}
	if !tunnel.Enable {
		probe := tunnelProbeStatus{Status: "not_tested", Message: "隧道未启用", Timestamp: time.Now()}
		tunnelProbeStatuses.Store(tunnel.Id, probe)
		s.applyProbeStatus(tunnel)
		return tunnel, nil
	}
	if tunnel.Network == "udp" {
		probe := tunnelProbeStatus{Status: "not_tested", Message: "纯 UDP 隧道未执行 TCP 探测", Timestamp: time.Now()}
		tunnelProbeStatuses.Store(tunnel.Id, probe)
		s.applyProbeStatus(tunnel)
		return tunnel, nil
	}

	host := tunnel.Listen
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(tunnel.ListenPort)), 2*time.Second)
	if err != nil {
		probe := tunnelProbeStatus{Status: "failed", Message: "TCP 入口连接失败: " + err.Error(), Timestamp: time.Now()}
		tunnelProbeStatuses.Store(tunnel.Id, probe)
		s.applyProbeStatus(tunnel)
		return tunnel, nil
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	probe := tunnelProbeStatus{Status: "success", Message: "TCP 连接探测成功；该结果不是实时在线状态", Timestamp: time.Now()}
	if readErr != nil {
		if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
			// A listener with no reverse worker is closed immediately by Xray. A
			// connection that remains open through the deadline is useful evidence
			// that the reverse path and the B-side target accepted the stream.
		} else if readErr == io.EOF {
			probe = tunnelProbeStatus{Status: "failed", Message: "TCP 连接被远端立即关闭", Timestamp: time.Now()}
		} else {
			probe = tunnelProbeStatus{Status: "failed", Message: "TCP 连接异常: " + readErr.Error(), Timestamp: time.Now()}
		}
	}
	tunnelProbeStatuses.Store(tunnel.Id, probe)
	s.applyProbeStatus(tunnel)
	return tunnel, nil
}

func (s *TunnelService) genXrayInboundConfig(tunnel *model.Tunnel) (*xray.InboundConfig, error) {
	listen := tunnel.Listen
	if listen != "" {
		listen = fmt.Sprintf("\"%v\"", listen)
	}

	settings, err := json.Marshal(map[string]interface{}{
		"address": tunnel.TargetAddress,
		"port":    tunnel.TargetPort,
		"network": tunnel.Network,
	})
	if err != nil {
		return nil, err
	}

	return &xray.InboundConfig{
		Listen:   json_util.RawMessage(listen),
		Port:     tunnel.ListenPort,
		Protocol: "dokodemo-door",
		Settings: json_util.RawMessage(settings),
		Tag:      tunnel.InboundTag(),
	}, nil
}

func buildKcpStreamSettings(tunnel *model.Tunnel) map[string]interface{} {
	streamSettings := map[string]interface{}{
		"network":  "mkcp",
		"security": "none",
		"kcpSettings": map[string]interface{}{
			"mtu":              tunnel.KcpMtu,
			"tti":              tunnel.KcpTti,
			"uplinkCapacity":   tunnel.KcpUplinkCapacity,
			"downlinkCapacity": tunnel.KcpDownlinkCapacity,
			"congestion":       tunnel.KcpCongestion,
			"readBufferSize":   tunnel.KcpReadBufferSize,
			"writeBufferSize":  tunnel.KcpWriteBufferSize,
		},
	}
	if finalmask := buildKcpFinalMask(tunnel.KcpFinalMaskType); finalmask != nil {
		streamSettings["finalmask"] = finalmask
	}
	return streamSettings
}

func (s *TunnelService) genXrayOutboundConfig(tunnel *model.Tunnel) (json.RawMessage, error) {
	user := map[string]interface{}{
		"id": tunnel.UUID,
	}
	if tunnel.Protocol == "vmess" {
		user["alterId"] = 0
		user["security"] = "auto"
	} else {
		user["encryption"] = "none"
	}

	outbound := map[string]interface{}{
		"tag":      tunnel.OutboundTag(),
		"protocol": tunnel.Protocol,
		"settings": map[string]interface{}{
			"vnext": []interface{}{
				map[string]interface{}{
					"address": tunnel.RemoteAddress,
					"port":    tunnel.RemotePort,
					"users": []interface{}{
						user,
					},
				},
			},
		},
		"streamSettings": buildKcpStreamSettings(tunnel),
	}

	data, err := json.Marshal(outbound)
	return json.RawMessage(data), err
}

func (s *TunnelService) genXrayPortalInboundConfig(tunnel *model.Tunnel) (*xray.InboundConfig, error) {
	if tunnel.PortalTransport == PortalTransportXHTTP {
		settings, err := json.Marshal(map[string]interface{}{
			"clients": []map[string]interface{}{
				{
					"id":    tunnel.UUID,
					"email": fmt.Sprintf("portal-%d", tunnel.Id),
				},
			},
			"decryption": "none",
		})
		if err != nil {
			return nil, err
		}
		streamSettings, err := json.Marshal(map[string]interface{}{
			"network":  "xhttp",
			"security": "none",
			"xhttpSettings": map[string]interface{}{
				"path": tunnel.XHttpPath,
				"host": "",
				"mode": "auto",
			},
		})
		if err != nil {
			return nil, err
		}
		return &xray.InboundConfig{
			Listen:         json_util.RawMessage(`"127.0.0.1"`),
			Port:           tunnel.PortalListenPort,
			Protocol:       "vless",
			Settings:       json_util.RawMessage(settings),
			StreamSettings: json_util.RawMessage(streamSettings),
			Tag:            tunnel.PortalInboundTag(),
		}, nil
	}

	settings, err := json.Marshal(map[string]interface{}{
		"clients": []map[string]interface{}{
			{
				"id":      tunnel.UUID,
				"alterId": 0,
				"email":   fmt.Sprintf("portal-%d", tunnel.Id),
			},
		},
	})
	if err != nil {
		return nil, err
	}
	streamSettings, err := json.Marshal(buildKcpStreamSettings(tunnel))
	if err != nil {
		return nil, err
	}
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(`"0.0.0.0"`),
		Port:           tunnel.RemotePort,
		Protocol:       "vmess",
		Settings:       json_util.RawMessage(settings),
		StreamSettings: json_util.RawMessage(streamSettings),
		Tag:            tunnel.PortalInboundTag(),
	}, nil
}

func buildKcpFinalMask(maskType string) map[string]interface{} {
	maskType = strings.TrimSpace(maskType)
	if maskType == "" || maskType == "none" {
		return nil
	}
	return map[string]interface{}{
		"udp": []map[string]interface{}{
			{
				"type":     maskType,
				"settings": map[string]interface{}{},
			},
		},
	}
}

func (s *TunnelService) genXrayRoutingRule(tunnel *model.Tunnel) (json.RawMessage, error) {
	rule := map[string]interface{}{
		"type":        "field",
		"inboundTag":  []string{tunnel.InboundTag()},
		"outboundTag": tunnel.OutboundTag(),
	}
	data, err := json.Marshal(rule)
	return json.RawMessage(data), err
}

func (s *TunnelService) genXrayPortalRoutingRules(tunnel *model.Tunnel) ([]json.RawMessage, error) {
	rules := []map[string]interface{}{
		{
			"type":        "field",
			"domain":      []string{"full:" + tunnel.ReverseDomain()},
			"outboundTag": tunnel.PortalTag(),
		},
		{
			"type":        "field",
			"inboundTag":  []string{tunnel.InboundTag()},
			"outboundTag": tunnel.PortalTag(),
		},
	}
	result := make([]json.RawMessage, 0, len(rules))
	for _, rule := range rules {
		data, err := json.Marshal(rule)
		if err != nil {
			return nil, err
		}
		result = append(result, json.RawMessage(data))
	}
	return result, nil
}

func (s *TunnelService) ApplyToXrayConfig(xrayConfig *xray.Config) error {
	tunnels, err := s.GetAllEnabledTunnels()
	if err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		if err := s.applyTunnelToXrayConfig(xrayConfig, tunnel); err != nil {
			return err
		}
	}
	return nil
}

func (s *TunnelService) applyTunnelToXrayConfig(xrayConfig *xray.Config, tunnel *model.Tunnel) error {
	inboundConfig, err := s.genXrayInboundConfig(tunnel)
	if err != nil {
		return err
	}
	xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)

	if tunnel.Mode == TunnelModePortal {
		portalInbound, err := s.genXrayPortalInboundConfig(tunnel)
		if err != nil {
			return err
		}
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *portalInbound)
		if err := appendReversePortal(&xrayConfig.Reverse, tunnel); err != nil {
			return err
		}
		rules, err := s.genXrayPortalRoutingRules(tunnel)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if err := appendRoutingRule(&xrayConfig.RouterConfig, rule); err != nil {
				return err
			}
		}
		return nil
	}

	outboundConfig, err := s.genXrayOutboundConfig(tunnel)
	if err != nil {
		return err
	}
	if err := appendRawJSONArray(&xrayConfig.OutboundConfigs, outboundConfig); err != nil {
		return err
	}

	routingRule, err := s.genXrayRoutingRule(tunnel)
	if err != nil {
		return err
	}
	return appendRoutingRule(&xrayConfig.RouterConfig, routingRule)
}

// BuildTunnelFixtureConfig uses the same tunnel generator as the running
// panel without reading the database. It is intended for reproducible
// cross-repository Portal smoke tests.
func BuildTunnelFixtureConfig(tunnel *model.Tunnel) (*xray.Config, error) {
	service := &TunnelService{}
	service.normalizeTunnel(tunnel)
	if err := service.checkTunnel(tunnel); err != nil {
		return nil, err
	}
	config := &xray.Config{
		LogConfig:       json_util.RawMessage(`{"loglevel":"warning"}`),
		RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom","settings":{}},{"tag":"blocked","protocol":"blackhole","settings":{}}]`),
	}
	if err := service.applyTunnelToXrayConfig(config, tunnel); err != nil {
		return nil, err
	}
	return config, nil
}

func appendRawJSONArray(raw *json_util.RawMessage, item json.RawMessage) error {
	items := make([]json.RawMessage, 0)
	trimmed := bytes.TrimSpace([]byte(*raw))
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return common.NewError("outbounds 配置不是数组:", err)
		}
	}
	items = append(items, item)
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	*raw = json_util.RawMessage(data)
	return nil
}

func appendReversePortal(raw *json_util.RawMessage, tunnel *model.Tunnel) error {
	reverse := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace([]byte(*raw))
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &reverse); err != nil {
			return common.NewError("reverse 配置不是对象:", err)
		}
	}
	portals := make([]json.RawMessage, 0)
	if rawPortals, ok := reverse["portals"]; ok && len(bytes.TrimSpace(rawPortals)) > 0 {
		if err := json.Unmarshal(rawPortals, &portals); err != nil {
			return common.NewError("reverse.portals 配置不是数组:", err)
		}
	}
	portal, err := json.Marshal(map[string]interface{}{
		"tag":    tunnel.PortalTag(),
		"domain": tunnel.ReverseDomain(),
	})
	if err != nil {
		return err
	}
	portals = append(portals, json.RawMessage(portal))
	portalData, err := json.Marshal(portals)
	if err != nil {
		return err
	}
	reverse["portals"] = json.RawMessage(portalData)
	data, err := json.Marshal(reverse)
	if err != nil {
		return err
	}
	*raw = json_util.RawMessage(data)
	return nil
}

func appendRoutingRule(raw *json_util.RawMessage, rule json.RawMessage) error {
	routing := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace([]byte(*raw))
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &routing); err != nil {
			return common.NewError("routing 配置不是对象:", err)
		}
	}

	rules := make([]json.RawMessage, 0)
	if rawRules, ok := routing["rules"]; ok && len(bytes.TrimSpace(rawRules)) > 0 {
		if err := json.Unmarshal(rawRules, &rules); err != nil {
			return common.NewError("routing.rules 配置不是数组:", err)
		}
	}
	// Tunnel routing rules must take precedence over generic rules such as
	// geoip:private -> blocked. Otherwise dokodemo-door targets like
	// 127.0.0.1 can be blackholed before the tunnel outbound is selected.
	rules = append([]json.RawMessage{rule}, rules...)
	rulesData, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	routing["rules"] = json.RawMessage(rulesData)

	data, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	*raw = json_util.RawMessage(data)
	return nil
}
