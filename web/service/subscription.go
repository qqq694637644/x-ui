package service

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"x-ui/database"
	"x-ui/database/model"
)

type SubscriptionService struct {
	settingService SettingService
	linkService    LinkService
}

var (
	ErrSubscriptionDisabled = errors.New("subscription is disabled")
	ErrSubscriptionToken    = errors.New("invalid subscription token")
)

func (s *SubscriptionService) Generate(token string) (string, error) {
	settings, err := s.settingService.GetEndpointSettings()
	if err != nil {
		return "", err
	}
	if !settings.SubscriptionEnable {
		return "", ErrSubscriptionDisabled
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(settings.SubscriptionToken)) != 1 {
		return "", ErrSubscriptionToken
	}
	if strings.TrimSpace(settings.SubscriptionBaseURL) == "" {
		return "", fmt.Errorf("stable subscription base URL is not configured")
	}
	if !isManagedStateHealthy() {
		return "", ErrManagedStateUnhealthy
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
	activeCountByInbound := make(map[int]int, len(endpoints))
	for _, endpoint := range endpoints {
		activeCountByInbound[endpoint.InboundId]++
		if activeCountByInbound[endpoint.InboundId] == 1 {
			activeByInbound[endpoint.InboundId] = endpoint
		}
	}

	links := make([]string, 0, len(inbounds))
	for _, inbound := range inbounds {
		if activeCountByInbound[inbound.Id] != 1 {
			return "", fmt.Errorf("published inbound %d must have exactly one active public endpoint, got %d", inbound.Id, activeCountByInbound[inbound.Id])
		}
		endpoint := activeByInbound[inbound.Id]
		link, err := s.linkService.GenerateInboundLink(inbound, endpoint)
		if err != nil {
			return "", fmt.Errorf("published inbound %d cannot generate subscription link: %w", inbound.Id, err)
		}
		if strings.TrimSpace(link) == "" {
			return "", fmt.Errorf("published inbound %d generated an empty subscription link", inbound.Id)
		}
		links = append(links, link)
	}
	body := strings.Join(links, "\n")
	return base64.StdEncoding.EncodeToString([]byte(body)), nil
}
