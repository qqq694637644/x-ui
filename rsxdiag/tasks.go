package rsxdiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type TaskSpec struct {
	Kind      string `json:"kind"`
	Bytes     int64  `json:"bytes"`
	Direction string `json:"direction"`
	Seconds   int    `json:"seconds"`
	Target    string `json:"target"`
	URL       string `json:"url"`
	UseWARP   bool   `json:"use_warp"`
}
type Step struct {
	Name       string      `json:"name"`
	State      string      `json:"state"`
	Time       time.Time   `json:"time"`
	DurationMS float64     `json:"duration_ms"`
	Evidence   interface{} `json:"evidence,omitempty"`
	Error      string      `json:"error,omitempty"`
}
type Task struct {
	ID       string      `json:"id"`
	Spec     TaskSpec    `json:"spec"`
	State    string      `json:"state"`
	Started  time.Time   `json:"started"`
	Finished *time.Time  `json:"finished,omitempty"`
	Steps    []Step      `json:"steps"`
	Result   interface{} `json:"result,omitempty"`
	Error    string      `json:"error,omitempty"`
}
type Runner struct {
	mu     sync.Mutex
	tasks  map[string]*Task
	order  []string
	active string
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

func NewRunner() *Runner { return &Runner{tasks: map[string]*Task{}} }
func (s TaskSpec) Validate() error {
	switch s.Kind {
	case "self_test", "route_check", "egress_check", "report":
	case "raw_benchmark", "socks_benchmark", "target_benchmark":
		if s.Bytes != 1_000_000 && s.Bytes != 10_000_000 && s.Bytes != 50_000_000 {
			return errors.New("benchmark size must be 1, 10, or 50 MB")
		}
		if s.Kind != "target_benchmark" && s.Direction != "upload" && s.Direction != "download" {
			return errors.New("invalid benchmark direction")
		}
	case "capture":
		if s.Seconds != 30 && s.Seconds != 60 && s.Seconds != 300 {
			return errors.New("capture duration must be 30, 60, or 300 seconds")
		}
	default:
		return errors.New("unknown diagnostic task")
	}
	if len(s.Target) > 512 || len(s.URL) > 2048 {
		return errors.New("diagnostic target too long")
	}
	return nil
}
func (r *Runner) Start(parent context.Context, s TaskSpec, run func(context.Context, string, func(Step)) (interface{}, error)) (*Task, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("diagnostics stopped")
	}
	if r.active != "" {
		return nil, errors.New("another diagnostic task is active")
	}
	limit := 130 * time.Second
	if s.Kind == "capture" {
		limit = time.Duration(s.Seconds)*time.Second + 5*time.Minute
	}
	if s.Kind == "report" {
		limit = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	id := ID()
	t := &Task{ID: id, Spec: s, State: "running", Started: time.Now().UTC(), Steps: []Step{}}
	r.tasks[id] = t
	r.order = append(r.order, id)
	if len(r.order) > 32 {
		delete(r.tasks, r.order[0])
		r.order = r.order[1:]
	}
	r.active = id
	r.cancel = cancel
	r.wg.Add(1)
	progress := func(step Step) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(t.Steps) < 64 {
			step.Time = time.Now().UTC()
			t.Steps = append(t.Steps, step)
		}
	}
	go func() {
		defer r.wg.Done()
		defer cancel()
		var result interface{}
		var err error
		func() {
			defer func() {
				if v := recover(); v != nil {
					err = fmt.Errorf("diagnostic task panic: %v", v)
				}
			}()
			result, err = run(ctx, id, progress)
		}()
		r.mu.Lock()
		defer r.mu.Unlock()
		now := time.Now().UTC()
		t.Finished = &now
		t.Result = result
		t.State = "completed"
		if err != nil {
			t.Error = err.Error()
			t.State = "failed"
		}
		if ctx.Err() != nil {
			t.State = "cancelled"
			t.Error = ctx.Err().Error()
		}
		r.active = ""
		r.cancel = nil
	}()
	copy := *t
	return &copy, nil
}
func (r *Runner) Cancel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != id || r.cancel == nil {
		return errors.New("no matching running task")
	}
	r.tasks[id].State = "cancelling"
	r.cancel()
	return nil
}
func (r *Runner) Snapshot() []Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Task{}
	for i := len(r.order) - 1; i >= 0; i-- {
		t := r.tasks[r.order[i]]
		if t == nil {
			continue
		}
		copy := *t
		copy.Steps = append([]Step(nil), t.Steps...)
		out = append(out, copy)
	}
	return out
}
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
func (r *Runner) Active() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.active != "" }
func CloneJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"state":"unavailable","error":"diagnostic value could not be encoded"}`)
	}
	return b
}
