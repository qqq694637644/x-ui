package service

import (
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/web/entity"
)

func TestResetSettingsRefusesManagedEndpointHistory(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "settings-reset-managed.db")); err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	if err := settingService.UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   false,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.PublicEndpoint{
		InboundId: 42,
		Host:      "retired.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := settingService.ResetSettings(); err == nil || !strings.Contains(err.Error(), "PublicEndpoint") {
		t.Fatalf("ResetSettings() managed-history error = %v", err)
	}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicBaseDomain != "asdasdasdas.shop" {
		t.Fatalf("settings were partially reset despite managed history: %#v", settings)
	}
}

func TestEndpointSettingsRejectDrainBelowOneMinute(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "settings-drain-min.db")); err != nil {
		t.Fatal(err)
	}
	err := (&SettingService{}).UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   false,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		HostRandomLength:     10,
		EndpointDrainSeconds: 0,
	})
	if err == nil || !strings.Contains(err.Error(), "60") {
		t.Fatalf("UpdateEndpointSettings() drain minimum error = %v", err)
	}
}
