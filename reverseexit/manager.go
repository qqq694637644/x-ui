package reverseexit

import (
	"context"
	"errors"
	"sync"
)

// RuntimeStatus does not expose authentication material.
type RuntimeStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Error   string `json:"error"`
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
	return &Manager{status: RuntimeStatus{State: "stopped"}}
}

func (m *Manager) Status() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) setStatus(state, message string) {
	m.mu.Lock()
	m.status.State = state
	m.status.Error = message
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
	m.status = RuntimeStatus{Enabled: enabled, State: "stopped"}
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
