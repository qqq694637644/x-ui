package service

import (
	"fmt"
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
	settingService SettingService
	caddyService   CaddyService
	linkService    LinkService

	applyManagedSiteHook    func(baseDomain string, block string) (string, error)
	restoreCaddyHook        func(content string) error
	healthCheckEndpointHook func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error
	commitRotationHook      func(items []rotationItem, retireAt int64) error
}

type EndpointInit struct {
	Host string `json:"host" form:"host"`
	Port int    `json:"port" form:"port"`
}

type rotationItem struct {
	inbound *model.Inbound
	old     *model.PublicEndpoint
	next    *model.PublicEndpoint
	spec    *managedInboundSpec
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
	_, err := s.applyCurrentRoutes()
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
	if _, err := s.applyCurrentRoutes(); err != nil {
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
		spec, specErr := validateManagedInbound(inbound)
		row := &EndpointRow{
			InboundId: inbound.Id,
			Remark:    inbound.Remark,
			Enable:    inbound.Enable,
			Publish:   inbound.Publish,
			Protocol:  inbound.Protocol,
			Supported: specErr == nil,
			Endpoints: byInbound[inbound.Id],
		}
		if specErr == nil {
			row.Network = "xhttp"
			row.Path = spec.Path
		}
		for _, endpoint := range row.Endpoints {
			if endpoint.Status == model.EndpointStatusActive {
				row.Active = endpoint
				break
			}
		}
		if row.Active != nil && specErr == nil {
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
	spec, err := validateManagedInbound(inbound)
	if err != nil {
		return nil, err
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
	if !validDomain(host) {
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
		Status:    model.EndpointStatusPending,
		CreatedAt: time.Now().Unix(),
	}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		return nil, err
	}
	oldContent, err := s.applyCurrentRoutes()
	if err != nil {
		_ = database.GetDB().Delete(endpoint).Error
		return nil, err
	}
	if err := s.checkManagedEndpointHealth(inbound, endpoint, managedHealthPath(transportMatchPath(spec.Path))); err != nil {
		restoreErr := s.restoreCaddy(oldContent)
		_ = database.GetDB().Delete(endpoint).Error
		if restoreErr != nil {
			return nil, common.NewError("初始化公网入口真实链路探测失败: ", err, "; Caddy 回滚失败: ", restoreErr)
		}
		return nil, err
	}
	if err := database.GetDB().Model(endpoint).Update("status", model.EndpointStatusActive).Error; err != nil {
		restoreErr := s.restoreCaddy(oldContent)
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
		if _, err := validateManagedInbound(inbound); err != nil {
			return err
		}
		endpoint, err := s.activeEndpoint(inboundID)
		if err != nil {
			return fmt.Errorf("initialize a public endpoint before publishing this inbound: %w", err)
		}
		if _, err := s.linkService.GenerateInboundLink(inbound, endpoint); err != nil {
			return fmt.Errorf("published inbound validation failed: %w", err)
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
	if _, err := s.applyCurrentRoutes(); err != nil {
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

	items := make([]rotationItem, 0, len(inbounds))
	now := time.Now().Unix()
	reservedHosts := map[string]bool{}
	for _, inbound := range inbounds {
		spec, err := validateManagedInbound(inbound)
		if err != nil {
			return nil, fmt.Errorf("published inbound %d is not eligible for managed rotation: %w", inbound.Id, err)
		}
		old, err := s.activeEndpoint(inbound.Id)
		if err != nil {
			return nil, fmt.Errorf("inbound %d has no active endpoint: %w", inbound.Id, err)
		}
		if _, err := s.linkService.GenerateInboundLink(inbound, old); err != nil {
			return nil, fmt.Errorf("published inbound %d cannot generate its current subscription link: %w", inbound.Id, err)
		}
		host, err := s.generateUniqueHost(settings.PublicBaseDomain, settings.HostRandomLength, reservedHosts)
		if err != nil {
			return nil, err
		}
		reservedHosts[host] = true
		next := &model.PublicEndpoint{
			InboundId: inbound.Id,
			Host:      host,
			Port:      settings.PublicPort,
			Status:    model.EndpointStatusPending,
			CreatedAt: now,
		}
		if _, err := s.linkService.GenerateInboundLink(inbound, next); err != nil {
			return nil, fmt.Errorf("published inbound %d cannot generate the next subscription link: %w", inbound.Id, err)
		}
		items = append(items, rotationItem{inbound: inbound, old: old, next: next, spec: spec})
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
	for _, item := range items {
		pendingIDs = append(pendingIDs, item.next.Id)
	}

	oldContent, err := s.applyCurrentRoutes()
	if err != nil {
		s.deletePending(pendingIDs)
		return nil, err
	}
	for _, item := range items {
		healthPath := managedHealthPath(transportMatchPath(item.spec.Path))
		if err := s.checkManagedEndpointHealth(item.inbound, item.next, healthPath); err != nil {
			restoreErr := s.restoreCaddy(oldContent)
			s.deletePending(pendingIDs)
			if restoreErr != nil {
				return nil, common.NewError("new endpoint real-chain health check failed: ", err, "; caddy rollback failed: ", restoreErr)
			}
			return nil, fmt.Errorf("new endpoint real-chain health check failed for %s: %w", item.next.Host, err)
		}
	}

	retireAt := time.Now().Add(time.Duration(settings.EndpointDrainSeconds) * time.Second).Unix()
	if err := s.commitRotation(items, retireAt); err != nil {
		s.deletePending(pendingIDs)
		return nil, s.rollbackCaddy(oldContent, err)
	}
	result := &RotationResult{Items: make([]RotationPair, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, RotationPair{
			InboundId: item.inbound.Id,
			OldHost:   item.old.Host,
			NewHost:   item.next.Host,
			Path:      item.spec.Path,
		})
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
	if _, err := s.applyCurrentRoutes(); err != nil {
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
	if _, err := s.applyCurrentRoutes(); err != nil {
		_ = database.GetDB().Model(&model.PublicEndpoint{}).Where("id IN ?", ids).
			Update("status", model.EndpointStatusDraining).Error
		return err
	}
	return nil
}

func (s *EndpointService) applyCurrentRoutes() (string, error) {
	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return "", err
	}
	routes, err := s.managedRoutes()
	if err != nil {
		return "", err
	}
	block, err := RenderManagedCaddy(settings.PublicBaseDomain, settings.PublicPort, settings.CaddyTLSCertFile, settings.CaddyTLSKeyFile, routes)
	if err != nil {
		return "", err
	}
	oldContent, err := s.applyManagedSite(settings.PublicBaseDomain, block)
	if err != nil {
		return "", err
	}
	return oldContent, nil
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
		spec, err := validateManagedInbound(inbound)
		if err != nil {
			return nil, fmt.Errorf("managed inbound %d is invalid: %w", inbound.Id, err)
		}
		routes = append(routes, ManagedRoute{
			Host:         endpoint.Host,
			Path:         transportMatchPath(spec.Path),
			UpstreamHost: "127.0.0.1",
			UpstreamPort: inbound.Port,
			Kind:         "inbound",
		})
		for _, portal := range portalByInbound[inbound.Id] {
			routes = append(routes, ManagedRoute{
				Host:         endpoint.Host,
				Path:         transportMatchPath(portal.XHttpPath),
				UpstreamHost: "127.0.0.1",
				UpstreamPort: portal.PortalListenPort,
				Kind:         "portal",
			})
		}
	}
	return routes, nil
}

func (s *EndpointService) applyManagedSite(baseDomain string, block string) (string, error) {
	if s.applyManagedSiteHook != nil {
		return s.applyManagedSiteHook(baseDomain, block)
	}
	oldContent, _, err := s.caddyService.ApplyManagedSite(baseDomain, block)
	if err != nil {
		return "", err
	}
	return oldContent, nil
}

func (s *EndpointService) restoreCaddy(content string) error {
	if s.restoreCaddyHook != nil {
		return s.restoreCaddyHook(content)
	}
	return s.caddyService.RestoreContent(content)
}

func (s *EndpointService) checkManagedEndpointHealth(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error {
	if s.healthCheckEndpointHook != nil {
		return s.healthCheckEndpointHook(inbound, endpoint, healthPath)
	}
	return checkVLESSXHTTPEndpointHealth(inbound, endpoint, healthPath)
}

func (s *EndpointService) commitRotation(items []rotationItem, retireAt int64) error {
	if s.commitRotationHook != nil {
		return s.commitRotationHook(items, retireAt)
	}
	tx := database.GetDB().Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for _, item := range items {
		if err := tx.Model(&model.PublicEndpoint{}).Where("id = ?", item.old.Id).
			Updates(map[string]interface{}{"status": model.EndpointStatusDraining, "retire_at": retireAt}).Error; err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Model(&model.PublicEndpoint{}).Where("id = ?", item.next.Id).
			Updates(map[string]interface{}{"status": model.EndpointStatusActive, "retire_at": 0}).Error; err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Model(&model.Tunnel{}).
			Where("mode = ? AND portal_transport = ? AND remote_address = ?", TunnelModePortal, PortalTransportXHTTP, item.old.Host).
			Update("remote_address", item.next.Host).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
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
		Where("host = ?", host).Count(&count).Error; err != nil {
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
	if restoreErr := s.restoreCaddy(oldContent); restoreErr != nil {
		return common.NewError("endpoint transaction failed: ", cause, "; caddy rollback failed: ", restoreErr)
	}
	return cause
}

func transportMatchPath(value string) string {
	if index := strings.Index(value, "?"); index >= 0 {
		value = value[:index]
	}
	return normalizeManagedPath(value)
}
