package service

import (
	"context"
	"time"

	"x-ui/reverseexit"
	"x-ui/rsxdiag"
)

type targetHealth struct {
	Checked      time.Time `json:"checked"`
	State        string    `json:"state"`
	Error        string    `json:"error,omitempty"`
	Failures     int       `json:"consecutive_failures"`
	TCPConnect   bool      `json:"tcp_connect"`
	UDPAssociate bool      `json:"udp_associate"`
	// A background ASSOCIATE health check deliberately does not imply a
	// verified UDP data round-trip; active self-test performs that separately.
	UDPRoundtripVerified bool `json:"udp_roundtrip_verified"`
}

func (d *DiagnosticsRuntime) collectTargetHealth() {
	defer d.wg.Done()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
		}
		if !d.Settings().Paused {
			h := targetHealth{Checked: time.Now().UTC(), State: "ready"}
			ctx, cancel := context.WithTimeout(d.ctx, 8*time.Second)
			_, err := reverseexit.DiagnosticConnect(ctx, d.cfg.Config, d.cfg.WARPProbe, false)
			if err == nil {
				h.TCPConnect = true
				control, sock, _, udpErr := reverseexit.DiagnosticUDP(ctx, d.cfg.Config, false)
				if udpErr == nil {
					h.UDPAssociate = true
					_ = control.Close()
					_ = sock.Close()
				} else {
					err = udpErr
				}
			}
			cancel()
			if err != nil {
				h.State = "unavailable"
				h.Error = err.Error()
			}
			d.mu.Lock()
			if err != nil {
				h.Failures = d.targetHealth.Failures + 1
			}
			d.targetHealth = h
			d.mu.Unlock()
		}
		timer.Reset(30 * time.Second)
	}
}

// Ratios are computed from interval counter deltas. Averages of prior ratios
// with different packet counts are not presented as measured loss probability.
func (d *DiagnosticsRuntime) addQualityWindows(p *rsxdiag.Point) {
	d.quality = append(d.quality, *p)
	cut := p.Time.Add(-60 * time.Second)
	i := 0
	for i < len(d.quality) && (d.quality[i].Time.Before(cut) || len(d.quality)-i > 70) {
		i++
	}
	if i > 0 {
		d.quality = append([]rsxdiag.Point(nil), d.quality[i:]...)
	}
	for _, window := range []struct {
		name    string
		seconds int
	}{{"10s", 10}, {"60s", 60}} {
		start := p.Time.Add(-time.Duration(window.seconds) * time.Second)
		for _, prefix := range []string{"quic_", "node_"} {
			var sent, lost, stream, retrans float64
			hasRetrans := false
			for _, sample := range d.quality {
				if sample.Time.Before(start) {
					continue
				}
				sent += sample.Totals[prefix+"sent_packets"]
				lost += sample.Totals[prefix+"loss_declarations"]
				stream += sample.Totals[prefix+"stream_sent_bytes"]
				if v, ok := sample.Totals[prefix+"stream_retransmitted_bytes"]; ok {
					hasRetrans = true
					retrans += v
				}
			}
			if sent >= 10 {
				ratio := lost / sent * 100
				p.Values[prefix+"loss_pct_"+window.name] = fptr(ratio)
				if window.name == "60s" {
					p.Values[prefix+"loss_pct"] = fptr(ratio)
				}
			}
			if hasRetrans && stream > 0 {
				p.Values[prefix+"stream_retransmit_pct_"+window.name] = fptr(retrans / stream * 100)
			}
		}
	}
}
