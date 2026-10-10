package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/common"
	"x-ui/util/xray_util"
	"x-ui/xray"

	"gorm.io/gorm"
)

type InboundService struct {
	syncManagedRoutesHook func() error
}

func (s *InboundService) syncManagedRoutes() error {
	if s.syncManagedRoutesHook != nil {
		err := s.syncManagedRoutesHook()
		setManagedCaddyHealthy(err == nil)
		return err
	}
	return (&EndpointService{}).SyncManagedRoutes()
}

func inboundJSONEquivalent(a string, b string) bool {
	var left interface{}
	var right interface{}
	if err := json.Unmarshal([]byte(a), &left); err != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	if err := json.Unmarshal([]byte(b), &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func managedInboundCriticalChanges(oldInbound *model.Inbound, nextInbound *model.Inbound) []string {
	changes := make([]string, 0, 5)
	if strings.TrimSpace(oldInbound.Listen) != strings.TrimSpace(nextInbound.Listen) {
		changes = append(changes, "Listen")
	}
	if oldInbound.Port != nextInbound.Port {
		changes = append(changes, "Port")
	}
	if oldInbound.Protocol != nextInbound.Protocol {
		changes = append(changes, "Protocol")
	}
	if !inboundJSONEquivalent(oldInbound.Settings, nextInbound.Settings) {
		changes = append(changes, "Settings")
	}
	if !inboundJSONEquivalent(oldInbound.StreamSettings, nextInbound.StreamSettings) {
		changes = append(changes, "StreamSettings")
	}
	return changes
}

func validateInboundTransport(inbound *model.Inbound) error {
	if strings.TrimSpace(inbound.StreamSettings) == "" {
		return nil
	}
	stream := struct {
		Network string `json:"network"`
	}{}
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return err
	}
	if (strings.EqualFold(strings.TrimSpace(stream.Network), "xhttp") || strings.EqualFold(strings.TrimSpace(stream.Network), "splithttp")) && inbound.Protocol != model.VLESS {
		return fmt.Errorf("XHTTP 传输仅支持 VLESS 入站")
	}
	return nil
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
	// Public subscription state is only enabled through EndpointService after
	// an active endpoint and strict managed configuration have been validated.
	inbound.Publish = false
	if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
		return err
	}
	if err := validateInboundTransport(inbound); err != nil {
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
		inbound.Publish = false
		if err := xray_util.ValidateXray26327StreamSettings(inbound.StreamSettings); err != nil {
			return err
		}
		if err := validateInboundTransport(inbound); err != nil {
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
	oldInbound := &model.Inbound{}
	if err := db.First(oldInbound, id).Error; err != nil {
		return err
	}
	var oldEndpoints []*model.PublicEndpoint
	if err := db.Where("inbound_id = ?", id).Find(&oldEndpoints).Error; err != nil {
		return err
	}
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err := tx.Model(&model.PublicEndpoint{}).Where("inbound_id = ?", id).
		Updates(map[string]interface{}{"status": model.EndpointStatusRetired, "retire_at": time.Now().Unix()}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Delete(model.Inbound{}, id).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	if len(oldEndpoints) == 0 {
		return nil
	}
	if err := s.syncManagedRoutes(); err != nil {
		restoreErr := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(oldInbound).Error; err != nil {
				return err
			}
			for _, endpoint := range oldEndpoints {
				if err := tx.Save(endpoint).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if restoreErr != nil {
			setManagedDataHealthy(false)
			return common.NewError("删除入站后的 Caddy 同步失败: ", err, "; 数据恢复失败: ", restoreErr)
		}
		return common.NewError("删除入站后的 Caddy 同步失败，数据库已恢复: ", err)
	}
	return nil
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
	if err := validateInboundTransport(inbound); err != nil {
		return err
	}
	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return err
	}
	var liveEndpointCount int64
	if err := database.GetDB().Model(&model.PublicEndpoint{}).
		Where("inbound_id = ? AND status IN ?", inbound.Id, []string{model.EndpointStatusPending, model.EndpointStatusActive, model.EndpointStatusDraining}).
		Count(&liveEndpointCount).Error; err != nil {
		return err
	}
	if liveEndpointCount > 0 {
		if changes := managedInboundCriticalChanges(oldInbound, inbound); len(changes) > 0 {
			return common.NewError("已有公网入口的入站不允许修改连接关键字段 ", strings.Join(changes, ", "), "；如需修改请删除并重新建立节点")
		}
	}
	if err := checkInboundListenerConflicts(inbound, inbound.Id); err != nil {
		return err
	}
	previous := *oldInbound
	enableChanged := previous.Enable != inbound.Enable
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
	if oldInbound.Publish {
		if _, err := validateManagedInbound(oldInbound); err != nil {
			return common.NewError("已发布入站不允许保存为非托管 VLESS/XHTTP 配置: ", err)
		}
		endpoint, err := (&EndpointService{}).activeEndpoint(oldInbound.Id)
		if err != nil {
			return common.NewError("已发布入站缺少 active 公网入口: ", err)
		}
		if _, err := (&LinkService{}).GenerateInboundLink(oldInbound, endpoint); err != nil {
			return common.NewError("已发布入站无法生成严格订阅链接: ", err)
		}
	}

	db := database.GetDB()
	if err := db.Save(oldInbound).Error; err != nil {
		return err
	}
	if liveEndpointCount == 0 || !enableChanged {
		return nil
	}
	if err := s.syncManagedRoutes(); err != nil {
		if restoreErr := db.Save(&previous).Error; restoreErr != nil {
			setManagedDataHealthy(false)
			return common.NewError("更新入站后的 Caddy 同步失败: ", err, "; 数据恢复失败: ", restoreErr)
		}
		return common.NewError("更新入站后的 Caddy 同步失败，数据库已恢复: ", err)
	}
	return nil
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
	endpointMutationLock.Lock()
	defer endpointMutationLock.Unlock()

	db := database.GetDB()
	now := time.Now().Unix() * 1000
	var invalid []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Find(&invalid).Error; err != nil {
		return 0, err
	}
	if len(invalid) == 0 {
		return 0, nil
	}
	ids := make([]int, 0, len(invalid))
	for _, inbound := range invalid {
		ids = append(ids, inbound.Id)
	}
	result := db.Model(&model.Inbound{}).Where("id IN ? AND enable = ?", ids, true).Update("enable", false)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, nil
	}

	var managedEndpointCount int64
	if err := db.Model(&model.PublicEndpoint{}).
		Where("inbound_id IN ? AND status IN ?", ids, []string{model.EndpointStatusPending, model.EndpointStatusActive, model.EndpointStatusDraining}).
		Count(&managedEndpointCount).Error; err != nil {
		if rollbackErr := db.Model(&model.Inbound{}).Where("id IN ?", ids).Update("enable", true).Error; rollbackErr != nil {
			setManagedDataHealthy(false)
			return 0, common.NewError("自动禁用入站后检查托管 Endpoint 失败: ", err, "; 数据恢复失败: ", rollbackErr)
		}
		return 0, err
	}
	if managedEndpointCount == 0 {
		return result.RowsAffected, nil
	}

	var syncErr error
	if s.syncManagedRoutesHook != nil {
		syncErr = s.syncManagedRoutesHook()
		setManagedCaddyHealthy(syncErr == nil)
	} else {
		_, syncErr = (&EndpointService{}).applyCurrentRoutes()
	}
	if syncErr == nil {
		return result.RowsAffected, nil
	}
	if rollbackErr := db.Model(&model.Inbound{}).Where("id IN ?", ids).Update("enable", true).Error; rollbackErr != nil {
		setManagedDataHealthy(false)
		return 0, common.NewError("自动禁用入站后的 Caddy 同步失败: ", syncErr, "; 数据恢复失败: ", rollbackErr)
	}
	return 0, common.NewError("自动禁用入站后的 Caddy 同步失败，数据库已恢复: ", syncErr)
}
