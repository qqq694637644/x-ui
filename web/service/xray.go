package service

import (
	"encoding/json"
	"errors"
	"sync"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/xray"

	"go.uber.org/atomic"
)

var p *xray.Process
var lock sync.Mutex
var isNeedXrayRestart atomic.Bool
var result string
var lastXrayApplyError struct {
	sync.RWMutex
	message string
}

func xrayApplyFailure() string {
	lastXrayApplyError.RLock()
	defer lastXrayApplyError.RUnlock()
	return lastXrayApplyError.message
}

func recordXrayApplyResult(err error) {
	lastXrayApplyError.Lock()
	defer lastXrayApplyError.Unlock()
	if err != nil {
		lastXrayApplyError.message = err.Error()
		// The scheduler consumes the flag BEFORE calling RestartXray.
		// Re-queue every failed attempt, including a failed cold start,
		// so a saved A mapping is never mistaken for an applied mapping.
		isNeedXrayRestart.Store(true)
	} else {
		lastXrayApplyError.message = ""
	}
}

func currentXrayConfig() *xray.Config {
	lock.Lock()
	defer lock.Unlock()
	if p == nil || !p.IsRunning() {
		return nil
	}
	return p.GetConfig()
}

type XrayService struct {
	inboundService InboundService
	tunnelService  TunnelService
	mytrnService   MyTRNService
	settingService SettingService
}

func (s *XrayService) IsXrayRunning() bool {
	running := p != nil && p.IsRunning()
	if !running {
		setManagedXrayHealthy(false)
	}
	return running
}

func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}
	return p.GetErr()
}

func (s *XrayService) GetXrayResult() string {
	if result != "" {
		return result
	}
	if s.IsXrayRunning() {
		return ""
	}
	if p == nil {
		return ""
	}
	result = p.GetResult()
	return result
}

func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}
	err = s.tunnelService.ApplyToXrayConfig(xrayConfig)
	if err != nil {
		return nil, err
	}
	if err := s.mytrnService.ApplyToXrayConfig(xrayConfig); err != nil {
		return nil, err
	}
	return xrayConfig, nil
}

func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, error) {
	if !s.IsXrayRunning() {
		return nil, errors.New("xray is not running")
	}
	return p.GetTraffic(true)
}

func (s *XrayService) RestartXray(isForce bool) (err error) {
	lock.Lock()
	defer lock.Unlock()
	defer func() {
		setManagedXrayHealthy(err == nil && p != nil && p.IsRunning())
		recordXrayApplyResult(err)
	}()
	logger.Debug("restart xray, force:", isForce)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}
	if err := xray.ValidateConfig(xrayConfig); err != nil {
		return err
	}

	var previousConfig *xray.Config
	previousRunning := p != nil && p.IsRunning()
	if previousRunning {
		if !isForce && p.GetConfig().Equals(xrayConfig) {
			logger.Debug("not need to restart xray")
			return nil
		}
		previousConfig = p.GetConfig()
		if err := p.Stop(); err != nil {
			return common.NewError("停止旧 Xray 失败，未应用新配置: ", err)
		}
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	if err := p.Start(); err != nil {
		if previousRunning && previousConfig != nil {
			rollback := xray.NewProcess(previousConfig)
			if rollbackErr := rollback.Start(); rollbackErr != nil {
				p = rollback
				return common.NewError("启动新配置失败，且恢复旧配置失败: ", err, "; rollback: ", rollbackErr)
			}
			p = rollback
			return common.NewError("启动新配置失败，已恢复旧配置: ", err)
		}
		return err
	}
	return nil
}

func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	setManagedXrayHealthy(false)
	logger.Debug("stop xray")
	if s.IsXrayRunning() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

func (s *XrayService) SetToNeedRestart() {
	setManagedXrayHealthy(false)
	isNeedXrayRestart.Store(true)
}

func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CAS(true, false)
}
