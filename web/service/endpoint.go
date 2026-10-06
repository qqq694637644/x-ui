package service

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/common"
	"x-ui/util/random"
	"x-ui/web/entity"
)

var endpointMutationLock sync.Mutex

type EndpointService struct {
	inboundService InboundService
	tunnelService  TunnelService
	settingService SettingService
	caddyService   CaddyService
	linkService    LinkService
}

type EndpointInit struct {
	Host     string `json:"host" form:"host"`
	Port     int    `json:"port" form:"port"`
	Security string `json:"security" form:"security"`
	SNI      string `json:"sni" form:"sni"`
}

type EndpointRow struct {
	InboundId int                     `json:"inboundId"`
	Remark    string                  `json:"remark"`
	Enable    bool                    `json:"enable"`
	Publish   bool                    `json:"publish"`
	Protocol  model.Protocol          `json:"protocol"`
	Network   string                  `json:"network"`
	Path      string                  `json:"path"`
	Supported bool                    `json:"supported"`
	Active    *model.PublicEndpoint   `json:"active"`
	Endpoints []*model.PublicEndpoint `json:"endpoints"`
	Link      string                  `json:"link"`
}

type RotationPair struct {
	InboundId int    `json:"inboundId"`
	OldHost   string `json:"oldHost"`
	NewHost   string `json:"newHost"`
	Path      string `json:"path"`
}

type RotationResult struct {
	Count int            `json:"count"`
	Items []RotationPair `json:"items"`
}

func (s *EndpointService) SyncManagedRoutes() error {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()
	_, _, err := s.applyCurrentRoutes()
	return err
}

func (s *EndpointService) ReconcileManagedRoutes() error {
	var count int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return s.SyncManagedRoutes()
}

func (s *EndpointService) RecoverPending() error {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	var pending []*model.PublicEndpoint
	if err := database.GetDB().Where("status = ?", model.EndpointStatusPending).Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	ids := make([]int, 0, len(pending))
	for _, endpoint := range pending {
		ids = append(ids, endpoint.Id)
	}
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("id IN ?", ids).
		Update("status", model.EndpointStatusRetired).Error; err != nil {
		return err
	}
	if _, _, err := s.applyCurrentRoutes(); err != nil {
		_ = database.GetDB().Model(&model.PublicEndpoint{}).Where("id IN ?", ids).
			Update("status", model.EndpointStatusPending).Error
		return err
	}
	return nil
}

func (s *EndpointService) List(userID int) ([]*EndpointRow, error) {
	inbounds, err := s.inboundService.GetInbounds(userID)
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return []*EndpointRow{}, nil
	}
	ids := make([]int, 0, len(inbounds))
	for _, inbound := range inbounds {
		ids = append(ids, inbound.Id)
	}
	var endpoints []*model.PublicEndpoint
	if err := database.GetDB().Where("inbound_id IN ?", ids).
		Order("created_at desc, id desc").Find(&endpoints).Error; err != nil {
		return nil, err
	}
	byInbound := map[int][]*model.PublicEndpoint{}
	for _, endpoint := range endpoints {
		byInbound[endpoint.InboundId] = append(byInbound[endpoint.InboundId], endpoint)
	}
	rows := make([]*EndpointRow, 0, len(inbounds))
	for _, inbound := range inbounds {
		transport, transportErr := inboundTransportInfo(inbound)
		row := &EndpointRow{
			InboundId: inbound.Id,
			Remark:    inbound.Remark,
			Enable:    inbound.Enable,
			Publish:   inbound.Publish,
			Protocol:  inbound.Protocol,
			Supported: transportErr == nil && managedNetworkSupported(transport.Network) && linkProtocolSupported(inbound.Protocol),
			Endpoints: byInbound[inbound.Id],
		}
		if transportErr == nil {
			row.Network = transport.Network
			row.Path = transport.Path
		}
		for _, endpoint := range row.Endpoints {
			if endpoint.Status == model.EndpointStatusActive {
				row.Active = endpoint
				break
			}
		}
		if row.Active != nil && linkProtocolSupported(inbound.Protocol) {
			if link, linkErr := s.linkService.GenerateInboundLink(inbound, row.Active); linkErr == nil {
				row.Link = link
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *EndpointService) Initialize(userID int, inboundID int, form *EndpointInit) (*model.PublicEndpoint, error) {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	inbound, err := s.getOwnedInbound(userID, inboundID)
	if err != nil {
		return nil, err
	}
	transport, err := inboundTransportInfo(inbound)
	if err != nil {
		return nil, err
	}
	if !managedNetworkSupported(transport.Network) {
		return nil, fmt.Errorf("transport %s is not supported by managed Caddy endpoints", transport.Network)
	}
	if !linkProtocolSupported(inbound.Protocol) {
		return nil, fmt.Errorf("protocol %s cannot be published as a subscription link", inbound.Protocol)
	}
	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return nil, err
	}
	if settings.PublicBaseDomain == "" {
		return nil, fmt.Errorf("public base domain is not configured")
	}

	host := strings.ToLower(strings.Trim(strings.TrimSpace(form.Host), "."))
	if host == "" {
		return nil, fmt.Errorf("public host is required")
	}
	if net.ParseIP(host) == nil && !validDomain(host) {
		return nil, fmt.Errorf("public host is invalid: %s", host)
	}
	suffix := "." + settings.PublicBaseDomain
	if !strings.HasSuffix(host, suffix) {
		return nil, fmt.Errorf("public host must be a direct subdomain of %s", settings.PublicBaseDomain)
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return nil, fmt.Errorf("public host must contain exactly one label before %s", settings.PublicBaseDomain)
	}
	port := form.Port
	if port == 0 {
		port = settings.PublicPort
	}
	if port != settings.PublicPort {
		return nil, fmt.Errorf("public endpoint port must match configured public port %d", settings.PublicPort)
	}
	security := strings.ToLower(strings.TrimSpace(form.Security))
	if security == "" {
		security = "tls"
	}
	if security != "tls" {
		return nil, fmt.Errorf("managed CDN endpoints currently require TLS")
	}
	var count int64
	if err := database.GetDB().Model(model.PublicEndpoint{}).
		Where("inbound_id = ? AND status = ?", inboundID, model.EndpointStatusActive).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, fmt.Errorf("inbound %d already has an active public endpoint", inboundID)
	}
	if err := s.ensureHostAvailable(host); err != nil {
		return nil, err
	}

	endpoint := &model.PublicEndpoint{
		InboundId: inboundID,
		Host:      host,
		Port:      port,
		Security:  security,
		SNI:       strings.TrimSpace(form.SNI),
		Status:    model.EndpointStatusPending,
		CreatedAt: time.Now().Unix(),
	}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		return nil, err
	}
	oldContent, _, err := s.applyCurrentRoutes()
	if err != nil {
		_ = database.GetDB().Delete(endpoint).Error
		return nil, err
	}
	if err := database.GetDB().Model(endpoint).Update("status", model.EndpointStatusActive).Error; err != nil {
		restoreErr := s.caddyService.RestoreContent(oldContent)
		_ = database.GetDB().Delete(endpoint).Error
		if restoreErr != nil {
			return nil, common.NewError("初始化公网入口状态切换失败: ", err, "; Caddy 回滚失败: ", restoreErr)
		}
		return nil, err
	}
	endpoint.Status = model.EndpointStatusActive
	return endpoint, nil
}

func (s *EndpointService) SetPublish(userID int, inboundID int, publish bool) error {
	inbound, err := s.getOwnedInbound(userID, inboundID)
	if err != nil {
		return err
	}
	if publish {
		if !linkProtocolSupported(inbound.Protocol) {
			return fmt.Errorf("protocol %s cannot be published", inbound.Protocol)
		}
		transport, err := inboundTransportInfo(inbound)
		if err != nil {
			return err
		}
		if !managedNetworkSupported(transport.Network) {
			return fmt.Errorf("transport %s is not supported by managed endpoints", transport.Network)
		}
		var count int64
		if err := database.GetDB().Model(model.PublicEndpoint{}).
			Where("inbound_id = ? AND status = ?", inboundID, model.EndpointStatusActive).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("initialize a public endpoint before publishing this inbound")
		}
	}
	return database.GetDB().Model(&model.Inbound{}).Where("id = ? AND user_id = ?", inboundID, userID).
		Update("publish", publish).Error
}

func (s *EndpointService) GetLink(userID int, inboundID int) (string, error) {
	inbound, err := s.getOwnedInbound(userID, inboundID)
	if err != nil {
		return "", err
	}
	endpoint, err := s.activeEndpoint(inboundID)
	if err != nil {
		return "", err
	}
	return s.linkService.GenerateInboundLink(inbound, endpoint)
}

func (s *EndpointService) UpdateSettings(settings *entity.EndpointSettings) error {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	old, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return err
	}
	var count int64
	if err := database.GetDB().Model(model.PublicEndpoint{}).
		Where("status != ?", model.EndpointStatusRetired).Count(&count).Error; err != nil {
		return err
	}
	newDomain := normalizeDomain(settings.PublicBaseDomain)
	if count > 0 && newDomain != normalizeDomain(old.PublicBaseDomain) {
		return fmt.Errorf("cannot change public base domain while active/draining endpoints exist")
	}
	if count > 0 && settings.PublicPort != old.PublicPort {
		return fmt.Errorf("cannot change public port while active/draining endpoints exist")
	}
	if err := s.settingService.UpdateEndpointSettings(settings); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, _, err := s.applyCurrentRoutes(); err != nil {
		_ = s.settingService.UpdateEndpointSettings(old)
		return err
	}
	return nil
}

func (s *EndpointService) RotateAll(userID int) (*RotationResult, error) {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return nil, err
	}
	if settings.PublicBaseDomain == "" {
		return nil, fmt.Errorf("public base domain is not configured")
	}
	var inbounds []*model.Inbound
	if err := database.GetDB().Where("user_id = ? AND enable = ? AND publish = ?", userID, true, true).
		Order("id asc").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, fmt.Errorf("no enabled and published inbounds")
	}

	type pendingItem struct {
		inbound *model.Inbound
		old     *model.PublicEndpoint
		next    *model.PublicEndpoint
		path    string
	}
	items := make([]pendingItem, 0, len(inbounds))
	now := time.Now().Unix()
	reservedHosts := map[string]bool{}
	for _, inbound := range inbounds {
		transport, err := inboundTransportInfo(inbound)
		if err != nil {
			return nil, err
		}
		if !managedNetworkSupported(transport.Network) {
			return nil, fmt.Errorf("inbound %d transport %s is not supported by managed rotation", inbound.Id, transport.Network)
		}
		old, err := s.activeEndpoint(inbound.Id)
		if err != nil {
			return nil, fmt.Errorf("inbound %d has no active endpoint: %w", inbound.Id, err)
		}
		host, err := s.generateUniqueHost(settings.PublicBaseDomain, settings.HostRandomLength, reservedHosts)
		if err != nil {
			return nil, err
		}
		reservedHosts[host] = true
		sni := old.SNI
		if strings.EqualFold(strings.TrimSpace(sni), strings.TrimSpace(old.Host)) {
			sni = host
		}
		next := &model.PublicEndpoint{
			InboundId: inbound.Id,
			Host:      host,
			Port:      settings.PublicPort,
			Security:  old.Security,
			SNI:       sni,
			Status:    model.EndpointStatusPending,
			CreatedAt: now,
		}
		items = append(items, pendingItem{inbound: inbound, old: old, next: next, path: transport.Path})
	}

	tx := database.GetDB().Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	for i := range items {
		if err := tx.Create(items[i].next).Error; err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}

	pendingIDs := make([]int, 0, len(items))
	pendingHosts := map[string]bool{}
	securityByHost := map[string]string{}
	for _, item := range items {
		pendingIDs = append(pendingIDs, item.next.Id)
		pendingHosts[item.next.Host] = true
		securityByHost[item.next.Host] = item.next.Security
	}

	oldContent, probes, err := s.applyCurrentRoutes()
	if err != nil {
		s.deletePending(pendingIDs)
		return nil, err
	}
	if err := s.healthCheckPending(probes, pendingHosts, securityByHost, settings.PublicPort); err != nil {
		restoreErr := s.caddyService.RestoreContent(oldContent)
		s.deletePending(pendingIDs)
		if restoreErr != nil {
			return nil, common.NewError("new endpoints failed health check: ", err, "; caddy rollback failed: ", restoreErr)
		}
		return nil, err
	}

	switchTx := database.GetDB().Begin()
	if switchTx.Error != nil {
		s.deletePending(pendingIDs)
		return nil, s.rollbackCaddy(oldContent, switchTx.Error)
	}
	retireAt := time.Now().Add(time.Duration(settings.EndpointDrainSeconds) * time.Second).Unix()
	result := &RotationResult{Items: make([]RotationPair, 0, len(items))}
	for _, item := range items {
		if err := switchTx.Model(&model.PublicEndpoint{}).Where("id = ?", item.old.Id).
			Updates(map[string]interface{}{"status": model.EndpointStatusDraining, "retire_at": retireAt}).Error; err != nil {
			switchTx.Rollback()
			s.deletePending(pendingIDs)
			return nil, s.rollbackCaddy(oldContent, err)
		}
		if err := switchTx.Model(&model.PublicEndpoint{}).Where("id = ?", item.next.Id).
			Updates(map[string]interface{}{"status": model.EndpointStatusActive, "retire_at": 0}).Error; err != nil {
			switchTx.Rollback()
			s.deletePending(pendingIDs)
			return nil, s.rollbackCaddy(oldContent, err)
		}
		if err := switchTx.Model(&model.Tunnel{}).
			Where("mode = ? AND portal_transport = ? AND remote_address = ?", TunnelModePortal, PortalTransportXHTTP, item.old.Host).
			Update("remote_address", item.next.Host).Error; err != nil {
			switchTx.Rollback()
			s.deletePending(pendingIDs)
			return nil, s.rollbackCaddy(oldContent, err)
		}
		result.Items = append(result.Items, RotationPair{
			InboundId: item.inbound.Id,
			OldHost:   item.old.Host,
			NewHost:   item.next.Host,
			Path:      item.path,
		})
	}
	if err := switchTx.Commit().Error; err != nil {
		s.deletePending(pendingIDs)
		return nil, s.rollbackCaddy(oldContent, err)
	}
	result.Count = len(result.Items)
	return result, nil
}

func (s *EndpointService) Retire(userID int, endpointID int) error {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	var endpoint model.PublicEndpoint
	if err := database.GetDB().First(&endpoint, endpointID).Error; err != nil {
		return err
	}
	if _, err := s.getOwnedInbound(userID, endpoint.InboundId); err != nil {
		return err
	}
	if endpoint.Status == model.EndpointStatusActive {
		return fmt.Errorf("active endpoint cannot be retired directly")
	}
	if endpoint.Status == model.EndpointStatusRetired {
		return nil
	}
	oldStatus := endpoint.Status
	oldRetireAt := endpoint.RetireAt
	if err := database.GetDB().Model(&endpoint).
		Updates(map[string]interface{}{"status": model.EndpointStatusRetired, "retire_at": time.Now().Unix()}).Error; err != nil {
		return err
	}
	if _, _, err := s.applyCurrentRoutes(); err != nil {
		_ = database.GetDB().Model(&endpoint).
			Updates(map[string]interface{}{"status": oldStatus, "retire_at": oldRetireAt}).Error
		return err
	}
	return nil
}

func (s *EndpointService) RetireExpired() error {
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	var due []*model.PublicEndpoint
	now := time.Now().Unix()
	if err := database.GetDB().Where("status = ? AND retire_at > 0 AND retire_at <= ?", model.EndpointStatusDraining, now).
		Find(&due).Error; err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	ids := make([]int, 0, len(due))
	for _, endpoint := range due {
		ids = append(ids, endpoint.Id)
	}
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("id IN ?", ids).
		Update("status", model.EndpointStatusRetired).Error; err != nil {
		return err
	}
	if _, _, err := s.applyCurrentRoutes(); err != nil {
		_ = database.GetDB().Model(&model.PublicEndpoint{}).Where("id IN ?", ids).
			Update("status", model.EndpointStatusDraining).Error
		return err
	}
	return nil
}

func (s *EndpointService) applyCurrentRoutes() (string, []ManagedProbe, error) {
	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return "", nil, err
	}
	routes, err := s.managedRoutes()
	if err != nil {
		return "", nil, err
	}
	block, probes, err := RenderManagedCaddy(settings.PublicBaseDomain, settings.PublicPort, settings.CaddyTLSCertFile, settings.CaddyTLSKeyFile, routes)
	if err != nil {
		return "", nil, err
	}
	oldContent, _, err := s.caddyService.ApplyManagedBlock(block)
	if err != nil {
		return "", nil, err
	}
	return oldContent, probes, nil
}

func (s *EndpointService) managedRoutes() ([]ManagedRoute, error) {
	var endpoints []*model.PublicEndpoint
	if err := database.GetDB().
		Where("status IN ?", []string{model.EndpointStatusPending, model.EndpointStatusActive, model.EndpointStatusDraining}).
		Order("inbound_id asc, id asc").Find(&endpoints).Error; err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		return []ManagedRoute{}, nil
	}
	inboundIDs := make([]int, 0)
	seenInbound := map[int]bool{}
	for _, endpoint := range endpoints {
		if !seenInbound[endpoint.InboundId] {
			seenInbound[endpoint.InboundId] = true
			inboundIDs = append(inboundIDs, endpoint.InboundId)
		}
	}
	var inbounds []*model.Inbound
	if err := database.GetDB().Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	inboundByID := map[int]*model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.Id] = inbound
	}

	groupHosts := map[int]map[string]bool{}
	for _, endpoint := range endpoints {
		if groupHosts[endpoint.InboundId] == nil {
			groupHosts[endpoint.InboundId] = map[string]bool{}
		}
		groupHosts[endpoint.InboundId][strings.ToLower(endpoint.Host)] = true
	}

	var portals []*model.Tunnel
	if err := database.GetDB().Where("enable = ? AND mode = ? AND portal_transport = ?", true, TunnelModePortal, PortalTransportXHTTP).
		Find(&portals).Error; err != nil {
		return nil, err
	}
	portalByInbound := map[int][]*model.Tunnel{}
	for _, portal := range portals {
		host := strings.ToLower(strings.TrimSpace(portal.RemoteAddress))
		for inboundID, hosts := range groupHosts {
			if hosts[host] {
				portalByInbound[inboundID] = append(portalByInbound[inboundID], portal)
				break
			}
		}
	}

	routes := make([]ManagedRoute, 0, len(endpoints)*2)
	for _, endpoint := range endpoints {
		inbound := inboundByID[endpoint.InboundId]
		if inbound == nil || !inbound.Enable {
			continue
		}
		transport, err := inboundTransportInfo(inbound)
		if err != nil {
			return nil, err
		}
		if !managedNetworkSupported(transport.Network) {
			return nil, fmt.Errorf("inbound %d transport %s cannot be rendered through Caddy", inbound.Id, transport.Network)
		}
		routes = append(routes, ManagedRoute{
			Host:         endpoint.Host,
			Path:         transportMatchPath(transport.Path),
			Network:      transport.Network,
			UpstreamHost: inbound.Listen,
			UpstreamPort: inbound.Port,
			Kind:         "inbound",
		})
		for _, portal := range portalByInbound[inbound.Id] {
			routes = append(routes, ManagedRoute{
				Host:         endpoint.Host,
				Path:         transportMatchPath(portal.XHttpPath),
				Network:      "xhttp",
				UpstreamHost: "127.0.0.1",
				UpstreamPort: portal.PortalListenPort,
				Kind:         "portal",
			})
		}
	}
	return routes, nil
}

func (s *EndpointService) healthCheckPending(probes []ManagedProbe, pendingHosts map[string]bool, securityByHost map[string]string, publicPort int) error {
	filtered := make([]ManagedProbe, 0)
	for _, probe := range probes {
		if pendingHosts[probe.Host] {
			filtered = append(filtered, probe)
		}
	}
	if len(filtered) == 0 {
		return fmt.Errorf("no health probes were generated for pending endpoints")
	}
	localSeen := map[string]bool{}
	for _, probe := range filtered {
		localTarget := net.JoinHostPort(strings.Trim(probe.LocalHost, "[]"), strconv.Itoa(probe.LocalPort))
		if !localSeen[localTarget] {
			localSeen[localTarget] = true
			conn, err := net.DialTimeout("tcp", localTarget, 2*time.Second)
			if err != nil {
				return fmt.Errorf("local upstream %s is unreachable: %w", localTarget, err)
			}
			_ = conn.Close()
		}

		scheme := "https"
		if strings.EqualFold(securityByHost[probe.Host], "none") {
			scheme = "http"
		}
		hostPort := probe.Host
		if (scheme == "https" && publicPort != 443) || (scheme == "http" && publicPort != 80) {
			hostPort = net.JoinHostPort(probe.Host, strconv.Itoa(publicPort))
		}
		target := scheme + "://" + hostPort + probe.Path
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			client := &http.Client{
				Timeout:   5 * time.Second,
				Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			req, _ := http.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Cache-Control", "no-cache")
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusNoContent {
					lastErr = nil
					break
				}
				lastErr = fmt.Errorf("health URL %s returned HTTP %d", target, resp.StatusCode)
			} else {
				lastErr = err
			}
			time.Sleep(500 * time.Millisecond)
		}
		if lastErr != nil {
			return fmt.Errorf("public endpoint health check failed for %s: %w", probe.Host, lastErr)
		}
	}
	return nil
}

func (s *EndpointService) activeEndpoint(inboundID int) (*model.PublicEndpoint, error) {
	endpoint := &model.PublicEndpoint{}
	err := database.GetDB().Where("inbound_id = ? AND status = ?", inboundID, model.EndpointStatusActive).
		Order("created_at desc, id desc").First(endpoint).Error
	if err != nil {
		return nil, err
	}
	return endpoint, nil
}

func (s *EndpointService) getOwnedInbound(userID int, inboundID int) (*model.Inbound, error) {
	inbound := &model.Inbound{}
	if err := database.GetDB().Where("id = ? AND user_id = ?", inboundID, userID).First(inbound).Error; err != nil {
		return nil, err
	}
	return inbound, nil
}

func (s *EndpointService) ensureHostAvailable(host string) error {
	var count int64
	if err := database.GetDB().Model(model.PublicEndpoint{}).
		Where("host = ? AND status != ?", host, model.EndpointStatusRetired).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("public host is already in use: %s", host)
	}
	return nil
}

func (s *EndpointService) generateUniqueHost(baseDomain string, length int, reserved map[string]bool) (string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		label, err := random.SecureLowerSeq(length)
		if err != nil {
			return "", err
		}
		host := label + "." + baseDomain
		if reserved != nil && reserved[host] {
			continue
		}
		if err := s.ensureHostAvailable(host); err == nil {
			return host, nil
		}
	}
	return "", fmt.Errorf("failed to allocate a unique random hostname")
}

func (s *EndpointService) deletePending(ids []int) {
	if len(ids) == 0 {
		return
	}
	_ = database.GetDB().Where("id IN ? AND status = ?", ids, model.EndpointStatusPending).
		Delete(&model.PublicEndpoint{}).Error
}

func (s *EndpointService) rollbackCaddy(oldContent string, cause error) error {
	if restoreErr := s.caddyService.RestoreContent(oldContent); restoreErr != nil {
		return common.NewError("endpoint transaction failed: ", cause, "; caddy rollback failed: ", restoreErr)
	}
	return cause
}

func managedNetworkSupported(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "xhttp", "ws", "http", "grpc":
		return true
	default:
		return false
	}
}

func transportMatchPath(value string) string {
	if index := strings.Index(value, "?"); index >= 0 {
		value = value[:index]
	}
	return normalizeManagedPath(value)
}

func linkProtocolSupported(protocol model.Protocol) bool {
	switch protocol {
	case model.VMess, model.VLESS, model.Trojan, model.Shadowsocks:
		return true
	default:
		return false
	}
}
