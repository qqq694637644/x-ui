package service

import (
	"fmt"
	"strings"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/xray_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

type InboundService struct {
}

func (s *InboundService) GetInbounds(userId int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("user_id = ?", userId).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) GetAllInbounds() ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) inboundTagExists(tag string, ignoreID int) (bool, error) {
	db := database.GetDB()
	db = db.Model(model.Inbound{}).Where("tag = ?", tag)
	if ignoreID > 0 {
		db = db.Where("id != ?", ignoreID)
	}
	var count int64
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *InboundService) assignUniqueInboundTag(inbound *model.Inbound, ignoreID int, reserved map[string]struct{}) error {
	base := strings.TrimSpace(inbound.Tag)
	if base == "" {
		base = fmt.Sprintf("inbound-%v", inbound.Port)
	}
	for suffix := 1; suffix <= 10000; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, exists := reserved[candidate]; exists {
			continue
		}
		exists, err := s.inboundTagExists(candidate, ignoreID)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		inbound.Tag = candidate
		if reserved != nil {
			reserved[candidate] = struct{}{}
		}
		return nil
	}
	return fmt.Errorf("无法为入站端口 %d 分配唯一 tag", inbound.Port)
}

func (s *InboundService) AddInbound(inbound *model.Inbound) error {
	if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
		return err
	}
	if err := checkInboundListenerConflicts(inbound, 0); err != nil {
		return err
	}
	if err := s.assignUniqueInboundTag(inbound, 0, nil); err != nil {
		return err
	}
	db := database.GetDB()
	return db.Save(inbound).Error
}

func (s *InboundService) AddInbounds(inbounds []*model.Inbound) error {
	reservedTags := make(map[string]struct{}, len(inbounds))
	endpoints := make([]listenerEndpoint, 0, len(inbounds))
	for _, inbound := range inbounds {
		if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
			return err
		}
		if err := checkInboundListenerConflicts(inbound, 0); err != nil {
			return err
		}
		endpoint, err := inboundListenerEndpoint(inbound)
		if err != nil {
			return err
		}
		for _, existing := range endpoints {
			if endpointsConflict(endpoint, existing) {
				return endpointConflictError(endpoint, existing)
			}
		}
		endpoints = append(endpoints, endpoint)
		if err := s.assignUniqueInboundTag(inbound, 0, reservedTags); err != nil {
			return err
		}
	}

	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	for _, inbound := range inbounds {
		err = tx.Save(inbound).Error
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *InboundService) DelInbound(id int) error {
	db := database.GetDB()
	return db.Delete(model.Inbound{}, id).Error
}

func (s *InboundService) GetInbound(id int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).First(inbound, id).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

func (s *InboundService) UpdateInbound(inbound *model.Inbound) error {
	if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
		return err
	}
	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return err
	}
	if err := checkInboundListenerConflicts(inbound, inbound.Id); err != nil {
		return err
	}
	oldInbound.Up = inbound.Up
	oldInbound.Down = inbound.Down
	oldInbound.Total = inbound.Total
	oldInbound.Remark = inbound.Remark
	oldInbound.Enable = inbound.Enable
	oldInbound.ExpiryTime = inbound.ExpiryTime
	oldInbound.Listen = inbound.Listen
	oldInbound.Port = inbound.Port
	oldInbound.Protocol = inbound.Protocol
	oldInbound.Settings = inbound.Settings
	oldInbound.StreamSettings = inbound.StreamSettings
	oldInbound.Sniffing = inbound.Sniffing
	// Preserve the existing unique tag when the listen port changes. Multiple
	// inbounds may now share a numeric port when their address/protocols do not
	// overlap, so a port-only tag is no longer unique.

	db := database.GetDB()
	return db.Save(oldInbound).Error
}

func (s *InboundService) AddTraffic(traffics []*xray.Traffic) (err error) {
	if len(traffics) == 0 {
		return nil
	}
	db := database.GetDB()
	db = db.Model(model.Inbound{})
	tx := db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Where("tag = ?", traffic.Tag).
				UpdateColumn("up", gorm.Expr("up + ?", traffic.Up)).
				UpdateColumn("down", gorm.Expr("down + ?", traffic.Down)).
				Error
			if err != nil {
				return
			}
		}
	}
	return
}

func (s *InboundService) DisableInvalidInbounds() (int64, error) {
	db := database.GetDB()
	now := time.Now().Unix() * 1000
	result := db.Model(model.Inbound{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Update("enable", false)
	err := result.Error
	count := result.RowsAffected
	return count, err
}
