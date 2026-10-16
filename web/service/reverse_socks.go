package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"x-ui/reverseexit"
)

const reverseSocksSettingKey = "reverseSocksConfig"

type reverseSocksSaved struct {
	reverseexit.Config
	Enabled bool `json:"enabled"`
}

// ReverseSocksForm is used only inside the authenticated x-ui dashboard.
// Empty secret fields mean "keep the current value".
type ReverseSocksForm struct {
	Enabled      bool   `json:"enabled" form:"enabled"`
	VisitorAddr  string `json:"visitorAddr" form:"visitorAddr"`
	UDPBindAddr  string `json:"udpBindAddr" form:"udpBindAddr"`
	UDPPeerAddr  string `json:"udpPeerAddr" form:"udpPeerAddr"`
	WARPAddr     string `json:"warpAddr" form:"warpAddr"`
	WARPUsername string `json:"warpUsername" form:"warpUsername"`
	WARPPassword string `json:"warpPassword" form:"warpPassword"`
	Token        string `json:"token" form:"token"`
}

type ReverseSocksView struct {
	Enabled         bool                      `json:"enabled"`
	VisitorAddr     string                    `json:"visitorAddr"`
	UDPBindAddr     string                    `json:"udpBindAddr"`
	UDPPeerAddr     string                    `json:"udpPeerAddr"`
	WARPAddr        string                    `json:"warpAddr"`
	WARPUsername    string                    `json:"warpUsername"`
	HasWARPPassword bool                      `json:"hasWarpPassword"`
	HasToken        bool                      `json:"hasToken"`
	Status          reverseexit.RuntimeStatus `json:"status"`
}

type ReverseSocksService struct {
	mu           sync.Mutex
	settings     SettingService
	manager      *reverseexit.Manager
	parent       context.Context
	startupError string
}

func NewReverseSocksService() *ReverseSocksService {
	return &ReverseSocksService{manager: reverseexit.NewManager(), parent: context.Background()}
}

func (s *ReverseSocksService) load() (reverseSocksSaved, error) {
	result := reverseSocksSaved{Config: reverseexit.DefaultConfig()}
	raw, err := s.settings.getString(reverseSocksSettingKey)
	if err != nil {
		return result, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return result, fmt.Errorf("invalid stored reverse SOCKS configuration: %w", err)
		}
	}
	result.RetryInterval = 2 * time.Second
	return result, nil
}

// Start restores desired runtime state when the panel starts. Invalid saved
// configuration is reported to the Web UI; no insecure fallback occurs.
func (s *ReverseSocksService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parent = ctx
	config, err := s.load()
	if err == nil {
		err = s.manager.Apply(ctx, config.Config, config.Enabled)
	}
	if err != nil {
		s.startupError = err.Error()
	}
	return err
}

func (s *ReverseSocksService) Stop() {
	s.manager.Stop()
}

func (s *ReverseSocksService) view(config reverseSocksSaved) ReverseSocksView {
	status := s.manager.Status()
	if s.startupError != "" {
		status = reverseexit.RuntimeStatus{Enabled: config.Enabled, State: "error", Error: s.startupError}
	}
	return ReverseSocksView{
		Enabled:         config.Enabled,
		VisitorAddr:     config.VisitorAddr,
		UDPBindAddr:     config.UDPBindAddr,
		UDPPeerAddr:     config.UDPPeerAddr,
		WARPAddr:        config.WARPAddr,
		WARPUsername:    config.WARPUsername,
		HasWARPPassword: config.WARPPassword != "",
		HasToken:        config.Token != "",
		Status:          status,
	}
}

func (s *ReverseSocksService) Get() (ReverseSocksView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.load()
	if err != nil {
		return ReverseSocksView{}, err
	}
	return s.view(config), nil
}

func (s *ReverseSocksService) persistAndApply(config reverseSocksSaved) (ReverseSocksView, error) {
	config.RetryInterval = 2 * time.Second
	if config.Enabled {
		if err := config.Config.Validate(); err != nil {
			return ReverseSocksView{}, err
		}
	}
	bytes, err := json.Marshal(config)
	if err != nil {
		return ReverseSocksView{}, err
	}
	if err := s.settings.setString(reverseSocksSettingKey, string(bytes)); err != nil {
		return ReverseSocksView{}, err
	}
	s.startupError = ""
	if err := s.manager.Apply(s.parent, config.Config, config.Enabled); err != nil {
		return ReverseSocksView{}, err
	}
	return s.view(config), nil
}

func (s *ReverseSocksService) Save(form ReverseSocksForm) (ReverseSocksView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.load()
	if err != nil {
		return ReverseSocksView{}, err
	}
	config.Enabled = form.Enabled
	config.VisitorAddr = form.VisitorAddr
	config.UDPBindAddr = form.UDPBindAddr
	config.UDPPeerAddr = form.UDPPeerAddr
	config.WARPAddr = form.WARPAddr
	config.WARPUsername = form.WARPUsername
	if form.WARPUsername == "" {
		config.WARPPassword = ""
	} else if form.WARPPassword != "" {
		config.WARPPassword = form.WARPPassword
	}
	if form.Token != "" {
		config.Token = form.Token
	}
	return s.persistAndApply(config)
}

func (s *ReverseSocksService) SetEnabled(enabled bool) (ReverseSocksView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.load()
	if err != nil {
		return ReverseSocksView{}, err
	}
	config.Enabled = enabled
	return s.persistAndApply(config)
}
