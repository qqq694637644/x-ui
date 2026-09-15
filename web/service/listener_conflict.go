package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/common"
)

type listenerProtocols uint8

const (
	listenerTCP listenerProtocols = 1 << iota
	listenerUDP
)

type listenerEndpoint struct {
	Address     string
	Port        int
	Protocols   listenerProtocols
	Description string
}

func protocolsFromNetwork(network string) listenerProtocols {
	var protocols listenerProtocols
	for _, item := range strings.Split(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(network), " ", "")), ",") {
		switch item {
		case "tcp":
			protocols |= listenerTCP
		case "udp":
			protocols |= listenerUDP
		}
	}
	return protocols
}

func transportProtocols(network string) listenerProtocols {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "mkcp", "kcp", "quic", "udp":
		return listenerUDP
	case "":
		return listenerTCP
	default:
		return listenerTCP
	}
}

func xhttpUsesHTTP3(stream map[string]interface{}) bool {
	network, _ := stream["network"].(string)
	if !strings.EqualFold(strings.TrimSpace(network), "xhttp") && !strings.EqualFold(strings.TrimSpace(network), "splithttp") {
		return false
	}
	security, _ := stream["security"].(string)
	if !strings.EqualFold(strings.TrimSpace(security), "tls") {
		return false
	}
	tlsSettings, ok := stream["tlsSettings"].(map[string]interface{})
	if !ok {
		return false
	}
	switch alpn := tlsSettings["alpn"].(type) {
	case string:
		values := strings.Split(alpn, ",")
		return len(values) == 1 && values[0] == "h3"
	case []interface{}:
		if len(alpn) != 1 {
			return false
		}
		value, ok := alpn[0].(string)
		return ok && value == "h3"
	case []string:
		return len(alpn) == 1 && alpn[0] == "h3"
	default:
		return false
	}
}

func normalizeListenAddress(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	return strings.Trim(address, "[]")
}

func listenAddressesOverlap(left string, right string) bool {
	left = normalizeListenAddress(left)
	right = normalizeListenAddress(right)
	isWildcard := func(value string) bool {
		return value == "" || value == "*" || value == "0.0.0.0" || value == "::"
	}
	return isWildcard(left) || isWildcard(right) || left == right
}

func endpointsConflict(left listenerEndpoint, right listenerEndpoint) bool {
	return left.Port == right.Port &&
		left.Protocols&right.Protocols != 0 &&
		listenAddressesOverlap(left.Address, right.Address)
}

func protocolLabel(protocols listenerProtocols) string {
	switch protocols {
	case listenerTCP:
		return "TCP"
	case listenerUDP:
		return "UDP"
	case listenerTCP | listenerUDP:
		return "TCP/UDP"
	default:
		return "未知协议"
	}
}

func tunnelListenerEndpoints(tunnel *model.Tunnel) []listenerEndpoint {
	endpoints := []listenerEndpoint{
		{
			Address:     tunnel.Listen,
			Port:        tunnel.ListenPort,
			Protocols:   protocolsFromNetwork(tunnel.Network),
			Description: fmt.Sprintf("隧道 %d 业务入口", tunnel.Id),
		},
	}
	if strings.EqualFold(strings.TrimSpace(tunnel.Mode), TunnelModePortal) {
		if strings.EqualFold(strings.TrimSpace(tunnel.PortalTransport), PortalTransportXHTTP) {
			endpoints = append(endpoints, listenerEndpoint{
				Address:     "127.0.0.1",
				Port:        tunnel.PortalListenPort,
				Protocols:   listenerTCP,
				Description: fmt.Sprintf("隧道 %d Portal XHTTP", tunnel.Id),
			})
		} else {
			endpoints = append(endpoints, listenerEndpoint{
				Address:     "0.0.0.0",
				Port:        tunnel.RemotePort,
				Protocols:   listenerUDP,
				Description: fmt.Sprintf("隧道 %d Portal mKCP", tunnel.Id),
			})
		}
	}
	return endpoints
}

func inboundListenerEndpoint(inbound *model.Inbound) (listenerEndpoint, error) {
	protocols := listenerTCP
	streamNetwork := ""
	if strings.TrimSpace(inbound.StreamSettings) != "" {
		stream := map[string]interface{}{}
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
			return listenerEndpoint{}, common.NewError("无法判断入站传输协议: ", err)
		}
		streamNetwork, _ = stream["network"].(string)
		if xhttpUsesHTTP3(stream) {
			protocols = listenerUDP
		} else {
			protocols = transportProtocols(streamNetwork)
		}
	}

	settings := map[string]interface{}{}
	if strings.TrimSpace(inbound.Settings) != "" {
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return listenerEndpoint{}, common.NewError("无法判断入站业务协议: ", err)
		}
	}
	if network, ok := settings["network"].(string); ok && strings.TrimSpace(network) != "" {
		if parsed := protocolsFromNetwork(network); parsed != 0 {
			protocols = parsed
		}
	}
	if udp, ok := settings["udp"].(bool); ok && udp {
		protocols |= listenerUDP
	}
	if protocols == 0 {
		protocols = listenerTCP
	}

	return listenerEndpoint{
		Address:     inbound.Listen,
		Port:        inbound.Port,
		Protocols:   protocols,
		Description: fmt.Sprintf("普通入站 %d (%s)", inbound.Id, inbound.Tag),
	}, nil
}

func endpointConflictError(requested listenerEndpoint, existing listenerEndpoint) error {
	return common.NewErrorf(
		"监听冲突: %s %s %s:%d 与 %s 冲突",
		requested.Description,
		protocolLabel(requested.Protocols&existing.Protocols),
		requested.Address,
		requested.Port,
		existing.Description,
	)
}

func checkTunnelListenerConflicts(tunnel *model.Tunnel, ignoreID int) error {
	db := database.GetDB()
	requested := tunnelListenerEndpoints(tunnel)
	for left := 0; left < len(requested); left++ {
		for right := left + 1; right < len(requested); right++ {
			if endpointsConflict(requested[left], requested[right]) {
				return endpointConflictError(requested[left], requested[right])
			}
		}
	}

	var tunnels []*model.Tunnel
	query := db.Model(model.Tunnel{})
	if ignoreID > 0 {
		query = query.Where("id != ?", ignoreID)
	}
	if err := query.Find(&tunnels).Error; err != nil {
		return err
	}
	for _, existingTunnel := range tunnels {
		for _, requestedEndpoint := range requested {
			for _, existingEndpoint := range tunnelListenerEndpoints(existingTunnel) {
				if endpointsConflict(requestedEndpoint, existingEndpoint) {
					return endpointConflictError(requestedEndpoint, existingEndpoint)
				}
			}
		}
	}

	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		existingEndpoint, err := inboundListenerEndpoint(inbound)
		if err != nil {
			return err
		}
		for _, requestedEndpoint := range requested {
			if endpointsConflict(requestedEndpoint, existingEndpoint) {
				return endpointConflictError(requestedEndpoint, existingEndpoint)
			}
		}
	}
	return nil
}

func checkInboundListenerConflicts(inbound *model.Inbound, ignoreID int) error {
	requested, err := inboundListenerEndpoint(inbound)
	if err != nil {
		return err
	}
	var inbounds []*model.Inbound
	query := database.GetDB().Model(model.Inbound{})
	if ignoreID > 0 {
		query = query.Where("id != ?", ignoreID)
	}
	if err := query.Find(&inbounds).Error; err != nil {
		return err
	}
	for _, existingInbound := range inbounds {
		existing, err := inboundListenerEndpoint(existingInbound)
		if err != nil {
			return err
		}
		if endpointsConflict(requested, existing) {
			return endpointConflictError(requested, existing)
		}
	}

	var tunnels []*model.Tunnel
	if err := database.GetDB().Model(model.Tunnel{}).Find(&tunnels).Error; err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		for _, existing := range tunnelListenerEndpoints(tunnel) {
			if endpointsConflict(requested, existing) {
				return endpointConflictError(requested, existing)
			}
		}
	}
	return nil
}
