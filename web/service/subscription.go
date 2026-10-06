package service

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"x-ui/database"
	"x-ui/database/model"
)

type SubscriptionService struct {
	settingService SettingService
	linkService    LinkService
}

func (s *SubscriptionService) Generate(token string) (string, error) {
	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return "", err
	}
	if !settings.SubscriptionEnable {
		return "", fmt.Errorf("subscription is disabled")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(settings.SubscriptionToken)) != 1 {
		return "", fmt.Errorf("invalid subscription token")
	}

	db := database.GetDB()
	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("enable = ? AND publish = ?", true, true).
		Order("id asc").
		Find(&inbounds).Error; err != nil {
		return "", err
	}

	var endpoints []*model.PublicEndpoint
	if err := db.Model(model.PublicEndpoint{}).
		Where("status = ?", model.EndpointStatusActive).
		Order("created_at desc, id desc").
		Find(&endpoints).Error; err != nil {
		return "", err
	}
	activeByInbound := make(map[int]*model.PublicEndpoint, len(endpoints))
	for _, endpoint := range endpoints {
		if _, exists := activeByInbound[endpoint.InboundId]; !exists {
			activeByInbound[endpoint.InboundId] = endpoint
		}
	}

	links := make([]string, 0, len(inbounds))
	for _, inbound := range inbounds {
		endpoint := activeByInbound[inbound.Id]
		if endpoint == nil {
			continue
		}
		link, err := s.linkService.GenerateInboundLink(inbound, endpoint)
		if err != nil {
			continue
		}
		if strings.TrimSpace(link) != "" {
			links = append(links, link)
		}
	}
	if len(links) == 0 {
		return "", fmt.Errorf("subscription has no publishable endpoints")
	}

	body := strings.Join(links, "\n")
	return base64.StdEncoding.EncodeToString([]byte(body)), nil
}
