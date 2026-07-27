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
	"x-ui/util/common"
	"x-ui/util/json_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

const (
	TunnelModeDirect = "direct"
	TunnelModePortal = "portal"
)

type tunnelProbeStatus struct {
	Status  string
	Message string
}

var tunnelProbeStatuses sync.Map

type TunnelService struct {
}

func (s *TunnelService) GetTunnels(userId int) ([]*model.Tunnel, error) {
	db := database.GetDB()
	var tunnels []*model.Tunnel
	err := db.Model(model.Tunnel{}).Where("user_id = ?", userId).Find(&tunnels).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	for _, tunnel := range tunnels {
		s.normalizeTunnel(tunnel)
		s.applyProbeStatus(tunnel)
	}
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

func (s *TunnelService) checkListenPortExist(port int, ignoreId int) (bool, error) {
	db := database.GetDB()
	var inboundCount int64
	err := db.Model(model.Inbound{}).Where("port = ?", port).Count(&inboundCount).Error
	if err != nil {
		return false, err
	}
	if inboundCount > 0 {
		return true, nil
	}

	tunnelDB := db.Model(model.Tunnel{}).Where("listen_port = ?", port)
	if ignoreId > 0 {
		tunnelDB = tunnelDB.Where("id != ?", ignoreId)
	}
	var tunnelCount int64
	err = tunnelDB.Count(&tunnelCount).Error
	if err != nil {
		return false, err
	}
	return tunnelCount > 0, nil
}

func (s *TunnelService) checkPortalPortExist(tunnel *model.Tunnel, ignoreId int) (bool, error) {
	if tunnel.Mode != TunnelModePortal {
		return false, nil
	}
	if tunnel.ListenPort == tunnel.RemotePort && (tunnel.Network == "udp" || tunnel.Network == "tcp,udp") {
		return true, nil
	}

	db := database.GetDB()
	portalDB := db.Model(model.Tunnel{}).
		Where("mode = ? AND remote_port = ?", TunnelModePortal, tunnel.RemotePort)
	if ignoreId > 0 {
		portalDB = portalDB.Where("id != ?", ignoreId)
	}
	var portalCount int64
	if err := portalDB.Count(&portalCount).Error; err != nil {
		return false, err
	}
	if portalCount > 0 {
		return true, nil
	}

	udpTunnelDB := db.Model(model.Tunnel{}).
		Where("listen_port = ? AND network IN ?", tunnel.RemotePort, []string{"udp", "tcp,udp"})
	if ignoreId > 0 {
		udpTunnelDB = udpTunnelDB.Where("id != ?", ignoreId)
	}
	var udpTunnelCount int64
	if err := udpTunnelDB.Count(&udpTunnelCount).Error; err != nil {
		return false, err
	}
	if udpTunnelCount > 0 {
		return true, nil
	}

	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).Where("port = ?", tunnel.RemotePort).Find(&inbounds).Error; err != nil {
		return false, err
	}
	for _, inbound := range inbounds {
		stream := map[string]interface{}{}
		if strings.TrimSpace(inbound.StreamSettings) == "" {
			continue
		}
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
			return false, common.NewError("无法判断入站端口协议:", err)
		}
		network, _ := stream["network"].(string)
		switch strings.ToLower(network) {
		case "mkcp", "kcp", "udp":
			return true, nil
		}
	}
	return false, nil
}

func (s *TunnelService) checkPortalUUIDExist(tunnel *model.Tunnel, ignoreId int) (bool, error) {
	if tunnel.Mode != TunnelModePortal {
		return false, nil
	}
	db := database.GetDB()
	query := db.Model(model.Tunnel{}).
		Where("mode = ? AND uuid = ?", TunnelModePortal, tunnel.UUID)
	if ignoreId > 0 {
		query = query.Where("id != ?", ignoreId)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *TunnelService) normalizeTunnel(tunnel *model.Tunnel) {
	tunnel.Mode = strings.ToLower(strings.TrimSpace(tunnel.Mode))
	tunnel.Protocol = strings.ToLower(strings.TrimSpace(tunnel.Protocol))
	tunnel.Network = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tunnel.Network), " ", ""))
	tunnel.Listen = strings.TrimSpace(tunnel.Listen)
	tunnel.TargetAddress = strings.TrimSpace(tunnel.TargetAddress)
	tunnel.RemoteAddress = strings.TrimSpace(tunnel.RemoteAddress)
	tunnel.UUID = strings.TrimSpace(tunnel.UUID)
	tunnel.KcpFinalMaskType = strings.TrimSpace(tunnel.KcpFinalMaskType)

	if tunnel.Mode == "" {
		tunnel.Mode = TunnelModeDirect
	}
	if tunnel.Protocol == "" {
		if tunnel.Mode == TunnelModePortal {
			tunnel.Protocol = "vmess"
		} else {
			tunnel.Protocol = "vless"
		}
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
	if tunnel.UUID == "" {
		return common.NewError("UUID 不能为空")
	}
	if tunnel.Network != "tcp" && tunnel.Network != "udp" && tunnel.Network != "tcp,udp" {
		return common.NewError("本地入口网络仅支持 tcp、udp 或 tcp,udp:", tunnel.Network)
	}

	if tunnel.Mode == TunnelModePortal {
		if tunnel.Protocol != "vmess" {
			return common.NewError("Portal 模式只支持 VMess")
		}
		if tunnel.RemotePort <= 0 || tunnel.RemotePort > 65535 {
			return common.NewError("Portal mKCP 端口不合法:", tunnel.RemotePort)
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

	if tunnel.KcpTti < 10 || tunnel.KcpTti > 5000 {
		return common.NewError("mKCP tti 必须在 10 到 5000 之间")
	}
	if tunnel.KcpMtu <= 0 || tunnel.KcpUplinkCapacity <= 0 || tunnel.KcpDownlinkCapacity <= 0 || tunnel.KcpReadBufferSize <= 0 || tunnel.KcpWriteBufferSize <= 0 {
		return common.NewError("mKCP 参数必须大于 0")
	}
	if !isValidKcpFinalMaskType(tunnel.KcpFinalMaskType) {
		return common.NewError("FinalMask UDP header 不支持:", tunnel.KcpFinalMaskType)
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
	exist, err := s.checkListenPortExist(tunnel.ListenPort, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("本地监听端口已存在:", tunnel.ListenPort)
	}
	exist, err = s.checkPortalPortExist(tunnel, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("Portal UDP 监听端口已存在:", tunnel.RemotePort)
	}
	exist, err = s.checkPortalUUIDExist(tunnel, 0)
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
	exist, err := s.checkListenPortExist(tunnel.ListenPort, tunnel.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("本地监听端口已存在:", tunnel.ListenPort)
	}
	exist, err = s.checkPortalPortExist(tunnel, tunnel.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("Portal UDP 监听端口已存在:", tunnel.RemotePort)
	}
	exist, err = s.checkPortalUUIDExist(tunnel, tunnel.Id)
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
	tunnel.Status = "unknown"
	tunnel.StatusMessage = "尚未探测"
	if status, ok := tunnelProbeStatuses.Load(tunnel.Id); ok {
		probe := status.(tunnelProbeStatus)
		tunnel.Status = probe.Status
		tunnel.StatusMessage = probe.Message
	}
}

func (s *TunnelService) ProbeTunnel(id int, userId int) (*model.Tunnel, error) {
	tunnel, err := s.GetTunnel(id, userId)
	if err != nil {
		return nil, err
	}
	if !tunnel.Enable {
		probe := tunnelProbeStatus{Status: "unknown", Message: "隧道未启用"}
		tunnelProbeStatuses.Store(tunnel.Id, probe)
		s.applyProbeStatus(tunnel)
		return tunnel, nil
	}
	if tunnel.Network == "udp" {
		probe := tunnelProbeStatus{Status: "unknown", Message: "UDP 无通用握手，无法可靠探测"}
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
		probe := tunnelProbeStatus{Status: "disconnected", Message: "入口连接失败: " + err.Error()}
		tunnelProbeStatuses.Store(tunnel.Id, probe)
		s.applyProbeStatus(tunnel)
		return tunnel, nil
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	probe := tunnelProbeStatus{Status: "connected", Message: "TCP 连接已建立并保持"}
	if readErr != nil {
		if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
			// A listener with no reverse worker is closed immediately by Xray. A
			// connection that remains open through the deadline is useful evidence
			// that the reverse path and the B-side target accepted the stream.
		} else if readErr == io.EOF {
			probe = tunnelProbeStatus{Status: "disconnected", Message: "连接被远端立即关闭"}
		} else {
			probe = tunnelProbeStatus{Status: "disconnected", Message: "连接异常: " + readErr.Error()}
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
			continue
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
		if err := appendRoutingRule(&xrayConfig.RouterConfig, routingRule); err != nil {
			return err
		}
	}
	return nil
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
