package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
	"x-ui/reverseexit"
)

const reverseSocksSettingKey = "reverseSocksConfig"

type reverseSocksSaved struct {
	reverseexit.Config
	reverseFRPCConfig
	Enabled bool `json:"enabled"`
}

// ReverseSocksForm is used only inside the authenticated x-ui dashboard.
// The single-user editor submits complete values; empty secrets clear them.
type ReverseSocksForm struct {
	Enabled        bool   `json:"enabled" form:"enabled"`
	VisitorAddr    string `json:"visitorAddr" form:"visitorAddr"`
	UDPBindAddr    string `json:"udpBindAddr" form:"udpBindAddr"`
	UDPPeerAddr    string `json:"udpPeerAddr" form:"udpPeerAddr"`
	WARPAddr       string `json:"warpAddr" form:"warpAddr"`
	WARPProbe      string `json:"warpProbe" form:"warpProbe"`
	WARPUsername   string `json:"warpUsername" form:"warpUsername"`
	WARPPassword   string `json:"warpPassword" form:"warpPassword"`
	Token          string `json:"token" form:"token"`
	FRPCBinaryPath string `json:"frpcBinaryPath" form:"frpcBinaryPath"`
	FRPCConfigPath string `json:"frpcConfigPath" form:"frpcConfigPath"`
	FRPSServerAddr string `json:"frpsServerAddr" form:"frpsServerAddr"`
	FRPSServerPort int    `json:"frpsServerPort" form:"frpsServerPort"`
	FRPSToken      string `json:"frpsToken" form:"frpsToken"`
	XTCPServerName string `json:"xtcpServerName" form:"xtcpServerName"`
	XTCPSecretKey  string `json:"xtcpSecretKey" form:"xtcpSecretKey"`
	STUNServer     string `json:"stunServer" form:"stunServer"`
}

type ReverseSocksView struct {
	Enabled        bool                      `json:"enabled"`
	VisitorAddr    string                    `json:"visitorAddr"`
	UDPBindAddr    string                    `json:"udpBindAddr"`
	UDPPeerAddr    string                    `json:"udpPeerAddr"`
	WARPAddr       string                    `json:"warpAddr"`
	WARPProbe      string                    `json:"warpProbe"`
	WARPUsername   string                    `json:"warpUsername"`
	WARPPassword   string                    `json:"warpPassword"`
	Token          string                    `json:"token"`
	FRPCBinaryPath string                    `json:"frpcBinaryPath"`
	FRPCConfigPath string                    `json:"frpcConfigPath"`
	FRPSServerAddr string                    `json:"frpsServerAddr"`
	FRPSServerPort int                       `json:"frpsServerPort"`
	FRPSToken      string                    `json:"frpsToken"`
	XTCPServerName string                    `json:"xtcpServerName"`
	XTCPSecretKey  string                    `json:"xtcpSecretKey"`
	STUNServer     string                    `json:"stunServer"`
	FRPC           frpcRuntime               `json:"frpc"`
	Status         reverseexit.RuntimeStatus `json:"status"`
}

type ReverseSocksService struct {
	mu           sync.Mutex
	settings     SettingService
	manager      *reverseexit.Manager
	frpc         *managedFRPC
	parent       context.Context
	startupError string
}

func NewReverseSocksService() *ReverseSocksService {
	s := &ReverseSocksService{manager: reverseexit.NewManager(), parent: context.Background()}
	s.frpc = newManagedFRPC(func() {
		// Serialized with Save/Restart; only unexpected exits may stop the
		// in-process Agent, never a manually launched unrelated frpc.
		go func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.frpc.Status().State == "error" {
				s.manager.Stop()
			}
		}()
	})
	return s
}

func (s *ReverseSocksService) load() (reverseSocksSaved, error) {
	result := reverseSocksSaved{Config: reverseexit.DefaultConfig(),
		reverseFRPCConfig: defaultReverseFRPCConfig()}
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
	if err == nil && config.Enabled {
		_, err = s.applyLocked(config, false)
	}
	if err != nil {
		s.startupError = err.Error()
	}
	return err
}

func (s *ReverseSocksService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manager.Stop()
	s.frpc.Stop()
}

func (s *ReverseSocksService) view(config reverseSocksSaved) ReverseSocksView {
	status := s.manager.Status()
	if s.startupError != "" {
		status = reverseexit.RuntimeStatus{Enabled: config.Enabled, State: "error", Error: s.startupError}
	}
	return ReverseSocksView{
		Enabled:        config.Enabled,
		VisitorAddr:    config.VisitorAddr,
		UDPBindAddr:    config.UDPBindAddr,
		UDPPeerAddr:    config.UDPPeerAddr,
		WARPAddr:       config.WARPAddr,
		WARPProbe:      config.WARPProbe,
		WARPUsername:   config.WARPUsername,
		WARPPassword:   config.WARPPassword,
		Token:          config.Token,
		FRPCBinaryPath: config.FRPCBinaryPath,
		FRPCConfigPath: config.FRPCConfigPath,
		FRPSServerAddr: config.FRPSServerAddr,
		FRPSServerPort: config.FRPSServerPort,
		FRPSToken:      config.FRPSToken,
		XTCPServerName: config.XTCPServerName,
		XTCPSecretKey:  config.XTCPSecretKey,
		STUNServer:     config.STUNServer,
		FRPC:           s.frpc.Status(),
		Status:         status,
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
	return s.applyLocked(config, true)
}

// applyLocked preflights the exact custom frpc *before* touching a running
// child. A failed verification leaves the prior process and DB untouched.
// Once accepted, both managed components restart together; an activation
// failure is terminal, never a legacy or direct-connect fallback.
func (s *ReverseSocksService) applyLocked(config reverseSocksSaved, persist bool) (ReverseSocksView, error) {
	config.RetryInterval = 2 * time.Second
	var staged string
	if config.Enabled {
		content, err := renderManagedFRPC(config.reverseFRPCConfig, config.Config)
		if err != nil {
			return ReverseSocksView{}, err
		}
		staged, err = stageFRPCConfig(s.parent, config.reverseFRPCConfig, content)
		if err != nil {
			return ReverseSocksView{}, err
		}
		defer os.Remove(staged)
	}
	if persist {
		bytes, err := json.Marshal(config)
		if err != nil {
			return ReverseSocksView{}, err
		}
		if err := s.settings.setString(reverseSocksSettingKey, string(bytes)); err != nil {
			return ReverseSocksView{}, err
		}
	}
	s.startupError = ""
	// Always stop the Agent before replacing its local Visitor.
	s.manager.Stop()
	s.frpc.Stop()
	if config.Enabled {
		if err := ensureVisitorPortAvailable(config.VisitorAddr); err != nil {
			s.startupError = err.Error()
			return ReverseSocksView{}, err
		}
		if err := activateFRPCConfig(config.reverseFRPCConfig, staged); err != nil {
			s.startupError = err.Error()
			return ReverseSocksView{}, err
		}
		if err := s.frpc.Start(config.FRPCBinaryPath, config.FRPCConfigPath); err != nil {
			s.startupError = err.Error()
			return ReverseSocksView{}, err
		}
		if err := s.manager.Apply(s.parent, config.Config, true); err != nil {
			s.frpc.Stop()
			s.startupError = err.Error()
			return ReverseSocksView{}, err
		}
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
	return s.persistAndApply(configFromReverseSocksForm(config, form))
}

// Save and Verify must read the same browser values. Previously, Verify
// ignored the current STUN field and checked an outdated SQLite value.
func configFromReverseSocksForm(config reverseSocksSaved, form ReverseSocksForm) reverseSocksSaved {
	config.Enabled = form.Enabled
	config.VisitorAddr = form.VisitorAddr
	config.UDPBindAddr = form.UDPBindAddr
	config.UDPPeerAddr = form.UDPPeerAddr
	config.WARPAddr = form.WARPAddr
	config.WARPProbe = form.WARPProbe
	config.WARPUsername = form.WARPUsername
	config.WARPPassword = form.WARPPassword
	config.Token = form.Token
	config.FRPCBinaryPath = form.FRPCBinaryPath
	config.FRPCConfigPath = form.FRPCConfigPath
	config.FRPSServerAddr = form.FRPSServerAddr
	config.FRPSServerPort = form.FRPSServerPort
	config.FRPSToken = form.FRPSToken
	config.XTCPServerName = form.XTCPServerName
	config.XTCPSecretKey = form.XTCPSecretKey
	config.STUNServer = form.STUNServer
	return config
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

// VerifyFRPC checks the current form without saving it, touching the active
// config file, or restarting any process.
func (s *ReverseSocksService) VerifyFRPC(form ReverseSocksForm) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.load()
	if err != nil {
		return err
	}
	config = configFromReverseSocksForm(config, form)
	content, err := renderManagedFRPC(config.reverseFRPCConfig, config.Config)
	if err != nil {
		return err
	}
	temp, err := stageFRPCConfig(s.parent, config.reverseFRPCConfig, content)
	if err != nil {
		return err
	}
	return os.Remove(temp)
}

func (s *ReverseSocksService) Restart() (ReverseSocksView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.load()
	if err != nil {
		return ReverseSocksView{}, err
	}
	if !config.Enabled {
		return ReverseSocksView{}, fmt.Errorf("enable reverse SOCKS5 before restart")
	}
	return s.applyLocked(config, false)
}
