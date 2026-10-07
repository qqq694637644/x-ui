package database

import (
	"path/filepath"
	"testing"

	"x-ui/database/model"
)

func TestPublicEndpointAllowsOnlyOneActivePerInbound(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "public-endpoint-unique.db")); err != nil {
		t.Fatal(err)
	}
	first := &model.PublicEndpoint{
		InboundId: 7,
		Host:      "first.example.com",
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: 1,
	}
	if err := GetDB().Create(first).Error; err != nil {
		t.Fatal(err)
	}
	second := &model.PublicEndpoint{
		InboundId: 7,
		Host:      "second.example.com",
		Port:      443,
		Status:    model.EndpointStatusActive,
		CreatedAt: 2,
	}
	if err := GetDB().Create(second).Error; err == nil {
		t.Fatal("database unexpectedly allowed two active endpoints for one inbound")
	}
	pending := &model.PublicEndpoint{
		InboundId: 7,
		Host:      "pending.example.com",
		Port:      443,
		Status:    model.EndpointStatusPending,
		CreatedAt: 3,
	}
	if err := GetDB().Create(pending).Error; err != nil {
		t.Fatalf("database rejected pending endpoint alongside active endpoint: %v", err)
	}
}
