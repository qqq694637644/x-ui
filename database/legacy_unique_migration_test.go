package database

import (
	"path/filepath"
	"testing"
	"x-ui/database/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLegacyPortUniqueConstraintsAreRemovedWithoutLosingData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE inbounds (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  port INTEGER UNIQUE,
  tag TEXT UNIQUE,
  settings TEXT,
  stream_settings TEXT
)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE tunnels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  listen_port INTEGER UNIQUE
)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO inbounds (port, tag, settings, stream_settings) VALUES (18080, 'legacy-inbound', '{}', '{}')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO tunnels (listen_port) VALUES (18081)`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB() migration error = %v", err)
	}
	migratedSQLDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migratedSQLDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("second InitDB() after migration error = %v", err)
	}

	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "inbounds", column: "port"},
		{table: "tunnels", column: "listen_port"},
	} {
		hasUnique, err := hasSingleColumnUniqueIndex(db, check.table, check.column)
		if err != nil {
			t.Fatal(err)
		}
		if hasUnique {
			t.Fatalf("legacy unique constraint still exists on %s.%s", check.table, check.column)
		}
	}

	var inboundCount int64
	if err := db.Model(&model.Inbound{}).Where("tag = ?", "legacy-inbound").Count(&inboundCount).Error; err != nil {
		t.Fatal(err)
	}
	if inboundCount != 1 {
		t.Fatalf("legacy inbound count = %d, want 1", inboundCount)
	}
	var tunnelCount int64
	if err := db.Model(&model.Tunnel{}).Where("listen_port = ?", 18081).Count(&tunnelCount).Error; err != nil {
		t.Fatal(err)
	}
	if tunnelCount != 1 {
		t.Fatalf("legacy tunnel count = %d, want 1", tunnelCount)
	}

	for _, inbound := range []*model.Inbound{
		{Port: 19000, Tag: "same-port-tcp", Protocol: model.Dokodemo, Settings: `{"network":"tcp"}`, StreamSettings: `{}`},
		{Port: 19000, Tag: "same-port-udp", Protocol: model.Dokodemo, Settings: `{"network":"udp"}`, StreamSettings: `{}`},
	} {
		if err := db.Create(inbound).Error; err != nil {
			t.Fatalf("same numeric inbound port should be allowed after migration: %v", err)
		}
	}
	for _, tunnel := range []*model.Tunnel{
		{Listen: "0.0.0.0", ListenPort: 19001, Network: "tcp"},
		{Listen: "0.0.0.0", ListenPort: 19001, Network: "udp"},
	} {
		if err := db.Create(tunnel).Error; err != nil {
			t.Fatalf("same numeric tunnel listen port should be allowed after migration: %v", err)
		}
	}

	if err := db.Create(&model.Inbound{Port: 19002, Tag: "same-port-tcp"}).Error; err == nil {
		t.Fatal("inbound tag uniqueness was lost during table rebuild")
	}
}
