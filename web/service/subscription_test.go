package service

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/web/entity"
)

func TestSubscriptionReflectsActiveEndpointRotationWithoutChangingPath(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription.db")); err != nil {
		t.Fatal(err)
	}

	settingService := &SettingService{}
	initial, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}

	inbound := validManagedInboundForTest(0, "node-a", 26417, "/q8Fa72Lm9x")
	inbound.Publish = true
	inbound.Tag = "subscription-test"
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	oldEndpoint := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "a.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: 1,
	}
	if err := database.GetDB().Create(oldEndpoint).Error; err != nil {
		t.Fatal(err)
	}

	subscription := &SubscriptionService{}
	firstBody, err := subscription.Generate(initial.SubscriptionToken)
	if err != nil {
		t.Fatal(err)
	}
	first := decodeSubscriptionForTest(t, firstBody)
	if !strings.Contains(first, "a.asdasdasdas.shop") {
		t.Fatalf("initial subscription missing old host: %s", first)
	}
	if !strings.Contains(first, "path=%2Fq8Fa72Lm9x") {
		t.Fatalf("initial subscription missing fixed path: %s", first)
	}

	if err := database.GetDB().Model(oldEndpoint).
		Updates(map[string]interface{}{"status": model.EndpointStatusDraining, "retire_at": int64(9999999999)}).Error; err != nil {
		t.Fatal(err)
	}
	newEndpoint := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "d.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: 2,
	}
	if err := database.GetDB().Create(newEndpoint).Error; err != nil {
		t.Fatal(err)
	}

	secondBody, err := subscription.Generate(initial.SubscriptionToken)
	if err != nil {
		t.Fatal(err)
	}
	second := decodeSubscriptionForTest(t, secondBody)
	if strings.Contains(second, "a.asdasdasdas.shop") {
		t.Fatalf("rotated subscription still contains old draining host: %s", second)
	}
	if !strings.Contains(second, "d.asdasdasdas.shop") {
		t.Fatalf("rotated subscription missing new host: %s", second)
	}
	if !strings.Contains(second, "path=%2Fq8Fa72Lm9x") {
		t.Fatalf("fixed path changed after rotation: %s", second)
	}
}

func TestSubscriptionRejectsWrongToken(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-token.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SubscriptionService{}).Generate("wrong-token"); err == nil {
		t.Fatal("wrong subscription token unexpectedly succeeded")
	}
}

func TestSubscriptionAllowsEmptyPublishedSet(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-empty.db")); err != nil {
		t.Fatal(err)
	}
	setManagedStateHealthy(true)
	settingService := &SettingService{}
	initial, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := (&SubscriptionService{}).Generate(initial.SubscriptionToken)
	if err != nil {
		t.Fatalf("Generate() empty published set error = %v", err)
	}
	if body != "" {
		t.Fatalf("Generate() empty published set body = %q, want empty HTTP 200 body", body)
	}
}

func TestSubscriptionFailsClosedWhenPublishedInboundHasNoActiveEndpoint(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-missing-endpoint.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := validManagedInboundForTest(0, "missing-endpoint", 26417, "/fixed")
	inbound.Publish = true
	inbound.Tag = "subscription-missing-endpoint"
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&SubscriptionService{}).Generate(settings.SubscriptionToken); err == nil {
		t.Fatal("subscription unexpectedly succeeded without an active endpoint")
	}
}

func TestSubscriptionFailsClosedWhenPublishedInboundBecomesInvalid(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-invalid-inbound.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := validManagedInboundForTest(0, "invalid-inbound", 26417, "/fixed")
	inbound.Publish = true
	inbound.Tag = "subscription-invalid-inbound"
	inbound.StreamSettings = `{"network":"ws","security":"none","wsSettings":{"path":"/fixed"}}`
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "invalid.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: 1,
	}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&SubscriptionService{}).Generate(settings.SubscriptionToken); err == nil {
		t.Fatal("subscription unexpectedly returned a partial result for an invalid published inbound")
	}
}

func TestSubscriptionFailsClosedWhenManagedStateIsUnhealthy(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-unhealthy.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	setManagedStateHealthy(false)
	defer setManagedStateHealthy(true)
	if _, err := (&SubscriptionService{}).Generate(settings.SubscriptionToken); !errors.Is(err, ErrManagedStateUnhealthy) {
		t.Fatalf("subscription unhealthy error = %v, want %v", err, ErrManagedStateUnhealthy)
	}
}

func TestSubscriptionRejectsMultipleActiveEndpointsForInbound(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-duplicate-active.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := validManagedInboundForTest(0, "duplicate-active", 26417, "/fixed")
	inbound.Publish = true
	inbound.Tag = "subscription-duplicate-active"
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Exec("DROP INDEX idx_public_endpoints_one_active_per_inbound").Error; err != nil {
		t.Fatal(err)
	}
	for i, host := range []string{"first.asdasdasdas.shop", "second.asdasdasdas.shop"} {
		endpoint := &model.PublicEndpoint{InboundId: inbound.Id, Host: host, Port: 443, Status: model.EndpointStatusActive, CreatedAt: int64(i + 1)}
		if err := database.GetDB().Create(endpoint).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&SubscriptionService{}).Generate(settings.SubscriptionToken); err == nil || !strings.Contains(err.Error(), "exactly one active public endpoint") {
		t.Fatalf("subscription duplicate-active error = %v", err)
	}
}

func decodeSubscriptionForTest(t *testing.T, body string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("decode subscription: %v", err)
	}
	return string(decoded)
}
