// Package rsxdiag contains bounded telemetry and diagnostics independent of the
// forwarding implementation. Counter updates never perform network or disk I/O.
package rsxdiag

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const Schema = "rsx-diagnostics/1"
const MaxSessions = 1024
const MaxRecent = 256
const MaxEvents = 256
const MaxFrame = 65535

func ID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func LocalToken(secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write([]byte("rsx3-diagnostics-local-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

type Event struct {
	Time      time.Time `json:"time"`
	Component string    `json:"component"`
	Code      string    `json:"code"`
	Detail    string    `json:"detail"`
	Session   string    `json:"session,omitempty"`
}
type Session struct {
	Key           string      `json:"key"`
	Epoch         string      `json:"epoch"`
	SID           uint32      `json:"sid"`
	Target        string      `json:"target"`
	Opened        time.Time   `json:"opened"`
	Closed        *time.Time  `json:"closed,omitempty"`
	State         string      `json:"state"`
	CloseCode     string      `json:"close_code,omitempty"`
	Operation     string      `json:"operation,omitempty"`
	Error         string      `json:"error,omitempty"`
	ConfirmMS     float64     `json:"confirm_ms"`
	TargetMS      float64     `json:"target_ms"`
	VisitorMS     float64     `json:"visitor_ms"`
	AuthMS        float64     `json:"auth_ms"`
	UploadBytes   uint64      `json:"upload_bytes"`
	DownloadBytes uint64      `json:"download_bytes"`
	RawReadBytes  uint64      `json:"raw_read_bytes"`
	ReadWaitMS    float64     `json:"read_wait_ms"`
	WriteWaitMS   float64     `json:"write_wait_ms"`
	MaxWriteMS    float64     `json:"max_write_ms"`
	Local         string      `json:"local,omitempty"`
	Remote        string      `json:"remote,omitempty"`
	TargetLocal   string      `json:"target_local,omitempty"`
	TargetRemote  string      `json:"target_remote,omitempty"`
	TupleAt       time.Time   `json:"tuple_at"`
	Link          interface{} `json:"link,omitempty"`
}
type Metrics struct {
	Attempts         uint64                 `json:"attempts"`
	Success          uint64                 `json:"success"`
	Failures         uint64                 `json:"failures"`
	Closed           uint64                 `json:"closed"`
	Active           int                    `json:"active"`
	UDPActive        int64                  `json:"udp_active"`
	UDPSent          uint64                 `json:"udp_sent"`
	UDPReceived      uint64                 `json:"udp_received"`
	UDPDrops         map[string]uint64      `json:"udp_drops"`
	UploadBytes      uint64                 `json:"upload_bytes"`
	DownloadBytes    uint64                 `json:"download_bytes"`
	SlowWrite        uint64                 `json:"slow_write"`
	Errors           map[string]uint64      `json:"errors"`
	Latency          map[string]interface{} `json:"latency"`
	ConfirmHistogram []uint64               `json:"confirm_histogram"`
}
type Snapshot struct {
	Build      map[string]string `json:"build"`
	Schema     string            `json:"schema"`
	ProcessID  string            `json:"process_id"`
	Component  string            `json:"component"`
	Time       time.Time         `json:"time"`
	Started    time.Time         `json:"started"`
	Metrics    Metrics           `json:"metrics"`
	Sessions   []Session         `json:"sessions"`
	Events     []Event           `json:"events"`
	Truncated  bool              `json:"truncated"`
	HeapBytes  uint64            `json:"heap_bytes"`
	Goroutines int               `json:"goroutines"`
}
type latency struct {
	at time.Time
	ms float64
}
type Meter struct {
	mu                                           sync.Mutex
	id, component                                string
	started                                      time.Time
	active                                       map[string]*Span
	recent                                       []Session
	events                                       []Event
	errors, udpDrops                             map[string]uint64
	latency                                      []latency
	confirmHistogram                             []uint64
	attempts, success, failures, closed          uint64
	upload, download, slow, udpSent, udpReceived uint64
	udpActive                                    int64
}
type Span struct {
	meter                                                      *Meter
	mu                                                         sync.Mutex
	session                                                    Session
	done                                                       bool
	confirmed                                                  bool
	started                                                    time.Time
	upload, download, rawRead, readNanos, writeNanos, maxWrite uint64
}

func NewMeter(component string) *Meter {
	return &Meter{id: ID(), component: component, started: time.Now().UTC(), active: make(map[string]*Span), errors: make(map[string]uint64), udpDrops: make(map[string]uint64)}
}
func (m *Meter) Open(epoch string, sid uint32, target string) *Span {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	key := epoch + "/" + fmtUint(sid)
	s := &Span{meter: m, started: time.Now(), session: Session{Key: key, Epoch: epoch, SID: sid, Target: target, Opened: time.Now().UTC(), State: "opening"}}
	if len(m.active) >= MaxSessions {
		m.failures++
		m.errors["telemetry_capacity"]++
		return nil
	}
	m.active[key] = s
	return s
}
func fmtUint(n uint32) string {
	const digits = "0123456789"
	var buf [10]byte
	i := len(buf)
	for {
		i--
		buf[i] = digits[n%10]
		n /= 10
		if n == 0 {
			return string(buf[i:])
		}
	}
}
func (m *Meter) Event(code, detail, key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) >= MaxEvents {
		copy(m.events, m.events[1:])
		m.events = m.events[:len(m.events)-1]
	}
	m.events = append(m.events, Event{time.Now().UTC(), m.component, code, detail, key})
}
func (s *Span) Ready(target, visitor, auth time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.confirmed || s.done {
		s.mu.Unlock()
		return
	}
	s.confirmed = true
	s.session.State = "active"
	s.session.ConfirmMS = float64(time.Since(s.started)) / float64(time.Millisecond)
	s.session.TargetMS = float64(target) / float64(time.Millisecond)
	s.session.VisitorMS = float64(visitor) / float64(time.Millisecond)
	s.session.AuthMS = float64(auth) / float64(time.Millisecond)
	ms := s.session.ConfirmMS
	s.mu.Unlock()
	m := s.meter
	m.mu.Lock()
	m.success++
	if m.confirmHistogram == nil {
		m.confirmHistogram = NewHistogram()
	}
	ObserveHistogram(m.confirmHistogram, ms)
	m.latency = append(m.latency, latency{time.Now(), ms})
	if len(m.latency) > 2048 {
		m.latency = m.latency[len(m.latency)-2048:]
	}
	m.mu.Unlock()
}
func (s *Span) Endpoints(visitor, target net.Conn) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.TupleAt = time.Now().UTC()
	if visitor != nil {
		s.session.Local = visitor.LocalAddr().String()
		s.session.Remote = visitor.RemoteAddr().String()
	}
	if target != nil {
		s.session.TargetLocal = target.LocalAddr().String()
		s.session.TargetRemote = target.RemoteAddr().String()
	}
}
func (s *Span) SetLink(link interface{}) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.session.Link = link
	s.mu.Unlock()
}
func (s *Span) snapshot() Session {
	s.mu.Lock()
	v := s.session
	s.mu.Unlock()
	v.UploadBytes = atomic.LoadUint64(&s.upload)
	v.DownloadBytes = atomic.LoadUint64(&s.download)
	v.RawReadBytes = atomic.LoadUint64(&s.rawRead)
	v.ReadWaitMS = float64(atomic.LoadUint64(&s.readNanos)) / 1e6
	v.WriteWaitMS = float64(atomic.LoadUint64(&s.writeNanos)) / 1e6
	v.MaxWriteMS = float64(atomic.LoadUint64(&s.maxWrite)) / 1e6
	return v
}
func ErrorCode(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "eof"
	}
	if errors.Is(err, net.ErrClosed) {
		return "local_closed"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	t := strings.ToLower(err.Error())
	switch {
	case strings.Contains(t, "reset"):
		return "reset"
	case strings.Contains(t, "refused"):
		return "refused"
	case strings.Contains(t, "context canceled"):
		return "cancelled"
	case strings.Contains(t, "authentication"):
		return "auth"
	case strings.Contains(t, "broken pipe"):
		return "broken_pipe"
	}
	return "io_error"
}
func (s *Span) Finish(operation string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	now := time.Now().UTC()
	s.session.Closed = &now
	s.session.Operation = operation
	s.session.CloseCode = ErrorCode(err)
	s.session.State = "closed"
	if err != nil {
		s.session.Error = err.Error()
	}
	confirmed := s.confirmed
	s.mu.Unlock()
	v := s.snapshot()
	m := s.meter
	m.mu.Lock()
	delete(m.active, v.Key)
	m.closed++
	if !confirmed {
		m.failures++
	}
	if v.CloseCode != "eof" {
		m.errors[v.CloseCode]++
	}
	m.recent = append(m.recent, v)
	if len(m.recent) > MaxRecent {
		m.recent = m.recent[len(m.recent)-MaxRecent:]
	}
	m.mu.Unlock()
	if err != nil {
		m.Event(v.CloseCode, v.Error, v.Key)
	}
}

// Copy preserves half-close at the caller. Metrics distinguish bytes read from
// bytes accepted by the next local socket, not bytes consumed by a browser.
func (s *Span) Copy(dst io.Writer, src io.Reader, direction string) (int64, error) {
	if s == nil {
		return io.CopyBuffer(dst, src, make([]byte, 64<<10))
	}
	buf := make([]byte, 64<<10)
	var total int64
	for {
		start := time.Now()
		n, readErr := src.Read(buf)
		atomic.AddUint64(&s.readNanos, uint64(time.Since(start)))
		if n > 0 {
			atomic.AddUint64(&s.rawRead, uint64(n))
			start = time.Now()
			written, writeErr := dst.Write(buf[:n])
			elapsed := time.Since(start)
			atomic.AddUint64(&s.writeNanos, uint64(elapsed))
			for {
				old := atomic.LoadUint64(&s.maxWrite)
				if old >= uint64(elapsed) || atomic.CompareAndSwapUint64(&s.maxWrite, old, uint64(elapsed)) {
					break
				}
			}
			if elapsed > 100*time.Millisecond {
				atomic.AddUint64(&s.meter.slow, 1)
			}
			if written > 0 {
				total += int64(written)
				if direction == "upload" {
					atomic.AddUint64(&s.upload, uint64(written))
					atomic.AddUint64(&s.meter.upload, uint64(written))
				} else {
					atomic.AddUint64(&s.download, uint64(written))
					atomic.AddUint64(&s.meter.download, uint64(written))
				}
			}
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}
func (m *Meter) UDP(direction, reason string, n int) {
	if m == nil {
		return
	}
	if reason != "" {
		m.mu.Lock()
		if len(m.udpDrops) < 32 || m.udpDrops[reason] > 0 {
			m.udpDrops[reason]++
		}
		m.mu.Unlock()
		return
	}
	if direction == "sent" {
		atomic.AddUint64(&m.udpSent, 1)
	} else {
		atomic.AddUint64(&m.udpReceived, 1)
	}
}
func (m *Meter) UDPActive(delta int64) {
	if m != nil {
		atomic.AddInt64(&m.udpActive, delta)
	}
}
func (m *Meter) Snapshot(limit int) Snapshot {
	if limit <= 0 || limit > MaxRecent {
		limit = MaxRecent
	}
	m.mu.Lock()
	v := Snapshot{Schema: Schema, ProcessID: m.id, Component: m.component, Time: time.Now().UTC(), Started: m.started, Metrics: Metrics{Attempts: m.attempts, Success: m.success, Failures: m.failures, Closed: m.closed, Active: len(m.active), UDPActive: atomic.LoadInt64(&m.udpActive), UDPSent: atomic.LoadUint64(&m.udpSent), UDPReceived: atomic.LoadUint64(&m.udpReceived), UploadBytes: atomic.LoadUint64(&m.upload), DownloadBytes: atomic.LoadUint64(&m.download), SlowWrite: atomic.LoadUint64(&m.slow), Errors: map[string]uint64{}, UDPDrops: map[string]uint64{}, Latency: map[string]interface{}{}}, Sessions: []Session{}}
	for k, n := range m.errors {
		v.Metrics.Errors[k] = n
	}
	v.Metrics.ConfirmHistogram = append([]uint64(nil), m.confirmHistogram...)
	for k, n := range m.udpDrops {
		v.Metrics.UDPDrops[k] = n
	}
	active := make([]*Span, 0, len(m.active))
	for _, s := range m.active {
		active = append(active, s)
	}
	v.Sessions = append(v.Sessions, m.recent...)
	v.Events = append([]Event(nil), m.events...)
	values := []float64{}
	for _, l := range m.latency {
		if time.Since(l.at) < time.Minute {
			values = append(values, l.ms)
		}
	}
	m.mu.Unlock()
	for _, s := range active {
		v.Sessions = append(v.Sessions, s.snapshot())
	}
	sort.Slice(v.Sessions, func(i, j int) bool { return v.Sessions[i].Opened.After(v.Sessions[j].Opened) })
	if len(v.Sessions) > limit {
		v.Truncated = true
		v.Sessions = v.Sessions[:limit]
	}
	sort.Float64s(values)
	v.Metrics.Latency["samples"] = len(values)
	for _, p := range []struct {
		name string
		q    float64
	}{{"p50", .5}, {"p95", .95}, {"p99", .99}} {
		v.Metrics.Latency[p.name] = nil
		if len(values) > 0 {
			idx := int(float64(len(values)-1) * p.q)
			v.Metrics.Latency[p.name] = values[idx]
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	v.HeapBytes = mem.HeapAlloc
	v.Goroutines = runtime.NumGoroutine()
	v.Build = map[string]string{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" {
				v.Build[setting.Key] = setting.Value
			}
		}
	}
	return v
}
