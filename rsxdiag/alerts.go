package rsxdiag

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

type AlertRule struct {
	ID             string  `json:"id"`
	Metric         string  `json:"metric"`
	Direction      string  `json:"direction"`
	Threshold      float64 `json:"threshold"`
	Recover        float64 `json:"recover"`
	HoldSeconds    int     `json:"hold_seconds"`
	RecoverSeconds int     `json:"recover_seconds"`
	Enabled        bool    `json:"enabled"`
}
type Alert struct {
	ID      string    `json:"id"`
	Rule    string    `json:"rule"`
	State   string    `json:"state"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
	// LastObserved remains the last *valid* measurement when collection
	// disappears. Updated is the time of the most recent state transition.
	LastObserved time.Time  `json:"last_observed"`
	Resolved     *time.Time `json:"resolved,omitempty"`
	Value        float64    `json:"value"`
	Detail       string     `json:"detail"`
}
type alertState struct {
	Since, GoodSince time.Time
	Active           *Alert
}
type AlertEngine struct {
	mu     sync.Mutex
	rules  []AlertRule
	states map[string]*alertState
	events []Alert
}

func DefaultRules() []AlertRule {
	return []AlertRule{
		{ID: "xtcp_down", Metric: "xtcp_online", Direction: "below", Threshold: .5, Recover: .9, HoldSeconds: 30, RecoverSeconds: 5, Enabled: true},
		{ID: "warp_failure", Metric: "warp_failures", Direction: "above", Threshold: 2, Recover: 0, HoldSeconds: 0, RecoverSeconds: 5, Enabled: true},
		{ID: "high_rtt", Metric: "quic_rtt_ms", Direction: "above", Threshold: 1000, Recover: 800, HoldSeconds: 60, RecoverSeconds: 30, Enabled: true},
		{ID: "high_loss", Metric: "quic_loss_pct", Direction: "above", Threshold: 20, Recover: 10, HoldSeconds: 60, RecoverSeconds: 30, Enabled: true},
		{ID: "tcp_failures", Metric: "tcp_failure_pct", Direction: "above", Threshold: 20, Recover: 5, HoldSeconds: 60, RecoverSeconds: 30, Enabled: true},
		{ID: "frpc_exit", Metric: "frpc_error", Direction: "above", Threshold: .5, Recover: 0, HoldSeconds: 0, RecoverSeconds: 5, Enabled: true},
		{ID: "frequent_reconnect", Metric: "reconnects_5m", Direction: "above", Threshold: 5, Recover: 2, HoldSeconds: 0, RecoverSeconds: 30, Enabled: true},
		{ID: "node_stale", Metric: "node_age_seconds", Direction: "above", Threshold: 15, Recover: 6, HoldSeconds: 10, RecoverSeconds: 5, Enabled: true},
		{ID: "target_failure", Metric: "target_failures", Direction: "above", Threshold: 2, Recover: 0, HoldSeconds: 0, RecoverSeconds: 5, Enabled: true},
	}
}
func NewAlerts(rules []AlertRule) (*AlertEngine, error) {
	e := &AlertEngine{states: map[string]*alertState{}}
	if err := e.SetRules(rules); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *AlertEngine) SetRules(rules []AlertRule) error {
	if len(rules) > 16 {
		return errors.New("too many alert rules")
	}
	allowed := map[string]bool{}
	for _, r := range DefaultRules() {
		allowed[r.Metric] = true
	}
	seen := map[string]bool{}
	enabled := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || len(r.ID) > 64 || seen[r.ID] || !allowed[r.Metric] {
			return errors.New("invalid alert rule identifier or metric")
		}
		seen[r.ID] = true
		enabled[r.ID] = r.Enabled
		if r.Direction != "above" && r.Direction != "below" {
			return errors.New("invalid alert comparison")
		}
		if r.HoldSeconds < 0 || r.HoldSeconds > 3600 || r.RecoverSeconds < 0 || r.RecoverSeconds > 3600 || math.IsNaN(r.Threshold) || math.IsNaN(r.Recover) || math.IsInf(r.Threshold, 0) || math.IsInf(r.Recover, 0) {
			return errors.New("invalid alert threshold")
		}
		if (r.Direction == "above" && r.Recover > r.Threshold) || (r.Direction == "below" && r.Recover < r.Threshold) {
			return errors.New("recovery threshold must provide hysteresis")
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append([]AlertRule(nil), rules...)
	for id, state := range e.states {
		if !seen[id] || !enabled[id] {
			if state.Active != nil {
				a := *state.Active
				a.State = "disabled"
				now := time.Now().UTC()
				a.Updated, a.Resolved = now, &now
				a.Detail = "Rule disabled or removed by user; not an observed recovery."
				e.events = append(e.events, a)
			}
			delete(e.states, id)
		}
	}
	if len(e.events) > 512 {
		e.events = e.events[len(e.events)-512:]
	}
	return nil
}
func (e *AlertEngine) Rules() []AlertRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]AlertRule(nil), e.rules...)
}

// markUnknown never fabricates a recovery. It retains the incident ID,
// last observed value and timestamp, and generates only one transition.
func markUnknown(s *alertState, metric string, at time.Time) (Alert, bool) {
	if s == nil {
		return Alert{}, false
	}
	s.Since, s.GoodSince = time.Time{}, time.Time{}
	if s.Active == nil || s.Active.State == "unknown" {
		return Alert{}, false
	}
	if s.Active.LastObserved.IsZero() {
		s.Active.LastObserved = s.Active.Updated // reports created before this field
	}
	s.Active.State = "unknown"
	s.Active.Updated = at
	s.Active.Detail = fmt.Sprintf("%s sample unavailable; last observed %s = %.3f at %s; recovery unverified",
		metric, metric, s.Active.Value, s.Active.LastObserved.UTC().Format(time.RFC3339))
	return *s.Active, true
}

// MarkUnknown is also called when collection is explicitly paused. A lack
// of samples is never reported as an observed service recovery.
func (e *AlertEngine) MarkUnknown(at time.Time) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := []Alert{}
	for _, r := range e.rules {
		if a, ok := markUnknown(e.states[r.ID], r.Metric, at); ok {
			changed = append(changed, a)
		}
	}
	e.record(changed)
	return changed
}
func (e *AlertEngine) record(changed []Alert) {
	e.events = append(e.events, changed...)
	if len(e.events) > 512 {
		e.events = e.events[len(e.events)-512:]
	}
}
func (e *AlertEngine) Evaluate(p Point) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := []Alert{}
	for _, r := range e.rules {
		state := e.states[r.ID]
		if state == nil {
			state = &alertState{}
			e.states[r.ID] = state
		}
		v := p.Values[r.Metric]
		if !r.Enabled || v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
			if a, changedState := markUnknown(state, r.Metric, p.Time); changedState {
				changed = append(changed, a)
			}
			continue
		}
		bad, good := *v > r.Threshold, *v <= r.Recover
		if r.Direction == "below" {
			bad = *v < r.Threshold
			good = *v >= r.Recover
		}
		if state.Active == nil {
			if !bad {
				state.Since = time.Time{}
				continue
			}
			if state.Since.IsZero() {
				state.Since = p.Time
			}
			if p.Time.Sub(state.Since) < time.Duration(r.HoldSeconds)*time.Second {
				continue
			}
			a := Alert{ID: ID(), Rule: r.ID, State: "active", Started: state.Since, Updated: p.Time, LastObserved: p.Time, Value: *v, Detail: fmt.Sprintf("%s %s %.3f (observed %.3f)", r.Metric, r.Direction, r.Threshold, *v)}
			state.Active = &a
			changed = append(changed, a)
		} else {
			previouslyUnknown := state.Active.State == "unknown"
			state.Active.Updated = p.Time
			state.Active.LastObserved = p.Time
			state.Active.Value = *v
			if previouslyUnknown && good {
				state.Active.Detail = fmt.Sprintf("%s valid recovery sample %.3f at %s; awaiting sustained recovery",
					r.Metric, *v, p.Time.UTC().Format(time.RFC3339))
			}
			if previouslyUnknown && !good {
				state.Active.State = "active"
				state.Active.Detail = fmt.Sprintf("%s %s %.3f (observed %.3f)", r.Metric, r.Direction, r.Threshold, *v)
				changed = append(changed, *state.Active)
			}
			if !good {
				state.GoodSince = time.Time{}
				continue
			}
			if state.GoodSince.IsZero() {
				state.GoodSince = p.Time
			}
			if p.Time.Sub(state.GoodSince) >= time.Duration(r.RecoverSeconds)*time.Second {
				a := *state.Active
				a.State = "resolved"
				now := p.Time
				a.Resolved = &now
				a.Updated = now
				changed = append(changed, a)
				state.Active = nil
				state.Since = time.Time{}
				state.GoodSince = time.Time{}
			}
		}
	}
	e.record(changed)
	return changed
}
func (e *AlertEngine) Snapshot() map[string]interface{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := []Alert{}
	unknown := []Alert{}
	for _, s := range e.states {
		if s.Active != nil {
			if s.Active.State == "active" {
				active = append(active, *s.Active)
			} else if s.Active.State == "unknown" {
				unknown = append(unknown, *s.Active)
			}
		}
	}
	return map[string]interface{}{"active": active, "unknown": unknown, "events": append([]Alert(nil), e.events...), "rules": append([]AlertRule(nil), e.rules...)}
}
func (e *AlertEngine) Restore(events []Alert) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(events) > 512 {
		events = events[len(events)-512:]
	}
	e.events = append([]Alert(nil), events...)
	for _, a := range events {
		s := e.states[a.Rule]
		if s == nil {
			s = &alertState{}
			e.states[a.Rule] = s
		}
		if a.State == "active" || a.State == "unknown" {
			value := a
			if value.LastObserved.IsZero() {
				value.LastObserved = value.Updated
			}
			s.Active = &value
			s.Since = a.Started
		} else {
			s.Active = nil
		}
	}
	// After a panel restart, previously live observations no longer attest
	// to current health. Preserve the incident as unknown until new samples.
	for _, r := range e.rules {
		if a, ok := markUnknown(e.states[r.ID], r.Metric, time.Now().UTC()); ok {
			e.record([]Alert{a})
		}
	}
}
