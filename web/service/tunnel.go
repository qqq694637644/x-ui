package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/common"
	"x-ui/util/json_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

type TunnelService struct {
}

func (s *TunnelService) GetTunnels(userId int) ([]*model.Tunnel, error) {
	db := database.GetDB()
	var tunnels []*model.Tunnel
	err := db.Model(model.Tunnel{}).Where("user_id = ?", userId).Find(&tunnels).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
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

func (s *TunnelService) normalizeTunnel(tunnel *model.Tunnel) {
	tunnel.Protocol = strings.ToLower(strings.TrimSpace(tunnel.Protocol))
	tunnel.Network = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tunnel.Network), " ", ""))
	tunnel.KcpHeaderType = strings.ToLower(strings.TrimSpace(tunnel.KcpHeaderType))

	if tunnel.Protocol == "" {
		tunnel.Protocol = "vless"
	}
	if tunnel.Network == "" {
		tunnel.Network = "tcp"
	}
	if tunnel.KcpHeaderType == "" {
		tunnel.KcpHeaderType = "none"
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
	if tunnel.ListenPort <= 0 || tunnel.ListenPort > 65535 {
		return common.NewError("本地监听端口不合法:", tunnel.ListenPort)
	}
	if tunnel.TargetPort <= 0 || tunnel.TargetPort > 65535 {
		return common.NewError("目标端口不合法:", tunnel.TargetPort)
	}
	if tunnel.RemotePort <= 0 || tunnel.RemotePort > 65535 {
		return common.NewError("远端端口不合法:", tunnel.RemotePort)
	}
	if tunnel.TargetAddress == "" {
		return common.NewError("目标地址不能为空")
	}
	if tunnel.RemoteAddress == "" {
		return common.NewError("远端地址不能为空")
	}
	if tunnel.UUID == "" {
		return common.NewError("UUID 不能为空")
	}
	if tunnel.Protocol != "vless" && tunnel.Protocol != "vmess" {
		return common.NewError("隧道协议仅支持 vless 或 vmess:", tunnel.Protocol)
	}
	if tunnel.Network != "tcp" && tunnel.Network != "udp" && tunnel.Network != "tcp,udp" {
		return common.NewError("本地入口网络仅支持 tcp、udp 或 tcp,udp:", tunnel.Network)
	}
	if tunnel.KcpHeaderType != "none" && tunnel.KcpHeaderType != "srtp" && tunnel.KcpHeaderType != "utp" && tunnel.KcpHeaderType != "wechat-video" && tunnel.KcpHeaderType != "dtls" && tunnel.KcpHeaderType != "wireguard" {
		return common.NewError("mKCP 伪装类型不支持:", tunnel.KcpHeaderType)
	}
	if tunnel.KcpTti < 10 || tunnel.KcpTti > 5000 {
		return common.NewError("mKCP tti 必须在 10 到 5000 之间")
	}
	if tunnel.KcpMtu <= 0 || tunnel.KcpUplinkCapacity <= 0 || tunnel.KcpDownlinkCapacity <= 0 || tunnel.KcpReadBufferSize <= 0 || tunnel.KcpWriteBufferSize <= 0 {
		return common.NewError("mKCP 参数必须大于 0")
	}
	return nil
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
	db := database.GetDB()
	return db.Save(tunnel).Error
}

func (s *TunnelService) DelTunnel(id int) error {
	db := database.GetDB()
	return db.Delete(model.Tunnel{}, id).Error
}

func (s *TunnelService) GetTunnel(id int) (*model.Tunnel, error) {
	db := database.GetDB()
	tunnel := &model.Tunnel{}
	err := db.Model(model.Tunnel{}).First(tunnel, id).Error
	if err != nil {
		return nil, err
	}
	return tunnel, nil
}

func (s *TunnelService) UpdateTunnel(tunnel *model.Tunnel) error {
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

	oldTunnel, err := s.GetTunnel(tunnel.Id)
	if err != nil {
		return err
	}

	oldTunnel.Enable = tunnel.Enable
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
	oldTunnel.KcpHeaderType = tunnel.KcpHeaderType
	oldTunnel.KcpSeed = tunnel.KcpSeed
	oldTunnel.KcpMtu = tunnel.KcpMtu
	oldTunnel.KcpTti = tunnel.KcpTti
	oldTunnel.KcpUplinkCapacity = tunnel.KcpUplinkCapacity
	oldTunnel.KcpDownlinkCapacity = tunnel.KcpDownlinkCapacity
	oldTunnel.KcpCongestion = tunnel.KcpCongestion
	oldTunnel.KcpReadBufferSize = tunnel.KcpReadBufferSize
	oldTunnel.KcpWriteBufferSize = tunnel.KcpWriteBufferSize

	db := database.GetDB()
	return db.Save(oldTunnel).Error
}

func (s *TunnelService) ApplyToXrayConfig(xrayConfig *xray.Config) error {
	tunnels, err := s.GetAllEnabledTunnels()
	if err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		inboundConfig, err := tunnel.GenXrayInboundConfig()
		if err != nil {
			return err
		}
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)

		outboundConfig, err := tunnel.GenXrayOutboundConfig()
		if err != nil {
			return err
		}
		if err := appendRawJSONArray(&xrayConfig.OutboundConfigs, outboundConfig); err != nil {
			return err
		}

		routingRule, err := tunnel.GenXrayRoutingRule()
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
	rules = append(rules, rule)
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
