package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/web/entity"
)

func configureEndpointSettingsForTest(t *testing.T) {
	t.Helper()
	setManagedStateHealthy(true)
	if err := (&SettingService{}).UpdateEndpointSettings(&entity.EndpointSettings{
		SubscriptionEnable:   true,
		SubscriptionBaseURL:  "https://sub.example.net/xui",
		PublicBaseDomain:     "asdasdasdas.shop",
		PublicPort:           443,
		HostRandomLength:     10,
		EndpointDrainSeconds: 1800,
		CaddyTLSCertFile:     "/etc/caddy/wildcard.crt",
		CaddyTLSKeyFile:      "/etc/caddy/wildcard.key",
	}); err != nil {
		t.Fatal(err)
	}
}

func createPublishedManagedInboundForTest(t *testing.T, tag string, remark string, port int, path string, host string) (*model.Inbound, *model.PublicEndpoint) {
	t.Helper()
	inbound := validManagedInboundForTest(0, remark, port, path)
	inbound.Tag = tag
	inbound.Publish = true
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      host,
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: int64(inbound.Id),
	}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		t.Fatal(err)
	}
	return inbound, endpoint
}

func TestManagedRoutesCarryPortalAcrossEndpointHostGroup(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "managed-routes.db")); err != nil {
		t.Fatal(err)
	}
	inbound := validManagedInboundForTest(0, "business", 26417, "/business")
	inbound.Tag = "portal-managed-route"
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []*model.PublicEndpoint{
		{InboundId: inbound.Id, Host: "old.example.com", Port: 443, Status: model.EndpointStatusDraining, CreatedAt: 1},
		{InboundId: inbound.Id, Host: "new.example.com", Port: 443, Status: model.EndpointStatusActive, CreatedAt: 2},
	} {
		if err := database.GetDB().Create(endpoint).Error; err != nil {
			t.Fatal(err)
		}
	}
	portal := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		RemoteAddress:    "new.example.com",
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "11111111-1111-1111-1111-111111111111",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	if err := database.GetDB().Create(portal).Error; err != nil {
		t.Fatal(err)
	}

	routes, err := (&EndpointService{}).managedRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatalf("managed route count = %d, want 4", len(routes))
	}
	seen := map[string]bool{}
	for _, route := range routes {
		seen[route.Host+"|"+route.Path] = true
	}
	for _, key := range []string{
		"old.example.com|/business",
		"old.example.com|/portal-fixed",
		"new.example.com|/business",
		"new.example.com|/portal-fixed",
	} {
		if !seen[key] {
			t.Fatalf("missing managed route %s: %#v", key, routes)
		}
	}
}

func TestInitializeBatchTakesOverAllEligibleInboundsWithOneCaddyApply(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "initialize-batch.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inboundA := validManagedInboundForTest(0, "a", 26417, "/a")
	inboundA.Tag = "initialize-batch-a"
	inboundB := validManagedInboundForTest(0, "b", 26418, "/b")
	inboundB.Tag = "initialize-batch-b"
	for _, inbound := range []*model.Inbound{inboundA, inboundB} {
		if err := database.GetDB().Create(inbound).Error; err != nil {
			t.Fatal(err)
		}
	}

	applyCalls := 0
	healthCalls := 0
	service := &EndpointService{
		applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
			applyCalls++
			for _, expected := range []string{"a.asdasdasdas.shop", "b.asdasdasdas.shop", "path /a*", "path /b*"} {
				if !strings.Contains(block, expected) {
					t.Fatalf("managed Caddy block missing %q:\n%s", expected, block)
				}
			}
			return "legacy-caddy", nil
		},
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error {
			healthCalls++
			return nil
		},
	}
	result, err := service.InitializeBatch(1, &EndpointBatchInit{Items: []*EndpointInit{
		{InboundId: inboundA.Id, Host: "a.asdasdasdas.shop", Port: 443},
		{InboundId: inboundB.Id, Host: "b.asdasdasdas.shop", Port: 443},
	}})
	if err != nil {
		t.Fatalf("InitializeBatch() error = %v", err)
	}
	if result.Count != 2 || applyCalls != 1 || healthCalls != 2 {
		t.Fatalf("unexpected initialization result=%#v apply=%d health=%d", result, applyCalls, healthCalls)
	}
	assertActiveEndpointCountForTest(t, 2)
	assertNoPendingEndpointsForTest(t)
}

func TestInitializeBatchRejectsPartialFirstTakeoverBeforeCaddyApply(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "initialize-partial.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inboundA := validManagedInboundForTest(0, "a", 26417, "/a")
	inboundA.Tag = "initialize-partial-a"
	inboundB := validManagedInboundForTest(0, "b", 26418, "/b")
	inboundB.Tag = "initialize-partial-b"
	for _, inbound := range []*model.Inbound{inboundA, inboundB} {
		if err := database.GetDB().Create(inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	applyCalls := 0
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		applyCalls++
		return "", nil
	}}
	_, err := service.InitializeBatch(1, &EndpointBatchInit{Items: []*EndpointInit{
		{InboundId: inboundA.Id, Host: "a.asdasdasdas.shop", Port: 443},
	}})
	if err == nil || !strings.Contains(err.Error(), "must initialize all enabled eligible inbounds atomically") {
		t.Fatalf("InitializeBatch() partial takeover error = %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("partial first takeover applied Caddy %d times", applyCalls)
	}
	assertActiveEndpointCountForTest(t, 0)
	assertNoPendingEndpointsForTest(t)
}

func TestRotateAllSuccessSwitchesWholeBatchAfterOneCaddyApply(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "rotate-success.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inboundA, oldA := createPublishedManagedInboundForTest(t, "rotate-a", "a", 26417, "/a", "a.asdasdasdas.shop")
	_, oldB := createPublishedManagedInboundForTest(t, "rotate-b", "b", 26418, "/b", "b.asdasdasdas.shop")
	portal := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		RemoteAddress:    oldA.Host,
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "33333333-3333-3333-3333-333333333333",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26419,
		XHttpPath:        "/portal-a",
	}
	if err := database.GetDB().Create(portal).Error; err != nil {
		t.Fatal(err)
	}

	applyCalls := 0
	healthCheckCalls := 0
	portalHealthCalls := 0
	restoreCalls := 0
	service := &EndpointService{
		applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
			applyCalls++
			if baseDomain != "asdasdasdas.shop" || !strings.Contains(block, "reverse_proxy h2c://127.0.0.1") || !strings.Contains(block, "path /portal-a*") {
				t.Fatalf("unexpected managed Caddy block:\n%s", block)
			}
			return "old-caddy", nil
		},
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error {
			healthCheckCalls++
			if endpoint.Host == oldA.Host || endpoint.Host == oldB.Host {
				t.Fatalf("health check used old hostname %s", endpoint.Host)
			}
			return nil
		},
		healthCheckPortalHook: func(portal *model.Tunnel) error {
			portalHealthCalls++
			return nil
		},
		restoreCaddyHook: func(content string) error {
			restoreCalls++
			return nil
		},
	}
	result, err := service.RotateAll(1)
	if err != nil {
		t.Fatalf("RotateAll() error = %v", err)
	}
	if result.Count != 2 || applyCalls != 1 || healthCheckCalls != 2 || portalHealthCalls != 1 || restoreCalls != 0 {
		t.Fatalf("unexpected rotation counters: result=%#v apply=%d healthCheck=%d portalHealth=%d restore=%d", result, applyCalls, healthCheckCalls, portalHealthCalls, restoreCalls)
	}
	assertEndpointStatusForTest(t, oldA.Id, model.EndpointStatusDraining)
	assertEndpointStatusForTest(t, oldB.Id, model.EndpointStatusDraining)
	assertActiveEndpointCountForTest(t, 2)
	var storedPortal model.Tunnel
	if err := database.GetDB().First(&storedPortal, portal.Id).Error; err != nil {
		t.Fatal(err)
	}
	newAHost := ""
	for _, item := range result.Items {
		if item.InboundId == inboundA.Id {
			newAHost = item.NewHost
		}
	}
	if newAHost == "" || storedPortal.RemoteAddress != newAHost {
		t.Fatalf("portal remote address = %q, want rotated host %q", storedPortal.RemoteAddress, newAHost)
	}
}

func TestRotateAllHealthCheckFailureRollsBackWholeBatch(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "rotate-healthcheck-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, oldA := createPublishedManagedInboundForTest(t, "healthcheck-a", "a", 26417, "/a", "a.asdasdasdas.shop")
	_, oldB := createPublishedManagedInboundForTest(t, "healthcheck-b", "b", 26418, "/b", "b.asdasdasdas.shop")

	applyCalls := 0
	healthCheckCalls := 0
	restoreCalls := 0
	service := &EndpointService{
		applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
			applyCalls++
			return "old-caddy", nil
		},
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error {
			healthCheckCalls++
			if healthCheckCalls == 2 {
				return errors.New("injected health-check failure")
			}
			return nil
		},
		restoreCaddyHook: func(content string) error {
			restoreCalls++
			if content != "old-caddy" {
				t.Fatalf("rollback content = %q", content)
			}
			return nil
		},
	}
	if _, err := service.RotateAll(1); err == nil {
		t.Fatal("RotateAll() unexpectedly succeeded after one health check failed")
	}
	if applyCalls != 1 || healthCheckCalls != 2 || restoreCalls != 1 {
		t.Fatalf("unexpected rollback counters: apply=%d healthCheck=%d restore=%d", applyCalls, healthCheckCalls, restoreCalls)
	}
	assertEndpointStatusForTest(t, oldA.Id, model.EndpointStatusActive)
	assertEndpointStatusForTest(t, oldB.Id, model.EndpointStatusActive)
	assertNoPendingEndpointsForTest(t)
	assertRetiredEndpointCountForTest(t, 2)
}

func TestRotateAllPortalListenerFailureRollsBackWholeBatch(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "rotate-portal-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, old := createPublishedManagedInboundForTest(t, "portal-health", "portal-health", 26417, "/business", "portal.asdasdasdas.shop")
	portal := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		RemoteAddress:    old.Host,
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "44444444-4444-4444-4444-444444444444",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26419,
		XHttpPath:        "/portal-health",
	}
	if err := database.GetDB().Create(portal).Error; err != nil {
		t.Fatal(err)
	}
	restoreCalls := 0
	service := &EndpointService{
		applyManagedSiteHook: func(baseDomain string, block string) (string, error) { return "old-caddy", nil },
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error { return nil },
		healthCheckPortalHook: func(portal *model.Tunnel) error { return errors.New("portal listener unavailable") },
		restoreCaddyHook: func(content string) error {
			restoreCalls++
			return nil
		},
	}
	if _, err := service.RotateAll(1); err == nil || !strings.Contains(err.Error(), "portal") {
		t.Fatalf("RotateAll() portal failure error = %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("portal failure restored Caddy %d times, want 1", restoreCalls)
	}
	assertEndpointStatusForTest(t, old.Id, model.EndpointStatusActive)
	assertNoPendingEndpointsForTest(t)
	assertRetiredEndpointCountForTest(t, 1)
}

func TestStartupReconcileFailureMarksManagedStateUnhealthy(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "startup-reconcile-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, _ = createPublishedManagedInboundForTest(t, "startup-reconcile", "startup-reconcile", 26417, "/fixed", "startup.asdasdasdas.shop")
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		return "", errors.New("injected reconcile failure")
	}}
	if err := service.StartupReconcile(); err == nil {
		t.Fatal("StartupReconcile() unexpectedly succeeded")
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state remained healthy after startup reconcile failure")
	}
	defer setManagedStateHealthy(true)
}

func TestRotateAllDBSwitchFailureRestoresCaddyAndKeepsOldActive(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "rotate-db-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	_, oldA := createPublishedManagedInboundForTest(t, "db-a", "a", 26417, "/a", "a.asdasdasdas.shop")
	_, oldB := createPublishedManagedInboundForTest(t, "db-b", "b", 26418, "/b", "b.asdasdasdas.shop")

	applyCalls := 0
	restoreCalls := 0
	service := &EndpointService{
		applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
			applyCalls++
			return "old-caddy", nil
		},
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error { return nil },
		commitRotationHook: func(items []rotationItem, retireAt int64) error {
			return errors.New("injected database switch failure")
		},
		restoreCaddyHook: func(content string) error {
			restoreCalls++
			return nil
		},
	}
	if _, err := service.RotateAll(1); err == nil {
		t.Fatal("RotateAll() unexpectedly succeeded after DB switch failure")
	}
	if applyCalls != 1 || restoreCalls != 1 {
		t.Fatalf("unexpected DB failure counters: apply=%d restore=%d", applyCalls, restoreCalls)
	}
	assertEndpointStatusForTest(t, oldA.Id, model.EndpointStatusActive)
	assertEndpointStatusForTest(t, oldB.Id, model.EndpointStatusActive)
	assertNoPendingEndpointsForTest(t)
	assertRetiredEndpointCountForTest(t, 2)
}

func TestCommitRotationDatabaseTransactionRollsBackPartialUpdates(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "commit-transaction.db")); err != nil {
		t.Fatal(err)
	}
	inboundA, oldA := createPublishedManagedInboundForTest(t, "tx-a", "a", 26417, "/a", "a.asdasdasdas.shop")
	inboundB, oldB := createPublishedManagedInboundForTest(t, "tx-b", "b", 26418, "/b", "b.asdasdasdas.shop")
	newA := &model.PublicEndpoint{InboundId: inboundA.Id, Host: "new-a.asdasdasdas.shop", Port: 443, Status: model.EndpointStatusPending, CreatedAt: 10}
	newB := &model.PublicEndpoint{InboundId: inboundB.Id, Host: "new-b.asdasdasdas.shop", Port: 443, Status: model.EndpointStatusPending, CreatedAt: 11}
	if err := database.GetDB().Create(newA).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(newB).Error; err != nil {
		t.Fatal(err)
	}
	specA, _ := validateManagedInbound(inboundA)
	specB, _ := validateManagedInbound(inboundB)
	trigger := fmt.Sprintf(`CREATE TRIGGER fail_second_activation
BEFORE UPDATE OF status ON public_endpoints
WHEN NEW.id = %d AND NEW.status = 'active'
BEGIN
    SELECT RAISE(ABORT, 'injected transaction failure');
END;`, newB.Id)
	if err := database.GetDB().Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	items := []rotationItem{
		{inbound: inboundA, old: oldA, next: newA, spec: specA},
		{inbound: inboundB, old: oldB, next: newB, spec: specB},
	}
	if err := (&EndpointService{}).commitRotation(items, 9999999999); err == nil {
		t.Fatal("commitRotation() unexpectedly succeeded despite abort trigger")
	}
	assertEndpointStatusForTest(t, oldA.Id, model.EndpointStatusActive)
	assertEndpointStatusForTest(t, oldB.Id, model.EndpointStatusActive)
	assertEndpointStatusForTest(t, newA.Id, model.EndpointStatusPending)
	assertEndpointStatusForTest(t, newB.Id, model.EndpointStatusPending)
}

func TestEnsureHostAvailableNeverReusesRetiredHost(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "retired-host.db")); err != nil {
		t.Fatal(err)
	}
	endpoint := &model.PublicEndpoint{Host: "blocked.asdasdasdas.shop", Port: 443, Status: model.EndpointStatusRetired}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&EndpointService{}).ensureHostAvailable(endpoint.Host); err == nil {
		t.Fatal("retired hostname unexpectedly became reusable")
	}
}

func TestEndpointHistoryRemainsReservedAfterInboundRemoval(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "delete-history.db")); err != nil {
		t.Fatal(err)
	}
	inbound, endpoint := createPublishedManagedInboundForTest(t, "delete-history", "delete", 26417, "/fixed", "deleted.asdasdasdas.shop")
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("inbound_id = ?", inbound.Id).
		Updates(map[string]interface{}{"status": model.EndpointStatusRetired, "retire_at": int64(1)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Delete(inbound).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.PublicEndpoint
	if err := database.GetDB().First(&stored, endpoint.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.EndpointStatusRetired || stored.Host != endpoint.Host {
		t.Fatalf("endpoint history was not preserved: %#v", stored)
	}
	if err := (&EndpointService{}).ensureHostAvailable(endpoint.Host); err == nil {
		t.Fatal("deleted inbound hostname unexpectedly became reusable")
	}
}

func TestPublishedInboundUpdateRejectsUnsupportedShapeBeforeSave(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "published-update.db")); err != nil {
		t.Fatal(err)
	}
	inbound, _ := createPublishedManagedInboundForTest(t, "published-update", "published", 26417, "/fixed", "published.asdasdasdas.shop")
	candidate := *inbound
	candidate.StreamSettings = `{"network":"ws","security":"none","wsSettings":{"path":"/fixed"}}`
	if err := (&InboundService{}).UpdateInbound(&candidate); err == nil {
		t.Fatal("published inbound unexpectedly accepted unsupported WebSocket transport")
	}
	stored := &model.Inbound{}
	if err := database.GetDB().First(stored, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.StreamSettings != inbound.StreamSettings {
		t.Fatalf("invalid published update was persisted: %s", stored.StreamSettings)
	}
}

func assertEndpointStatusForTest(t *testing.T, id int, want string) {
	t.Helper()
	var endpoint model.PublicEndpoint
	if err := database.GetDB().First(&endpoint, id).Error; err != nil {
		t.Fatal(err)
	}
	if endpoint.Status != want {
		t.Fatalf("endpoint %d status = %q, want %q", id, endpoint.Status, want)
	}
}

func assertActiveEndpointCountForTest(t *testing.T, want int64) {
	t.Helper()
	var count int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("status = ?", model.EndpointStatusActive).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("active endpoint count = %d, want %d", count, want)
	}
}

func assertNoPendingEndpointsForTest(t *testing.T) {
	t.Helper()
	var count int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("status = ?", model.EndpointStatusPending).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending endpoint count = %d, want 0", count)
	}
}

func assertRetiredEndpointCountForTest(t *testing.T, want int64) {
	t.Helper()
	var count int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("status = ?", model.EndpointStatusRetired).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("retired endpoint count = %d, want %d", count, want)
	}
}
