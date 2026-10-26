package service

// The collector is outside all forwarding paths. A blocked HTTP/RPC/history
// writer can make diagnostics stale, but cannot stop the XTCP data connection.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"x-ui/reverseexit"
	"x-ui/rsxdiag"
)

type DiagnosticsSettings struct {
	Paused           bool                `json:"paused"`
	Rules            []rsxdiag.AlertRule `json:"rules"`
	RetentionDays    int                 `json:"retention_days"`
	HistoryBudgetMiB int                 `json:"history_budget_mib"`
	Notifications    bool                `json:"notifications"`
}
type diagnosticSource struct {
	Data       json.RawMessage `json:"data"`
	Observed   time.Time       `json:"observed"`
	Error      string          `json:"error,omitempty"`
	AgeSeconds float64         `json:"age_seconds"`
	Stale      bool            `json:"stale"`
}
type counterObservation struct {
	value float64
	at    time.Time
}
type diagnosticFailureSample struct {
	at                 time.Time
	attempts, failures float64
}
type DiagnosticsRuntime struct {
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	mu                sync.Mutex
	cfg               reverseSocksSaved
	manager           *reverseexit.Manager
	frpc              *managedFRPC
	root              string
	settings          DiagnosticsSettings
	history           *rsxdiag.History
	alerts            *rsxdiag.AlertEngine
	runner            *rsxdiag.Runner
	local             diagnosticSource
	node              diagnosticSource
	targetHealth      targetHealth
	quality           []rsxdiag.Point
	last              rsxdiag.Point
	counters          map[string]counterObservation
	recentSuccess     []diagnosticFailureSample
	recentNodeSuccess []diagnosticFailureSample
	reconnects        []time.Time
	previousState     string
	historyError      string
	lastAlertWrite    time.Time
}

func newDiagnostics(parent context.Context, cfg reverseSocksSaved, manager *reverseexit.Manager, frpc *managedFRPC) (*DiagnosticsRuntime, error) {
	root := filepath.Join(filepath.Dir(cfg.FRPCConfigPath), "diagnostics")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe diagnostics directory")
	}
	settings := DiagnosticsSettings{Rules: rsxdiag.DefaultRules(), RetentionDays: 30, HistoryBudgetMiB: 128}
	if err := rsxdiag.ReadSettings(root, &settings); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := validateDiagnosticsSettings(settings); err != nil {
		return nil, err
	}
	history, err := rsxdiag.NewHistory(filepath.Join(root, "history"), settings.RetentionDays, int64(settings.HistoryBudgetMiB)<<20)
	if err != nil {
		return nil, err
	}
	alerts, err := rsxdiag.NewAlerts(settings.Rules)
	if err != nil {
		history.Close()
		return nil, err
	}
	if old, err := rsxdiag.ReadAlerts(root); err == nil {
		alerts.Restore(old)
	}
	ctx, cancel := context.WithCancel(parent)
	d := &DiagnosticsRuntime{ctx: ctx, cancel: cancel, cfg: cfg, manager: manager, frpc: frpc, root: root, settings: settings, history: history, alerts: alerts, runner: rsxdiag.NewRunner(), counters: map[string]counterObservation{}}
	d.wg.Add(3)
	go d.collect()
	go d.collectNode()
	go d.collectTargetHealth()
	return d, nil
}
func validateDiagnosticsSettings(s DiagnosticsSettings) error {
	if s.RetentionDays < 1 || s.RetentionDays > 30 {
		return errors.New("history retention must be 1..30 days")
	}
	if s.HistoryBudgetMiB < 1 || s.HistoryBudgetMiB > 512 {
		return errors.New("history budget must be 1..512 MiB")
	}
	_, err := rsxdiag.NewAlerts(s.Rules)
	return err
}
func (d *DiagnosticsRuntime) Close() {
	d.cancel()
	d.runner.Close()
	d.wg.Wait()
	d.history.Close()
	_ = rsxdiag.WriteAlerts(d.root, d.alerts.Snapshot())
}
func (d *DiagnosticsRuntime) Settings() DiagnosticsSettings {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.settings
	s.Rules = append([]rsxdiag.AlertRule(nil), s.Rules...)
	return s
}
func (d *DiagnosticsRuntime) SetSettings(s DiagnosticsSettings) error {
	if err := validateDiagnosticsSettings(s); err != nil {
		return err
	}
	if err := rsxdiag.WriteSettings(d.root, s); err != nil {
		return err
	}
	if err := d.alerts.SetRules(s.Rules); err != nil {
		return err
	}
	d.mu.Lock()
	d.settings = s
	d.mu.Unlock()
	d.history.SetLimits(s.RetentionDays, int64(s.HistoryBudgetMiB)<<20)
	return nil
}
func (d *DiagnosticsRuntime) localClient() rsxdiag.LocalClient {
	return rsxdiag.LocalClient{Addr: d.cfg.FRPCDiagnosticsAddr, Token: rsxdiag.LocalToken(d.cfg.Token)}
}
func (d *DiagnosticsRuntime) collectNode() {
	defer d.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
		if d.Settings().Paused {
			continue
		}
		peer := d.manager.DiagnosticPeer()
		if peer == nil {
			d.mu.Lock()
			d.node.Error = "node diagnostics not negotiated; update/enable Windows --diagnostics"
			d.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
		raw, err := peer.Call(ctx, "snapshot", nil)
		cancel()
		d.mu.Lock()
		if err != nil {
			d.node.Error = err.Error()
		} else {
			d.node = diagnosticSource{Data: raw, Observed: time.Now().UTC()}
		}
		d.mu.Unlock()
	}
}
func (d *DiagnosticsRuntime) collect() {
	defer d.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var bucket []rsxdiag.Point
	lastTick := time.Now()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		elapsed := now.Sub(lastTick).Seconds()
		lastTick = now
		if d.Settings().Paused {
			if changes := d.alerts.MarkUnknown(time.Now().UTC()); len(changes) > 0 {
				if err := rsxdiag.WriteAlerts(d.root, d.alerts.Snapshot()); err != nil {
					d.mu.Lock()
					d.historyError = err.Error()
					d.mu.Unlock()
				}
			}
			bucket = nil
			continue
		}
		ctx, cancel := context.WithTimeout(d.ctx, 800*time.Millisecond)
		raw, err := d.localClient().Do(ctx, "GET", "/v1/snapshot?compact=1", nil, 4<<20)
		cancel()
		d.mu.Lock()
		if err != nil {
			d.local.Error = err.Error()
		} else {
			d.local = diagnosticSource{Data: raw, Observed: time.Now().UTC()}
		}
		d.mu.Unlock()
		if elapsed > 3 {
			elapsed = 1
		} // do not fill observation gaps with invented uptime
		point := d.makePoint(now.UTC(), elapsed)
		d.addQualityWindows(&point)
		d.mu.Lock()
		d.last = point
		d.mu.Unlock()
		if changes := d.alerts.Evaluate(point); len(changes) > 0 {
			if err := rsxdiag.WriteAlerts(d.root, d.alerts.Snapshot()); err != nil {
				d.mu.Lock()
				d.historyError = err.Error()
				d.mu.Unlock()
			}
		}
		bucket = append(bucket, point)
		if len(bucket) >= 5 {
			combined := mergeDiagnosticPoints(bucket)
			d.history.Add(combined)
			bucket = nil
		}
	}
}
func diagnosticMap(raw json.RawMessage) map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal(raw, &m)
	return m
}
func child(m map[string]interface{}, key string) map[string]interface{} {
	v, _ := m[key].(map[string]interface{})
	return v
}
func number(m map[string]interface{}, key string) (float64, bool) {
	v, ok := m[key].(float64)
	return v, ok && !math.IsNaN(v) && !math.IsInf(v, 0)
}
func text(m map[string]interface{}, key string) string { v, _ := m[key].(string); return v }
func fptr(v float64) *float64                          { return &v }
func sourceTime(m map[string]interface{}, fallback time.Time) time.Time {
	at, err := time.Parse(time.RFC3339Nano, text(m, "time"))
	if err != nil {
		return fallback
	}
	return at
}
func (d *DiagnosticsRuntime) delta(key string, value float64, at time.Time) (float64, float64, bool) {
	old, ok := d.counters[key]
	d.counters[key] = counterObservation{value, at}
	if !ok || value < old.value {
		return 0, 0, false
	}
	seconds := at.Sub(old.at).Seconds()
	if seconds <= 0 || seconds > 15 {
		return 0, 0, false
	}
	return value - old.value, seconds, true
}
func (d *DiagnosticsRuntime) makePoint(now time.Time, seconds float64) rsxdiag.Point {
	d.mu.Lock()
	local, node, health := d.local, d.node, d.targetHealth
	d.mu.Unlock()
	point := rsxdiag.Point{Time: now, Seconds: seconds, Values: map[string]*float64{}, Totals: map[string]float64{}, Config: map[string]string{"protocol": d.cfg.Protocol, "congestion_mode": d.cfg.CongestionMode, "target_mbps": strconv.Itoa(d.cfg.TargetMbps), "max_mbps": strconv.Itoa(d.cfg.MaxMbps), "target_socks": d.cfg.TargetSOCKSAddr}}
	point.Histograms = map[string][]uint64{}
	if !health.Checked.IsZero() && now.Sub(health.Checked) < 90*time.Second {
		point.Values["target_socks_online"] = fptr(boolFloat(health.State == "ready"))
		point.Values["target_failures"] = fptr(float64(health.Failures))
	}
	st := d.manager.Status()
	fs := d.frpc.Status()
	if st.Enabled {
		point.Values["frpc_online"] = fptr(boolFloat(fs.State == "running"))
		point.Values["frpc_error"] = fptr(boolFloat(fs.State == "error"))
		point.Values["rsx3_online"] = fptr(boolFloat(st.State == "online"))
		point.Values["warp_online"] = nil
		if st.WARPState == "ready" {
			point.Values["warp_online"] = fptr(1)
		} else if st.WARPState == "unavailable" {
			point.Values["warp_online"] = fptr(0)
		}
		point.Values["warp_failures"] = fptr(float64(st.WARPFailures))
	}
	if d.previousState != "" && d.previousState != "online" && st.State == "online" {
		d.reconnects = append(d.reconnects, now)
		point.Totals["reconnections"] = 1
	}
	d.previousState = st.State
	keep := d.reconnects[:0]
	for _, t := range d.reconnects {
		if now.Sub(t) < 5*time.Minute {
			keep = append(keep, t)
		}
	}
	d.reconnects = keep
	point.Values["reconnects_5m"] = fptr(float64(len(keep)))
	if node.Observed.IsZero() {
		point.Values["node_age_seconds"] = fptr(now.Sub(d.historyStarted()).Seconds())
	} else {
		point.Values["node_age_seconds"] = fptr(now.Sub(node.Observed).Seconds())
	}
	if meter := d.manager.Metrics(); meter != nil {
		s := meter.Snapshot(1)
		confirmHist := rsxdiag.NewHistogram()
		validHist := false
		for i, n := range s.Metrics.ConfirmHistogram {
			if delta, _, ok := d.delta("confirmHist/"+s.ProcessID+"/"+strconv.Itoa(i), float64(n), now); ok && i < len(confirmHist) {
				confirmHist[i] = uint64(delta)
				validHist = true
			}
		}
		if validHist {
			point.Histograms["confirm_ms"] = confirmHist
		}
		point.Values["active_tcp"] = fptr(float64(s.Metrics.Active))
		point.Values["active_udp"] = fptr(float64(s.Metrics.UDPActive))
		point.Values["heap_bytes"] = fptr(float64(s.HeapBytes))
		point.Values["goroutines"] = fptr(float64(s.Goroutines))
		for _, n := range []string{"p50", "p95", "p99"} {
			if v, ok := s.Metrics.Latency[n].(float64); ok {
				point.Values["confirm_"+n+"_ms"] = fptr(v)
			}
		}
		for name, value := range map[string]uint64{"upload_bytes": s.Metrics.UploadBytes, "download_bytes": s.Metrics.DownloadBytes, "attempts": s.Metrics.Attempts, "failures": s.Metrics.Failures, "slow_write": s.Metrics.SlowWrite, "udp_sent": s.Metrics.UDPSent, "udp_received": s.Metrics.UDPReceived} {
			if delta, dt, ok := d.delta("agent/"+s.ProcessID+"/"+name, float64(value), now); ok {
				point.Totals[name] = delta
				if name == "upload_bytes" || name == "download_bytes" {
					point.Values[name+"_per_second"] = fptr(delta / dt)
				}
			}
		}
		var rate *float64
		d.recentSuccess, rate = failureWindow(d.recentSuccess, now, point.Totals["attempts"], point.Totals["failures"])
		if rate != nil {
			point.Values["agent_tcp_failure_pct"] = rate
			point.Values["tcp_failure_pct"] = rate
		}
	}
	for _, side := range []struct {
		name   string
		source diagnosticSource
	}{{"quic", local}, {"node", node}} {
		if side.source.Observed.IsZero() || now.Sub(side.source.Observed) > 6*time.Second {
			continue
		}
		m := diagnosticMap(side.source.Data)
		if side.name == "node" {
			if met := child(m, "metrics"); met != nil {
				at := sourceTime(m, side.source.Observed)
				if v, ok := number(met, "download_bytes"); ok {
					if delta, dt, ok := d.delta("python/"+text(m, "process_id")+"/download", v, at); ok {
						point.Values["node_download_bytes_per_second"] = fptr(delta / dt)
						point.Totals["node_download_bytes"] = delta
					}
				}
				for _, name := range []string{"attempts", "failures", "success", "slow_write"} {
					if v, ok := number(met, name); ok {
						if delta, _, valid := d.delta("python/"+text(m, "process_id")+"/"+name, v, at); valid {
							point.Totals["node_"+name] = delta
						}
					}
				}
				for name, raw := range child(met, "errors") {
					if len(name) > 48 {
						continue
					}
					if v, ok := raw.(float64); ok {
						if delta, _, valid := d.delta("python/"+text(m, "process_id")+"/errors/"+name, v, at); valid {
							point.Totals["node_error_"+name] = delta
						}
					}
				}
				var rate *float64
				d.recentNodeSuccess, rate = failureWindow(d.recentNodeSuccess, now, point.Totals["node_attempts"], point.Totals["node_failures"])
				if rate != nil {
					point.Values["node_tcp_failure_pct"] = rate
					if previous := point.Values["tcp_failure_pct"]; previous == nil || *rate > *previous {
						point.Values["tcp_failure_pct"] = rate
					}
				}
				if v, ok := number(met, "active"); ok {
					point.Values["node_active_tcp"] = fptr(v)
				}
			}
			m = child(m, "frpc")
		}
		list, _ := m["connections"].([]interface{})
		var active []map[string]interface{}
		for _, raw := range list {
			c, ok := raw.(map[string]interface{})
			if ok && text(c, "state") == "ready" {
				active = append(active, c)
			}
		}
		if side.name == "quic" {
			point.Values["xtcp_online"] = fptr(boolFloat(len(active) > 0))
		}
		if len(active) != 1 {
			continue
		}
		c := active[0]
		prefix := side.name + "_"
		if v, ok := number(c, "rtt_ms"); ok {
			hist := rsxdiag.NewHistogram()
			rsxdiag.ObserveHistogram(hist, v)
			point.Histograms[prefix+"rtt_ms"] = hist
		}
		for _, name := range []string{"rtt_ms", "min_rtt_ms", "rtt_variance_ms", "cwnd_bytes", "bytes_in_flight"} {
			if v, ok := number(c, name); ok {
				point.Values[prefix+name] = fptr(v)
			}
		}
		b := child(c, "brutal")
		for _, name := range []string{"target_mbps", "max_mbps", "pacing_bytes_per_second", "sample_ack_ratio", "compensation_ack_ratio"} {
			if v, ok := number(b, name); ok {
				point.Values[prefix+name] = fptr(v)
			}
		}
		id := text(m, "process_id") + "/" + text(c, "connection_id")
		at := sourceTime(m, side.source.Observed)
		deltas := map[string]float64{}
		for _, name := range []string{"sent_bytes", "received_bytes", "sent_packets", "received_packets", "loss_declarations", "spurious_loss", "stream_sent_bytes", "stream_retransmitted_bytes"} {
			if v, ok := number(c, name); ok {
				if delta, dt, valid := d.delta(prefix+id+name, v, at); valid {
					deltas[name] = delta
					if name == "sent_bytes" || name == "received_bytes" {
						point.Values[prefix+name+"_per_second"] = fptr(delta / dt)
					}
					point.Totals[prefix+name] = delta
				}
			}
		}
		if deltas["sent_packets"] >= 10 {
			point.Values[prefix+"loss_pct"] = fptr(math.Min(100, deltas["loss_declarations"]/deltas["sent_packets"]*100))
		}
		if deltas["stream_sent_bytes"] > 0 {
			if _, valid := deltas["stream_retransmitted_bytes"]; valid {
				point.Values[prefix+"stream_retransmit_pct"] = fptr(deltas["stream_retransmitted_bytes"] / deltas["stream_sent_bytes"] * 100)
			}
		}
	}
	if len(d.counters) > 2048 {
		for k, v := range d.counters {
			if now.Sub(v.at) > 10*time.Minute {
				delete(d.counters, k)
			}
		}
	}
	return point
}
func (d *DiagnosticsRuntime) historyStarted() time.Time {
	if m := d.manager.Metrics(); m != nil {
		return m.Snapshot(1).Started
	}
	return time.Now()
}
func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// Each side has its own five-minute counter window. We never sum Agent and
// Python attempts as though both represented distinct browser requests.
func failureWindow(history []diagnosticFailureSample, now time.Time, attempts, failures float64) ([]diagnosticFailureSample, *float64) {
	if attempts > 0 || failures > 0 {
		history = append(history, diagnosticFailureSample{at: now, attempts: attempts, failures: failures})
	}
	kept := history[:0]
	var total, failed float64
	for _, v := range history {
		if now.Sub(v.at) < 5*time.Minute {
			kept = append(kept, v)
			total += v.attempts
			failed += v.failures
		}
	}
	if total < 10 {
		return kept, nil
	}
	value := math.Min(100, failed/total*100)
	return kept, &value
}
func mergeDiagnosticPoints(points []rsxdiag.Point) rsxdiag.Point {
	out := rsxdiag.Point{Time: points[len(points)-1].Time, Values: map[string]*float64{}, Totals: map[string]float64{}, Config: points[len(points)-1].Config}
	out.Histograms = map[string][]uint64{}
	out.Weights = map[string]float64{}
	sums, weights := map[string]float64{}, map[string]float64{}
	for _, p := range points {
		for k, h := range p.Histograms {
			if out.Histograms[k] == nil {
				out.Histograms[k] = rsxdiag.NewHistogram()
			}
			rsxdiag.MergeHistogram(out.Histograms[k], h)
		}
		out.Seconds += p.Seconds
		for k, v := range p.Values {
			if weight := p.SampleSeconds(k); weight > 0 {
				sums[k] += *v * weight
				weights[k] += weight
			}
		}
		for k, v := range p.Totals {
			out.Totals[k] += v
		}
	}
	for k, v := range sums {
		out.Values[k] = fptr(v / weights[k])
		out.Weights[k] = weights[k]
	}
	return out
}
func (d *DiagnosticsRuntime) Snapshot() map[string]interface{} {
	d.mu.Lock()
	local, node, last, historyError, health := d.local, d.node, d.last, d.historyError, d.targetHealth
	d.mu.Unlock()
	now := time.Now()
	for _, s := range []*diagnosticSource{&local, &node} {
		if s.Observed.IsZero() {
			s.AgeSeconds = -1
			s.Stale = true
		} else {
			s.AgeSeconds = now.Sub(s.Observed).Seconds()
			s.Stale = s.AgeSeconds > 6
		}
	}
	var agent interface{}
	if meter := d.manager.Metrics(); meter != nil {
		agent = meter.Snapshot(128)
	}
	return map[string]interface{}{"schema": rsxdiag.Schema, "time": now.UTC(), "enabled": true, "settings": d.Settings(), "status": d.manager.Status(), "frpc": d.frpc.Status(), "local_frpc": local, "node": node, "target_health": health, "agent": agent, "point": last, "alerts": d.alerts.Snapshot(), "history": d.history.Status(), "history_error": historyError, "tasks": d.runner.Snapshot(), "measurement_notes": []string{"GO/Python byte counters are accepted relay bytes, not HTTP body or browser-consumption acknowledgements.", "QUIC wire counters exclude SOCKS5, outer WARP and UDP/IP framing.", "loss_pct is the 60-second counter-delta loss-declaration ratio; loss may be declared later than its send interval, so it is not end-to-end DATAGRAM loss.", "Long-term quantiles merge fixed histograms; per-request short-window percentiles are not averaged into a global percentile."}}
}
func (s *ReverseSocksService) Diagnostics() (*DiagnosticsRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diagnostics == nil {
		return nil, fmt.Errorf("diagnostics disabled or unavailable: %s", s.diagnosticsError)
	}
	return s.diagnostics, nil
}
func (d *DiagnosticsRuntime) History(span time.Duration) (interface{}, error) {
	since := time.Now().UTC().Add(-span)
	points, err := d.history.Read(since, 1000)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"points": points, "summary": rsxdiag.Summarize(points, span), "range_seconds": span.Seconds()}, nil
}
func (d *DiagnosticsRuntime) CancelTask(id string) error { return d.runner.Cancel(id) }
