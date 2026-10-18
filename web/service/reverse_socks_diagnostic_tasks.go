package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"x-ui/reverseexit"
	"x-ui/rsxdiag"
	"x-ui/xray"
)

func (d *DiagnosticsRuntime) StartTask(spec rsxdiag.TaskSpec) (*rsxdiag.Task, error) {
	if d.Settings().Paused {
		return nil, errors.New("resume diagnostics before starting an active test")
	}
	return d.runner.Start(d.ctx, spec, func(ctx context.Context, id string, step func(rsxdiag.Step)) (interface{}, error) {
		switch spec.Kind {
		case "self_test":
			return d.selfTest(ctx, step)
		case "raw_benchmark": // Direction is always relative to the Windows client.
			direction := "upload"
			if spec.Direction == "upload" {
				direction = "download"
			}
			raw, err := d.localClient().Do(ctx, "POST", "/v1/benchmark", map[string]interface{}{"bytes": spec.Bytes, "direction": direction}, 1<<20)
			return raw, err
		case "socks_benchmark":
			peer := d.manager.DiagnosticPeer()
			if peer == nil {
				return nil, errors.New("Windows diagnostics unavailable")
			}
			return peer.Benchmark(ctx, spec.Bytes, spec.Direction)
		case "route_check":
			return d.routeCheck(ctx, spec.Target)
		case "egress_check":
			return d.egressCheck(ctx, spec)
		case "target_benchmark":
			return d.targetBenchmark(ctx, spec)
		case "capture":
			return d.captureReport(ctx, id, spec.Seconds, step)
		case "report":
			return d.makeReport(ctx, id, step, false)
		}
		return nil, errors.New("unsupported diagnostic task")
	})
}
func (d *DiagnosticsRuntime) selfTest(ctx context.Context, progress func(rsxdiag.Step)) (interface{}, error) {
	results := []rsxdiag.Step{}
	check := func(name string, fn func(context.Context) (interface{}, error)) {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		stepCtx, cancel := context.WithTimeout(ctx, 9*time.Second)
		v, err := fn(stepCtx)
		cancel()
		s := rsxdiag.Step{Name: name, Time: time.Now().UTC(), State: "ok", DurationMS: float64(time.Since(start)) / float64(time.Millisecond), Evidence: v}
		if err != nil {
			s.State = "failed"
			s.Error = err.Error()
		}
		if m, ok := v.(json.RawMessage); ok {
			var state struct {
				State string `json:"state"`
				Error string `json:"error"`
			}
			if json.Unmarshal(m, &state) == nil && state.State == "failed" {
				s.State = "failed"
				s.Error = state.Error
			}
		}
		results = append(results, s)
		progress(s)
	}
	check("frpc_process", func(context.Context) (interface{}, error) {
		s := d.frpc.Status()
		if s.State != "running" {
			return s, errors.New("managed frpc is not running")
		}
		return s, nil
	})
	check("frpc_warp_configuration", func(context.Context) (interface{}, error) {
		raw, err := os.ReadFile(d.cfg.FRPCConfigPath)
		if err != nil {
			return nil, err
		}
		return verifyRuntimePrivacy(string(raw), d.cfg.Config)
	})
	check("warp_tcp_connect", func(c context.Context) (interface{}, error) {
		return reverseexit.DiagnosticConnect(c, d.cfg.Config, d.cfg.WARPProbe, true)
	})
	check("warp_udp_roundtrip", func(c context.Context) (interface{}, error) { return reverseexit.DiagnosticDNS(c, d.cfg.Config, true) })
	check("frps_control", func(c context.Context) (interface{}, error) {
		raw, err := d.localClient().Do(c, "GET", "/v1/snapshot?compact=1", nil, 4<<20)
		if err != nil {
			return nil, err
		}
		m := diagnosticMap(raw)
		s := child(child(m, "stages"), "frps/control")
		if text(s, "state") != "ready" {
			return s, errors.New("frps control is not ready")
		}
		return s, nil
	})
	check("xtcp_quic", func(c context.Context) (interface{}, error) {
		raw, err := d.localClient().Do(c, "GET", "/v1/snapshot?compact=1", nil, 4<<20)
		if err != nil {
			return nil, err
		}
		connections, _ := diagnosticMap(raw)["connections"].([]interface{})
		for _, v := range connections {
			m, _ := v.(map[string]interface{})
			if text(m, "state") == "ready" {
				return m, nil
			}
		}
		return nil, errors.New("no ready XTCP QUIC connection")
	})
	check("node_diagnostic_ping", func(c context.Context) (interface{}, error) {
		p := d.manager.DiagnosticPeer()
		if p == nil {
			return nil, errors.New("Windows diagnostics not negotiated")
		}
		start := time.Now()
		raw, err := p.Call(c, "ping", nil)
		if err != nil {
			return nil, err
		}
		end := time.Now()
		v := diagnosticMap(raw)
		if t, err := time.Parse(time.RFC3339Nano, text(v, "time")); err == nil {
			v["clock_offset_estimate_ms"] = float64(t.Sub(start.Add(end.Sub(start)/2))) / float64(time.Millisecond)
			v["clock_offset_uncertainty_ms"] = float64(end.Sub(start)) / float64(2*time.Millisecond)
		}
		v["roundtrip_ms"] = float64(end.Sub(start)) / float64(time.Millisecond)
		return v, nil
	})
	check("quic_datagram_roundtrip", func(c context.Context) (interface{}, error) {
		return d.localClient().Do(c, "POST", "/v1/udp-probe", map[string]interface{}{}, 1<<20)
	})
	check("rsx3_socks_tcp_1mb", func(c context.Context) (interface{}, error) {
		p := d.manager.DiagnosticPeer()
		if p == nil {
			return nil, errors.New("Windows diagnostics not negotiated")
		}
		return p.Benchmark(c, 1_000_000, "download")
	})
	check("rsx3_socks_udp", func(c context.Context) (interface{}, error) {
		p := d.manager.DiagnosticPeer()
		if p == nil {
			return nil, errors.New("Windows diagnostics not negotiated")
		}
		return p.Call(c, "socks_udp", nil)
	})
	check("target_socks_tcp", func(c context.Context) (interface{}, error) {
		return reverseexit.DiagnosticConnect(c, d.cfg.Config, d.cfg.WARPProbe, false)
	})
	check("target_socks_udp", func(c context.Context) (interface{}, error) { return reverseexit.DiagnosticDNS(c, d.cfg.Config, false) })
	failed := 0
	for _, s := range results {
		if s.State == "failed" {
			failed++
		}
	}
	state := "ok"
	if failed > 0 {
		state = "degraded"
	}
	return map[string]interface{}{"state": state, "failed": failed, "steps": results, "note": "Configuration compliance is not packet-capture proof of public-IP privacy. Handshake checks do not certify website reachability or bandwidth."}, ctx.Err()
}

// This is a configuration check only, never misrepresented as packet capture.
func verifyRuntimePrivacy(raw string, cfg reverseexit.Config) (interface{}, error) {
	endpoint := url.URL{Scheme: "socks5", Host: cfg.WARPAddr}
	if cfg.WARPUsername != "" {
		endpoint.User = url.UserPassword(cfg.WARPUsername, cfg.WARPPassword)
	}
	// Generated TOML is controlled by this service; exact quoted values are
	// checked without resolving STUN names or contacting any public endpoint.
	expected := []string{"transport.protocol = \"tcp\"", "transport.proxyURL = " + strconv.Quote(endpoint.String()), "udpProxyURL = " + strconv.Quote(endpoint.String()), "disableAssistedAddrs = true"}
	for _, line := range expected {
		if !strings.Contains(raw, line) {
			return nil, errors.New("managed frpc WARP-only configuration does not match expected values")
		}
	}
	return map[string]interface{}{"state": "ok", "evidence": "generated_config_exact_values", "warp_proxy": cfg.WARPAddr, "target_proxy": cfg.TargetSOCKSAddr, "packet_capture_verified": false}, nil
}

func runningXrayLog() (string, error) {
	raw, err := os.ReadFile(xray.GetConfigPath())
	if err != nil {
		return "", err
	}
	var c struct {
		Log struct {
			Access string `json:"access"`
		} `json:"log"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	if c.Log.Access == "" || c.Log.Access == "none" {
		return "", errors.New("Xray access log is not enabled in running config")
	}
	path := filepath.Clean(c.Log.Access)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Xray access log is not a regular file")
	}
	return path, nil
}
func tailFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("refusing nonregular diagnostic log")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, max))
}

var outboundPattern = regexp.MustCompile(`\[([^\]\r\n]*?)\s*(?:->|>>)\s*([^\]\s]+)\]`)

func correlateOutbound(lines, local, target string) map[string]interface{} {
	matches := []string{}
	tags := map[string]bool{}
	for _, line := range strings.Split(lines, "\n") {
		if !logHasEndpoint(line, local) || !logHasEndpoint(line, target) {
			continue
		}
		found := outboundPattern.FindStringSubmatch(line)
		if len(found) == 3 {
			matches = append(matches, line)
			tags[found[2]] = true
		}
	}
	out := map[string]interface{}{"verified": false, "state": "unverified", "evidence": "Xray access log source-tuple and destination correlation", "rule_verified": false, "rule_reason": "Xray access log exposes outbound tag, not the exact routing rule index", "matches": matches}
	if len(tags) == 1 {
		for tag := range tags {
			out["outbound"] = tag
		}
		out["verified"] = true
		out["state"] = "ok"
	} else if len(tags) > 1 {
		out["reason"] = "ambiguous outbound tags"
	} else {
		out["reason"] = "no matching access-log evidence"
	}
	return out
}

func logHasEndpoint(line, endpoint string) bool {
	for _, field := range strings.Fields(line) {
		field = strings.Trim(field, "[],\"")
		if field == endpoint || field == "tcp:"+endpoint || field == "udp:"+endpoint {
			return true
		}
	}
	return false
}

// Public source IP can only be certified by the responding echo service.
// Its result is scoped to this exact hostname, not to an unrelated route.
func (d *DiagnosticsRuntime) egressCheck(ctx context.Context, s rsxdiag.TaskSpec) (interface{}, error) {
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("IP echo test requires an explicit HTTPS URL without credentials")
	}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, DisableKeepAlives: true, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DialContext: func(c context.Context, network, address string) (net.Conn, error) {
		host, raw, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(raw)
		if err != nil {
			return nil, err
		}
		return reverseexit.DiagnosticTarget(c, d.cfg.Config, host, port, s.UseWARP)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return map[string]interface{}{"http_status": resp.StatusCode}, errors.New("IP echo returned non-200 response")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4096 {
		return nil, errors.New("IP echo response exceeds 4 KiB")
	}
	ip, err := parseEchoIP(raw)
	if err != nil {
		return map[string]interface{}{"response": string(raw)}, err
	}
	return map[string]interface{}{"state": "ok", "verified": true, "observed_public_ip": ip, "probe_hostname": u.Hostname(), "use_warp": s.UseWARP, "duration_ms": float64(time.Since(start)) / float64(time.Millisecond), "scope": "Only this IP-echo hostname and request. This is not proof of another domain's route or frpc source privacy."}, nil
}
func parseEchoIP(raw []byte) (string, error) {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "{") {
		var v struct {
			IP string `json:"ip"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return "", errors.New("invalid IP echo JSON")
		}
		value = strings.TrimSpace(v.IP)
	}
	ip := net.ParseIP(value)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", errors.New("IP echo did not return a usable public address")
	}
	return ip.String(), nil
}
func correlateOutboundWithin(lines, local, target string, opened time.Time, closed *time.Time) map[string]interface{} {
	end := time.Now().UTC()
	if closed != nil {
		end = *closed
	}
	kept := []string{}
	for _, line := range strings.Split(lines, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		at, err := time.ParseInLocation("2006/01/02 15:04:05.999999999", fields[0]+" "+fields[1], time.Local)
		if err != nil {
			continue
		}
		if at.Before(opened.Add(-time.Second)) || at.After(end.Add(time.Second)) {
			continue
		}
		kept = append(kept, line)
	}
	result := correlateOutbound(strings.Join(kept, "\n"), local, target)
	result["time_correlated"] = true
	return result
}

func (d *DiagnosticsRuntime) routeCheck(ctx context.Context, target string) (interface{}, error) {
	if d.cfg.TargetSOCKSAddr == "" {
		return map[string]interface{}{"state": "unverified", "reason": "target SOCKS5 not configured; default is WARP, not Xray routing"}, nil
	}
	host, portRaw, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return nil, errors.New("route target requires domain:port")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid target port")
	}
	logPath, logErr := runningXrayLog()
	var offset int64
	if logErr == nil {
		if info, e := os.Stat(logPath); e == nil {
			offset = info.Size()
		}
	}
	start := time.Now()
	conn, err := reverseexit.DiagnosticTarget(ctx, d.cfg.Config, host, port, false)
	if err != nil {
		return nil, err
	}
	local := conn.LocalAddr().String()
	proxy := conn.RemoteAddr().String()
	_ = conn.Close()
	result := map[string]interface{}{"target": target, "local": local, "proxy": proxy, "socks_connect_ms": float64(time.Since(start)) / float64(time.Millisecond), "public_exit_ip": nil, "public_exit_note": "A connection to this domain does not reveal its observed source IP; only a cooperating IP-echo endpoint can do that."}
	if logErr != nil {
		result["routing"] = map[string]interface{}{"state": "unverified", "reason": logErr.Error()}
		return result, nil
	}
	deadline := time.NewTimer(1500 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			result["routing"] = map[string]interface{}{"state": "unverified", "reason": "no matching Xray access record within 1.5s"}
			return result, nil
		case <-ticker.C:
			f, err := os.Open(logPath)
			if err != nil {
				return result, err
			}
			info, _ := f.Stat()
			if info != nil && info.Size() < offset {
				offset = 0
			}
			_, _ = f.Seek(offset, 0)
			raw, _ := io.ReadAll(io.LimitReader(f, 128<<10))
			_ = f.Close()
			route := correlateOutbound(string(raw), local, target)
			if route["verified"] == true {
				result["routing"] = route
				return result, nil
			}
		}
	}
}
func (d *DiagnosticsRuntime) targetBenchmark(ctx context.Context, s rsxdiag.TaskSpec) (interface{}, error) {
	parsed, err := url.Parse(s.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("target benchmark requires an explicit HTTPS URL without credentials")
	}
	start := time.Now()
	first := time.Duration(0)
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second, TLSHandshakeTimeout: 10 * time.Second, DialContext: func(c context.Context, network, address string) (net.Conn, error) {
		host, rawPort, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			return nil, err
		}
		return reverseexit.DiagnosticTarget(c, d.cfg.Config, host, port, s.UseWARP)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { first = time.Since(start) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return map[string]interface{}{"http_status": response.StatusCode}, errors.New("target returned non-2xx response")
	}
	dataStart := time.Now()
	hash := sha256.New()
	n, err := io.CopyN(hash, response.Body, s.Bytes)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	elapsed := time.Since(start)
	dataTime := time.Since(dataStart)
	result := map[string]interface{}{"state": "ok", "http_status": response.StatusCode, "bytes": n, "requested_bytes": s.Bytes, "first_ms": float64(first) / float64(time.Millisecond), "total_ms": float64(elapsed) / float64(time.Millisecond), "bytes_per_second": float64(n) / elapsed.Seconds(), "data_phase_bytes_per_second": float64(n) / dataTime.Seconds(), "sha256": hex.EncodeToString(hash.Sum(nil)), "proxy": d.cfg.TargetSOCKSAddr, "use_warp": s.UseWARP, "truncated_at_user_limit": n == s.Bytes, "note": "This test measures the VPS final egress, not the mainland-to-VPS tunnel. Target bytes remain encrypted HTTPS until the test client."}
	if err != nil {
		result["state"] = "failed"
	}
	return result, err
}

func (d *DiagnosticsRuntime) captureReport(ctx context.Context, id string, seconds int, progress func(rsxdiag.Step)) (interface{}, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	local, localErr := d.localClient().Do(callCtx, "POST", "/v1/capture", map[string]interface{}{"seconds": seconds}, 1<<20)
	cancel()
	progress(probeStep("local_capture_start", local, localErr))
	peer := d.manager.DiagnosticPeer()
	var remote interface{}
	var remoteErr error
	if peer == nil {
		remoteErr = errors.New("Windows diagnostics unavailable")
	} else {
		c, done := context.WithTimeout(ctx, 8*time.Second)
		remote, remoteErr = peer.Call(c, "capture_start", map[string]interface{}{"seconds": seconds})
		done()
	}
	progress(probeStep("node_capture_start", remote, remoteErr))
	if localErr != nil && remoteErr != nil {
		return nil, errors.New("neither endpoint could start capture")
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	// Stop and export with a bounded cleanup context even after cancellation.
	cleanup, done := context.WithTimeout(d.ctx, 3*time.Minute)
	defer done()
	c, stop := context.WithTimeout(cleanup, 5*time.Second)
	_, _ = d.localClient().Do(c, "DELETE", "/v1/capture", nil, 1<<20)
	stop()
	if peer != nil {
		c, stop = context.WithTimeout(cleanup, 8*time.Second)
		_, _ = peer.Call(c, "capture_stop", nil)
		stop()
	}
	return d.makeReport(cleanup, id, progress, true)
}
func probeStep(name string, value interface{}, err error) rsxdiag.Step {
	r := rsxdiag.Step{Name: name, State: "ok", Evidence: value}
	if err != nil {
		r.State = "unavailable"
		r.Error = err.Error()
	}
	return r
}
func (d *DiagnosticsRuntime) makeReport(ctx context.Context, id string, progress func(rsxdiag.Step), includeCapture bool) (interface{}, error) {
	files := map[string][]byte{}
	missing := []string{}
	snapshot := d.Snapshot()
	files["snapshot.json"] = rsxdiag.CloneJSON(snapshot)
	files["events.json"] = rsxdiag.CloneJSON(d.alerts.Snapshot())
	files["tasks.json"] = rsxdiag.CloneJSON(d.runner.Snapshot())
	// Original frpc restart logs are included only if already captured by the
	// existing feature; no arbitrary filesystem or process memory harvesting.
	if logs, enabled := d.frpc.Logs(); enabled && len(logs) > 0 {
		files["frpc-restart.log"] = []byte(logs)
	} else {
		missing = append(missing, "frpc restart log capture was not enabled")
	}
	if path, err := runningXrayLog(); err == nil {
		if raw, err := tailFile(path, 256<<10); err == nil {
			files["xray-access.log"] = raw
		} else {
			missing = append(missing, err.Error())
		}
	} else {
		missing = append(missing, "Xray access log: "+err.Error())
	}
	if includeCapture {
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		raw, err := d.localClient().Do(c, "GET", "/v1/capture/data", nil, 48<<20)
		cancel()
		progress(probeStep("local_capture_export", map[string]interface{}{"bytes": len(raw)}, err))
		if err != nil {
			missing = append(missing, "local qlog: "+err.Error())
		} else {
			var capture struct {
				Status json.RawMessage   `json:"status"`
				Qlogs  map[string]string `json:"qlogs"`
			}
			if json.Unmarshal(raw, &capture) == nil {
				files["local-capture.json"] = capture.Status
				if reason := captureIncomplete(capture.Status); reason != "" {
					missing = append(missing, "local capture: "+reason)
				}
				if len(capture.Qlogs) == 0 {
					missing = append(missing, "local capture contains no QUIC events")
				}
				for name, data := range capture.Qlogs {
					safe := strings.NewReplacer(":", "_", "/", "_", "\\", "_").Replace(name)
					files["local-"+safe+".sqlog"] = []byte(data)
				}
			}
		}
	}
	peer := d.manager.DiagnosticPeer()
	if peer == nil {
		missing = append(missing, "Windows diagnostic peer unavailable")
	} else {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := peer.Call(c, "logs", nil)
		cancel()
		if err == nil {
			files["python-logs.json"] = raw
		} else {
			missing = append(missing, "Python logs: "+err.Error())
		}
		if includeCapture {
			archive, err := downloadNodeCapture(ctx, peer)
			progress(probeStep("node_capture_export", map[string]interface{}{"bytes": len(archive)}, err))
			if err == nil {
				files["node-capture.zip"] = archive
				if reason := nodeCaptureIncomplete(archive); reason != "" {
					missing = append(missing, "node capture: "+reason)
				}
			} else {
				missing = append(missing, "node capture: "+err.Error())
			}
		}
	}
	summary := map[string]interface{}{"schema": rsxdiag.Schema, "task_id": id, "time": time.Now().UTC(), "unredacted": true, "missing": missing, "observations": snapshot, "notes": []string{"Packet-loss declarations and repeated stream offsets have different denominators.", "No absence of errors is claimed where samples are missing.", "Raw reports preserve original IP/domain/tuple identifiers. Secret files and business payloads are not collected.", "Full pre-capture QUIC handshake data is unavailable when a capture starts mid-connection."}}
	summary["retention_scope"] = map[string]interface{}{"agent_sessions": 256, "python_sessions": 128, "python_log_lines": 256, "frpc_restart_log_max_bytes": 65536, "xray_access_tail_max_bytes": 262144, "capture_max_bytes_per_endpoint": 16777216}
	return rsxdiag.WriteReport(d.root, id, files, summary, missing)
}
func captureIncomplete(raw []byte) string {
	var status struct {
		Complete bool   `json:"complete"`
		State    string `json:"state"`
		Bytes    int    `json:"bytes"`
		Dropped  uint64 `json:"dropped_events"`
	}
	if json.Unmarshal(raw, &status) != nil || status.State == "" {
		return "capture status unavailable"
	}
	if !status.Complete || status.Dropped > 0 {
		return "capture incomplete or events dropped: " + status.State
	}
	if status.Bytes == 0 {
		return "no events recorded"
	}
	return ""
}
func nodeCaptureIncomplete(raw []byte) string {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "invalid node ZIP"
	}
	for _, f := range archive.File {
		if f.Name == "capture.json" {
			r, err := f.Open()
			if err != nil {
				return "missing capture status"
			}
			data, err := io.ReadAll(io.LimitReader(r, 65536))
			_ = r.Close()
			if err != nil {
				return "unreadable capture status"
			}
			return captureIncomplete(data)
		}
	}
	return "missing capture status"
}
func downloadNodeCapture(ctx context.Context, peer *reverseexit.DiagnosticPeer) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := peer.Call(c, "capture_manifest", nil)
	cancel()
	if err != nil {
		return nil, err
	}
	var manifest struct {
		ID     string `json:"id"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Bytes < 1 || manifest.Bytes > 24<<20 {
		return nil, errors.New("invalid node capture manifest")
	}
	buf := make([]byte, 0, manifest.Bytes)
	for len(buf) < manifest.Bytes {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		raw, err := peer.Call(c, "capture_chunk", map[string]interface{}{"id": manifest.ID, "offset": len(buf)})
		cancel()
		if err != nil {
			return nil, err
		}
		var chunk struct {
			Offset int    `json:"offset"`
			Data   string `json:"data"`
		}
		if json.Unmarshal(raw, &chunk) != nil || chunk.Offset != len(buf) {
			return nil, errors.New("invalid capture chunk")
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || len(data) == 0 || len(data) > 32768 || len(buf)+len(data) > manifest.Bytes {
			return nil, errors.New("invalid capture chunk size")
		}
		buf = append(buf, data...)
	}
	hash := sha256.Sum256(buf)
	if hex.EncodeToString(hash[:]) != manifest.SHA256 {
		return nil, errors.New("node capture checksum mismatch")
	}
	return buf, nil
}
func (d *DiagnosticsRuntime) TraceSession(ctx context.Context, key string) (interface{}, error) {
	if key == "" || len(key) > 96 {
		return nil, errors.New("invalid session key")
	}
	meter := d.manager.Metrics()
	if meter == nil {
		return nil, errors.New("Agent metrics unavailable")
	}
	var found *rsxdiag.Session
	for _, s := range meter.Snapshot(256).Sessions {
		if s.Key == key {
			copy := s
			found = &copy
			break
		}
	}
	result := map[string]interface{}{"key": key, "agent": found, "node": nil, "xray": map[string]interface{}{"state": "unverified"}}
	if found != nil && found.Local != "" && !found.TupleAt.IsZero() {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		link, err := d.localClient().Lookup(queryCtx, found.Local, found.Remote, found.TupleAt.Format(time.RFC3339Nano))
		cancel()
		if err == nil {
			result["agent_quic_link"] = link
		} else {
			result["agent_quic_error"] = err.Error()
		}
	}
	peer := d.manager.DiagnosticPeer()
	if peer != nil {
		for offset := 0; offset < 128; offset += 16 {
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			raw, err := peer.Call(c, "sessions", map[string]interface{}{"offset": offset})
			cancel()
			if err != nil {
				result["node_error"] = err.Error()
				break
			}
			var page struct {
				Sessions []map[string]interface{} `json:"sessions"`
				Next     *int                     `json:"next"`
			}
			if json.Unmarshal(raw, &page) != nil {
				break
			}
			for _, s := range page.Sessions {
				if text(s, "key") == key {
					result["node"] = s
					if local, remote := text(s, "local"), text(s, "remote"); local != "" {
						c, cancel := context.WithTimeout(ctx, 3*time.Second)
						link, err := peer.Call(c, "lookup", map[string]interface{}{"local": local, "remote": remote, "at": text(s, "tuple_at")})
						cancel()
						if err == nil {
							result["node_quic_link"] = link
						}
					}
					offset = 128
					break
				}
			}
			if page.Next == nil {
				break
			}
		}
	}
	if found != nil && found.TargetLocal != "" {
		if path, err := runningXrayLog(); err == nil {
			if raw, err := tailFile(path, 256<<10); err == nil {
				result["xray"] = correlateOutboundWithin(string(raw), found.TargetLocal, found.Target, found.Opened, found.Closed)
			}
		}
	}
	return result, nil
}
func (d *DiagnosticsRuntime) Root() string { return filepath.Clean(d.root) }
