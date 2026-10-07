package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"x-ui/database"
	"x-ui/database/model"
)

func TestDisableInvalidInboundsSyncsManagedCaddy(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "disable-invalid-sync.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "disable-invalid", "disable-invalid", 26417, "/fixed", "disable.asdasdasdas.shop")
	if err := database.GetDB().Model(inbound).Updates(map[string]interface{}{
		"total":       int64(1),
		"up":          int64(1),
		"expiry_time": time.Now().Add(time.Hour).UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	syncCalls := 0
	service := &InboundService{syncManagedRoutesHook: func() error {
		syncCalls++
		routes, err := (&EndpointService{}).managedRoutes()
		if err != nil {
			return err
		}
		if len(routes) != 0 {
			t.Fatalf("managed routes still contain automatically disabled inbound: %#v", routes)
		}
		return nil
	}}
	count, err := service.DisableInvalidInbounds()
	if err != nil {
		t.Fatalf("DisableInvalidInbounds() error = %v", err)
	}
	if count != 1 || syncCalls != 1 {
		t.Fatalf("DisableInvalidInbounds() count=%d syncCalls=%d, want 1/1", count, syncCalls)
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enable {
		t.Fatal("invalid managed inbound remained enabled")
	}
}

func TestDisableInvalidInboundsRestoresDatabaseWhenCaddySyncFails(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "disable-invalid-rollback.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "disable-invalid-rollback", "disable-invalid-rollback", 26417, "/fixed", "rollback.asdasdasdas.shop")
	if err := database.GetDB().Model(inbound).Updates(map[string]interface{}{
		"total": int64(1),
		"down":  int64(1),
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := &InboundService{syncManagedRoutesHook: func() error {
		return errors.New("injected managed Caddy sync failure")
	}}
	count, err := service.DisableInvalidInbounds()
	if err == nil || count != 0 {
		t.Fatalf("DisableInvalidInbounds() count=%d error=%v, want rollback error", count, err)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after automatic-disable Caddy sync failure")
	}
	defer setManagedStateHealthy(true)
	var stored model.Inbound
	if err := database.GetDB().First(&stored, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Enable {
		t.Fatal("inbound enable flag was not restored after Caddy sync failure")
	}
}
