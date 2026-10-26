package service

// Independent RSX3-over-IPv6/DDNS management plane.
// Deliberately does not construct, stop or modify the old managedFRPC.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"x-ui/reverseexit"
)

const directSocksKey = "reverseSocksIPv6Config"

type DirectSocksSettings struct {
	Enabled             bool   `json:"enabled" form:"enabled"`
	EgressMode          string `json:"egressMode" form:"egressMode"`
	Hostname            string `json:"hostname" form:"hostname"`
	Port                int    `json:"port" form:"port"`
	TargetMbps          int    `json:"targetMbps" form:"targetMbps"`
	MaxMbps             int    `json:"maxMbps" form:"maxMbps"`
	CertSHA256          string `json:"certSHA256" form:"certSHA256"`
	Token               string `json:"token" form:"token"`
	TargetSOCKSAddr     string `json:"targetSocksAddr" form:"targetSocksAddr"`
	TargetSOCKSUsername string `json:"targetSocksUsername" form:"targetSocksUsername"`
	TargetSOCKSPassword string `json:"targetSocksPassword" form:"targetSocksPassword"`
}

type DirectSocksStatus struct {
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
	ConnectedIP string `json:"connectedIP,omitempty"`
}

type DirectSocksView struct {
	DirectSocksSettings
	HasToken          bool              `json:"hasToken"`
	HasTargetPassword bool              `json:"hasTargetPassword"`
	Status            DirectSocksStatus `json:"status"`
}

type DirectSocksService struct {
	mu       sync.Mutex
	settings SettingService
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	status   DirectSocksStatus
}

func NewDirectSocksService() *DirectSocksService {
	return &DirectSocksService{status: DirectSocksStatus{State: "stopped"}}
}

func (s *DirectSocksService) load() (DirectSocksSettings, error) {
	cfg := DirectSocksSettings{Port: 8080, EgressMode: "native", TargetMbps: 16, MaxMbps: 24}
	raw, err := s.settings.getString(directSocksKey)
	if err != nil {
		return cfg, err
	}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}
func (c DirectSocksSettings) transport() reverseexit.DirectConfig {
	native := c.EgressMode == "native"
	targetAddr, targetUser, targetPass := c.TargetSOCKSAddr, c.TargetSOCKSUsername, c.TargetSOCKSPassword
	if native {
		targetAddr, targetUser, targetPass = "", "", ""
	}
	return reverseexit.DirectConfig{
		Hostname: strings.TrimSpace(c.Hostname), Port: c.Port,
		TargetMbps: c.TargetMbps, MaxMbps: c.MaxMbps,
		CertSHA256: strings.ToLower(strings.TrimSpace(c.CertSHA256)),
		Agent: reverseexit.Config{
			Protocol: "rsx3", DirectQUIC: true,
			DirectTarget:        native,
			TargetSOCKSAddr:     targetAddr,
			TargetSOCKSUsername: targetUser,
			TargetSOCKSPassword: targetPass,
			Token:               c.Token, RetryInterval: 3 * time.Second,
		},
	}
}
func (s *DirectSocksService) viewLocked(cfg DirectSocksSettings) DirectSocksView {
	view := DirectSocksView{DirectSocksSettings: cfg, Status: s.status,
		HasToken: cfg.Token != "", HasTargetPassword: cfg.TargetSOCKSPassword != ""}
	view.Token = ""
	view.TargetSOCKSPassword = ""
	return view
}
func (s *DirectSocksService) Get() (DirectSocksView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.load()
	if err != nil {
		return DirectSocksView{}, err
	}
	return s.viewLocked(cfg), nil
}
func (s *DirectSocksService) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	cfg, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	if err = cfg.transport().Validate(); err != nil {
		s.mu.Lock()
		s.status = DirectSocksStatus{State: "error", Message: err.Error()}
		s.mu.Unlock()
		return err
	}
	s.startWorker(cfg)
	return nil
}
func (s *DirectSocksService) startWorker(cfg DirectSocksSettings) {
	s.mu.Lock()
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.status = DirectSocksStatus{State: "connecting"}
	s.mu.Unlock()
	go func() {
		defer close(done)
		err := reverseexit.RunDirect(ctx, cfg.transport(), func(state, msg string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.done != done {
				return
			}
			if state == "connected" {
				s.status.ConnectedIP = msg
				msg = ""
			}
			s.status.State = state
			s.status.Message = msg
		})
		s.mu.Lock()
		if s.done == done && ctx.Err() == nil && err != nil {
			s.status = DirectSocksStatus{State: "error", Message: err.Error()}
		}
		s.mu.Unlock()
	}()
}
func (s *DirectSocksService) stopWorker() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.status = DirectSocksStatus{State: "stopped"}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
func (s *DirectSocksService) Stop() { s.stopWorker() }

func (s *DirectSocksService) Save(in DirectSocksSettings) (DirectSocksView, error) {
	s.mu.Lock()
	if in.EgressMode != "native" && in.EgressMode != "socks5" {
		s.mu.Unlock()
		return DirectSocksView{}, fmt.Errorf("egressMode must be native or socks5")
	}
	old, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return DirectSocksView{}, err
	}
	// Blank secrets mean unchanged. Explicit rotation requires a new value.
	if in.Token == "" {
		in.Token = old.Token
	}
	if in.EgressMode == "native" {
		in.TargetSOCKSAddr = ""
		in.TargetSOCKSUsername = ""
		in.TargetSOCKSPassword = ""
	} else if in.TargetSOCKSPassword == "" {
		in.TargetSOCKSPassword = old.TargetSOCKSPassword
	}
	// Validate before stopping the active runtime or persisting invalid values.
	if in.Enabled {
		if err = in.transport().Validate(); err != nil {
			s.mu.Unlock()
			return DirectSocksView{}, err
		}
	}
	payload, err := json.Marshal(in)
	if err == nil {
		err = s.settings.setString(directSocksKey, string(payload))
	}
	s.mu.Unlock()
	if err != nil {
		return DirectSocksView{}, err
	}
	s.stopWorker()
	if in.Enabled {
		s.startWorker(in)
	}
	return s.Get()
}
func (s *DirectSocksService) SetEnabled(enabled bool) (DirectSocksView, error) {
	s.mu.Lock()
	cfg, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return DirectSocksView{}, err
	}
	cfg.Enabled = enabled
	return s.Save(cfg)
}
func (s *DirectSocksService) Restart() (DirectSocksView, error) {
	s.mu.Lock()
	cfg, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return DirectSocksView{}, err
	}
	if !cfg.Enabled {
		return DirectSocksView{}, fmt.Errorf("enable direct QUIC before restart")
	}
	return s.Save(cfg)
}
