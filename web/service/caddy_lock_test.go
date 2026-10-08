package service

import (
	"path/filepath"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
)

func TestCaddyManualMutationLockOnlyForLiveManagedEndpoints(t *testing.T) {
	for _, status := range []string{
		model.EndpointStatusPending,
		model.EndpointStatusActive,
		model.EndpointStatusDraining,
	} {
		t.Run(status, func(t *testing.T) {
			if err := database.InitDB(filepath.Join(t.TempDir(), "caddy-lock.db")); err != nil {
				t.Fatal(err)
			}
			service := &CaddyService{}
			locked, err := service.ManualMutationLocked()
			if err != nil {
				t.Fatal(err)
			}
			if locked {
				t.Fatal("manual Caddy mutation locked without live endpoints")
			}
			endpoint := &model.PublicEndpoint{
				InboundId: 1,
				Host:      status + ".example.com",
				Port:      443,
				Status:    status,
				CreatedAt: 1,
			}
			if err := database.GetDB().Create(endpoint).Error; err != nil {
				t.Fatal(err)
			}
			locked, err = service.ManualMutationLocked()
			if err != nil {
				t.Fatal(err)
			}
			if !locked {
				t.Fatalf("manual Caddy mutation not locked for %s endpoint", status)
			}
			if err := service.EnsureManualMutationAllowed(); err == nil {
				t.Fatalf("manual Caddy mutation unexpectedly allowed for %s endpoint", status)
			}
		})
	}
}

func TestCaddyManualMutationAllowsRetiredOnlyHistory(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "caddy-retired-unlocked.db")); err != nil {
		t.Fatal(err)
	}
	endpoint := &model.PublicEndpoint{
		InboundId: 1,
		Host:      "retired.example.com",
		Port:      443,
		Status:    model.EndpointStatusRetired,
		CreatedAt: 1,
		RetireAt:  2,
	}
	if err := database.GetDB().Create(endpoint).Error; err != nil {
		t.Fatal(err)
	}
	service := &CaddyService{}
	locked, err := service.ManualMutationLocked()
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Fatal("retired-only history incorrectly locked manual Caddy page")
	}
	if err := service.EnsureManualMutationAllowed(); err != nil {
		t.Fatalf("retired-only history unexpectedly blocked manual Caddy mutation: %v", err)
	}
}
