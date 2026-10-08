package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		applyManagedSiteHook:    func(baseDomain string, block string) (string, error) { return "old-caddy", nil },
		healthCheckEndpointHook: func(inbound *model.Inbound, endpoint *model.PublicEndpoint, healthPath string) error { return nil },
		healthCheckPortalHook:   func(portal *model.Tunnel) error { return errors.New("portal listener unavailable") },
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

func TestStartupReconcileAppliesCaddyWhenOnlyRetiredHistoryRemains(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "startup-retired-only.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	retired := &model.PublicEndpoint{
		InboundId: 999,
		Host:      "retired.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}
	if err := database.GetDB().Create(retired).Error; err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		applyCalls++
		if strings.Contains(block, retired.Host) {
			t.Fatalf("retired host leaked into reconciled Caddy block:\n%s", block)
		}
		return "stale-caddy-containing-retired-host", nil
	}}
	if err := service.StartupReconcile(); err != nil {
		t.Fatalf("StartupReconcile() error = %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("StartupReconcile() Caddy apply calls = %d, want 1", applyCalls)
	}
	if !isManagedStateHealthy() {
		t.Fatal("managed state remained unhealthy after retired-only reconcile")
	}
}

func TestEndpointSettingsKeepManagedZoneStableAfterRetiredHistoryExists(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "settings-retired-history.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	retired := &model.PublicEndpoint{
		InboundId: 999,
		Host:      "retired.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}
	if err := database.GetDB().Create(retired).Error; err != nil {
		t.Fatal(err)
	}
	settings, err := (&SettingService{}).GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		applyCalls++
		return "old-caddy", nil
	}}
	updated := *settings
	updated.HostRandomLength++
	if err := service.UpdateSettings(&updated, "panel.example.net"); err != nil {
		t.Fatalf("UpdateSettings() non-zone change error = %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("UpdateSettings() retired-only Caddy applies = %d, want 1", applyCalls)
	}

	changedZone := updated
	changedZone.PublicBaseDomain = "other.example.net"
	if err := service.UpdateSettings(&changedZone, "panel.example.net"); err == nil || !strings.Contains(err.Error(), "managed endpoint history") {
		t.Fatalf("UpdateSettings() managed-zone change error = %v", err)
	}
}

func TestFailedMutationReportsPendingCleanupFailureAndFailsClosed(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "pending-cleanup-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound := validManagedInboundForTest(0, "cleanup-fail", 26417, "/cleanup")
	inbound.Tag = "pending-cleanup-fail"
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	trigger := `CREATE TRIGGER fail_pending_retire
BEFORE UPDATE OF status ON public_endpoints
WHEN OLD.status = 'pending' AND NEW.status = 'retired'
BEGIN
    SELECT RAISE(ABORT, 'injected pending cleanup failure');
END;`
	if err := database.GetDB().Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		return "", errors.New("injected Caddy apply failure")
	}}
	_, err := service.InitializeBatch(1, &EndpointBatchInit{Items: []*EndpointInit{
		{InboundId: inbound.Id, Host: "cleanup.asdasdasdas.shop", Port: 443},
	}})
	if err == nil || !strings.Contains(err.Error(), "injected Caddy apply failure") || !strings.Contains(err.Error(), "pending cleanup failed") {
		t.Fatalf("InitializeBatch() cleanup failure error = %v", err)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after pending cleanup failure")
	}
	defer setManagedStateHealthy(true)
	var pendingCount int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).Where("status = ?", model.EndpointStatusPending).Count(&pendingCount).Error; err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 {
		t.Fatalf("pending endpoint count = %d, want 1 after injected cleanup failure", pendingCount)
	}
}

func TestCommitRotationUpdatesLegacyUppercasePortalHost(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "portal-uppercase.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, old := createPublishedManagedInboundForTest(t, "portal-uppercase", "portal-uppercase", 26417, "/fixed", "cdn.asdasdasdas.shop")
	next := &model.PublicEndpoint{
		InboundId: inbound.Id,
		Host:      "next.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusPending,
		CreatedAt: old.CreatedAt + 1,
	}
	if err := database.GetDB().Create(next).Error; err != nil {
		t.Fatal(err)
	}
	portal := &model.Tunnel{
		UserId:           1,
		Enable:           true,
		Mode:             TunnelModePortal,
		RemoteAddress:    "CDN.ASDASDASDAS.SHOP",
		RemotePort:       443,
		Protocol:         "vless",
		UUID:             "55555555-5555-5555-5555-555555555555",
		PortalTransport:  PortalTransportXHTTP,
		PortalListenPort: 26418,
		XHttpPath:        "/portal-fixed",
	}
	if err := database.GetDB().Create(portal).Error; err != nil {
		t.Fatal(err)
	}
	spec, err := validateManagedInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&EndpointService{}).commitRotation([]rotationItem{{inbound: inbound, old: old, next: next, spec: spec}}, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatalf("commitRotation() error = %v", err)
	}
	var stored model.Tunnel
	if err := database.GetDB().First(&stored, portal.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RemoteAddress != next.Host {
		t.Fatalf("Portal RemoteAddress = %q, want %q", stored.RemoteAddress, next.Host)
	}
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

func TestManagedInboundWithLiveEndpointRejectsConnectionCriticalChanges(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "managed-inbound-critical-lock.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "critical-lock", "critical", 26417, "/fixed", "critical.asdasdasdas.shop")

	tests := []struct {
		name        string
		mutate      func(*model.Inbound)
		wantMessage string
	}{
		{name: "listen", mutate: func(candidate *model.Inbound) { candidate.Listen = "0.0.0.0" }, wantMessage: "连接关键字段"},
		{name: "port", mutate: func(candidate *model.Inbound) { candidate.Port++ }, wantMessage: "连接关键字段"},
		{name: "protocol", mutate: func(candidate *model.Inbound) { candidate.Protocol = model.VMess }, wantMessage: "XHTTP 传输仅支持 VLESS"},
		{name: "settings", mutate: func(candidate *model.Inbound) {
			candidate.Settings = `{"clients":[{"id":"22222222-2222-2222-2222-222222222222","flow":""}],"decryption":"none"}`
		}, wantMessage: "连接关键字段"},
		{name: "streamSettings", mutate: func(candidate *model.Inbound) {
			candidate.StreamSettings = `{"network":"xhttp","security":"none","xhttpSettings":{"path":"/changed","host":"","mode":"auto"}}`
		}, wantMessage: "连接关键字段"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := *inbound
			tt.mutate(&candidate)
			err := (&InboundService{}).UpdateInbound(&candidate)
			if err == nil || !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("UpdateInbound() critical change error = %v", err)
			}
			stored := &model.Inbound{}
			if err := database.GetDB().First(stored, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Listen != inbound.Listen || stored.Port != inbound.Port || stored.Settings != inbound.Settings || stored.StreamSettings != inbound.StreamSettings {
				t.Fatalf("critical managed inbound fields changed despite lock: %#v", stored)
			}
		})
	}
}

func TestManagedInboundWithLiveEndpointAllowsNonCriticalChanges(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "managed-inbound-noncritical.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "noncritical", "before", 26417, "/fixed", "noncritical.asdasdasdas.shop")
	candidate := *inbound
	candidate.Remark = "after"
	candidate.Total = 12345
	candidate.ExpiryTime = time.Now().Add(time.Hour).UnixMilli()
	candidate.Settings = `{ "decryption":"none", "clients":[ { "flow":"", "id":"11111111-1111-1111-1111-111111111111" } ] }`

	syncCalls := 0
	service := &InboundService{syncManagedRoutesHook: func() error {
		syncCalls++
		return nil
	}}
	if err := service.UpdateInbound(&candidate); err != nil {
		t.Fatalf("UpdateInbound() non-critical change error = %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("managed Caddy sync calls = %d, want 1", syncCalls)
	}
	stored := &model.Inbound{}
	if err := database.GetDB().First(stored, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Remark != candidate.Remark || stored.Total != candidate.Total || stored.ExpiryTime != candidate.ExpiryTime {
		t.Fatalf("non-critical changes were not saved: %#v", stored)
	}
}

func TestManagedInboundUpdateCaddyFailureRestoresAndFailsClosed(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "managed-inbound-update-caddy-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	inbound, _ := createPublishedManagedInboundForTest(t, "update-caddy-fail", "before", 26417, "/fixed", "update-fail.asdasdasdas.shop")
	candidate := *inbound
	candidate.Remark = "after"
	service := &InboundService{syncManagedRoutesHook: func() error {
		return errors.New("injected Caddy sync failure")
	}}
	err := service.UpdateInbound(&candidate)
	if err == nil || !strings.Contains(err.Error(), "Caddy") {
		t.Fatalf("UpdateInbound() Caddy failure error = %v", err)
	}
	stored := &model.Inbound{}
	if err := database.GetDB().First(stored, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Remark != inbound.Remark {
		t.Fatalf("Inbound rollback failed: remark=%q want %q", stored.Remark, inbound.Remark)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after Inbound Caddy sync failure")
	}
	defer setManagedStateHealthy(true)
}

func TestSyncManagedRoutesFailureFailsClosed(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "sync-managed-fail-closed.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		return "", errors.New("injected managed Caddy apply failure")
	}}
	if err := service.SyncManagedRoutes(); err == nil {
		t.Fatal("SyncManagedRoutes() unexpectedly succeeded")
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after Caddy sync failure")
	}
	defer setManagedStateHealthy(true)
}

func TestStartupReconcileDoesNotBecomeHealthyBeforeXray(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "startup-waits-xray.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	setManagedXrayHealthy(false)
	if err := (&EndpointService{}).StartupReconcile(); err != nil {
		t.Fatalf("StartupReconcile() error = %v", err)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state became healthy before Xray start succeeded")
	}
	setManagedXrayHealthy(true)
	if !isManagedStateHealthy() {
		t.Fatal("managed state did not become healthy after Caddy reconcile and Xray success")
	}
}

func TestEndpointSettingsLockTLSPathsAfterHistoryExists(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "endpoint-tls-lock.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	if err := database.GetDB().Create(&model.PublicEndpoint{
		InboundId: 999,
		Host:      "history.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settings, err := (&SettingService{}).GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.CaddyTLSCertFile = "/new/fullchain.pem"
	settings.CaddyTLSKeyFile = "/new/privkey.pem"
	if err := (&EndpointService{}).UpdateSettings(settings, "panel.example.net"); err == nil || !strings.Contains(err.Error(), "certificate path") {
		t.Fatalf("UpdateSettings() TLS path lock error = %v", err)
	}
}

func TestEndpointSettingsCaddyFailureRestoresAndFailsClosed(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "endpoint-settings-caddy-fail.db")); err != nil {
		t.Fatal(err)
	}
	configureEndpointSettingsForTest(t)
	if err := database.GetDB().Create(&model.PublicEndpoint{
		InboundId: 999,
		Host:      "history-settings.asdasdasdas.shop",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingService := &SettingService{}
	settings, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	oldHostLength := settings.HostRandomLength
	settings.HostRandomLength++
	service := &EndpointService{applyManagedSiteHook: func(baseDomain string, block string) (string, error) {
		return "", errors.New("injected settings Caddy apply failure")
	}}
	err = service.UpdateSettings(settings, "panel.example.net")
	if err == nil {
		t.Fatal("UpdateSettings() unexpectedly succeeded")
	}
	stored, err := settingService.GetEndpointSettings()
	if err != nil {
		t.Fatal(err)
	}
	if stored.HostRandomLength != oldHostLength {
		t.Fatalf("Endpoint settings rollback failed: hostRandomLength=%d want %d", stored.HostRandomLength, oldHostLength)
	}
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy after Endpoint settings Caddy failure")
	}
	defer setManagedStateHealthy(true)
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
