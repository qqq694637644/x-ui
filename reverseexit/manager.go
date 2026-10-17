package reverseexit

import (
	"context"
	"errors"
	"sync"
)

// RuntimeStatus does not expose authentication material.
type RuntimeStatus struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	Error     string `json:"error"`
	WARPState string `json:"warpState"`
	WARPError string `json:"warpError"`
}

// Manager owns precisely one in-process exit Agent. Save/Start/Stop are
// serialized: an old worker is completely stopped before a replacement runs.
type Manager struct {
	applyMu sync.Mutex
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	status  RuntimeStatus
}

func NewManager() *Manager {
	return &Manager{status: RuntimeStatus{State: "stopped", WARPState: "unknown"}}
}

func (m *Manager) Status() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) setStatus(state, message string) {
	m.mu.Lock()
	if state == "warp-ready" || state == "warp-error" || state == "warp-checking" {
		switch state {
		case "warp-ready":
			m.status.WARPState = "ready"
		case "warp-error":
			m.status.WARPState = "unavailable"
		default:
			m.status.WARPState = "checking"
		}
		m.status.WARPError = message
		m.mu.Unlock()
		return
	}
	m.status.State = state
	m.status.Error = message
	if state != "online" {
		m.status.WARPState = "unknown"
		m.status.WARPError = ""
	}
	m.mu.Unlock()
}

func (m *Manager) Apply(parent context.Context, config Config, enabled bool) error {
	if enabled {
		if err := config.Validate(); err != nil {
			return err
		}
	}
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	oldCancel, oldDone := m.cancel, m.done
	m.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
		<-oldDone
	}
	m.mu.Lock()
	m.cancel = nil
	m.done = nil
	m.status = RuntimeStatus{Enabled: enabled, State: "stopped", WARPState: "unknown"}
	if !enabled {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	m.cancel, m.done = cancel, done
	m.status.State = "connecting"
	m.mu.Unlock()
	go func() {
		defer close(done)
		err := Run(ctx, config, m.setStatus)
		if ctx.Err() == nil && err != nil && !errors.Is(err, context.Canceled) {
			m.setStatus("error", err.Error())
		} else {
			m.setStatus("stopped", "")
		}
	}()
	return nil
}

func (m *Manager) Stop() {
	_ = m.Apply(context.Background(), Config{}, false)
}
