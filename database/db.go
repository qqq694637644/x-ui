package database

import (
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io/fs"
	"os"
	"path"
	"x-ui/config"
	"x-ui/database/model"
	"x-ui/util/xray_util"
)

var db *gorm.DB

type legacyTunnelMkcpColumns struct {
	KcpHeaderType string `gorm:"column:kcp_header_type"`
	KcpSeed       string `gorm:"column:kcp_seed"`
}

func (legacyTunnelMkcpColumns) TableName() string {
	return "tunnels"
}

func initUser() error {
	err := db.AutoMigrate(&model.User{})
	if err != nil {
		return err
	}
	var count int64
	err = db.Model(&model.User{}).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		user := &model.User{
			Username: "admin",
			Password: "admin",
		}
		return db.Create(user).Error
	}
	return nil
}

func initInbound() error {
	if err := db.AutoMigrate(&model.Inbound{}); err != nil {
		return err
	}
	return validateInboundStreamSettingsForXray26327()
}

func validateInboundStreamSettingsForXray26327() error {
	var inbounds []*model.Inbound
	if err := db.Model(&model.Inbound{}).Find(&inbounds).Error; err != nil {
		return err
	}
	for _, inbound := range inbounds {
		if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
			return fmt.Errorf("inbound id=%d tag=%s port=%d stream_settings 不兼容 Xray-core 26.3.27: %w", inbound.Id, inbound.Tag, inbound.Port, err)
		}
	}
	return nil
}

func initTunnel() error {
	if err := db.AutoMigrate(&model.Tunnel{}); err != nil {
		return err
	}
	if db.Migrator().HasColumn(&legacyTunnelMkcpColumns{}, "kcp_header_type") {
		if err := db.Migrator().DropColumn(&legacyTunnelMkcpColumns{}, "kcp_header_type"); err != nil {
			return err
		}
	}
	if db.Migrator().HasColumn(&legacyTunnelMkcpColumns{}, "kcp_seed") {
		if err := db.Migrator().DropColumn(&legacyTunnelMkcpColumns{}, "kcp_seed"); err != nil {
			return err
		}
	}
	return nil
}

func initSetting() error {
	return db.AutoMigrate(&model.Setting{})
}

func InitDB(dbPath string) error {
	dir := path.Dir(dbPath)
	err := os.MkdirAll(dir, fs.ModeDir)
	if err != nil {
		return err
	}

	var gormLogger logger.Interface

	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	c := &gorm.Config{
		Logger: gormLogger,
	}
	db, err = gorm.Open(sqlite.Open(dbPath), c)
	if err != nil {
		return err
	}

	err = initUser()
	if err != nil {
		return err
	}
	err = initInbound()
	if err != nil {
		return err
	}
	err = initTunnel()
	if err != nil {
		return err
	}
	err = initSetting()
	if err != nil {
		return err
	}

	return nil
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
