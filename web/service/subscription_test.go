package service

import (
	"encoding/base64"
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
		SubscriptionTitle:    "test",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}

	inbound := &model.Inbound{
		UserId:         1,
		Remark:         "node-a",
		Enable:         true,
		Publish:        true,
		Listen:         "127.0.0.1",
		Port:           26417,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","flow":""}],"decryption":"none"}`,
		StreamSettings: `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/q8Fa72Lm9x","host":"","mode":"auto"}}`,
		Tag:            "subscription-test",
		Sniffing:       `{"enabled":false}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	oldEndpoint := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "a.asdasdasdas.shop",
		Port:      443,
		Security:  "tls",
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
		Security:  "tls",
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
		SubscriptionTitle:    "test",
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

func decodeSubscriptionForTest(t *testing.T, body string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("decode subscription: %v", err)
	}
	return string(decoded)
}
