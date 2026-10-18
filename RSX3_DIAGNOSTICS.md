# RSX3 Diagnostics: four-stage implementation

This feature extends the existing `/xui/reverse-socks` page. The existing frpc PID, RSX3 connection, WARP handshake, enable/disable, and restart-log controls remain unchanged. Diagnostics does not replace the data protocol, NAT punching, fixed UDP 39999, Brutal pacing or Xray domain routing.

## Deployment

Upgrade the **three matching PR builds**: both custom frpc executables, this x-ui panel/Agent, and the companion Python `nat_node` source. Official frps **0.69.0 remains unmodified**.

1. Stop the mainland Python process and start the new checkout with the existing RSX3 token and `--diagnostics`. Keep the existing SOCKS/MUX/DATAGRAM ports:

   ```powershell
   python -m reverse_socks --protocol rsx3 --diagnostics `
     --frpc-diagnostics 127.0.0.1:9320 `
     --socks 127.0.0.1:1080 --mux 127.0.0.1:9312 `
     --udp-bind 127.0.0.1:9314 --udp-frpc 127.0.0.1:9313
   ```

2. Generate the mainland frpc diagnostic configuration with the **same environment token**:

   ```powershell
   python -m reverse_socks --print-diagnostics-config --frpc-diagnostics 127.0.0.1:9320
   ```

   This prints `diagnostics.enabled`, `diagnostics.listenAddr`, and a domain-separated HMAC `diagnostics.token`. Put the three values at the TOML root **before** `[[proxies]]`. Restart mainland frpc. Do not change `proxies.natTraversal.bindPort = 39999`. The derived token is a real credential: protect the configuration file.

3. In x-ui's existing reverse SOCKS page, enable **RSX3 Diagnostics** and keep offshore frpc diagnostics at `127.0.0.1:9321`. Save and apply. The panel generates the offshore token from the same RSX3 secret. Initial configuration application follows the existing frpc/Agent restart behavior. Later diagnostic tests/captures and the pause button do **not** restart forwarding.

4. No firewall ports need to be opened. Both frpc diagnostic APIs bind literal IPv4 loopback and require a Bearer token. The Windows diagnostic peer is negotiated using RSX3 PING/PONG, then authenticated on a separate XTCP stream. Diagnostics enabled now **requires the matching Python, Agent and frpc diagnostic versions**: until authenticated, new business TCP/UDP sessions are refused; capability/peer handshake timeout closes the RSX3 control connection within 12 seconds with an explicit version error. There is no old-node compatibility forwarding mode or RSX2 protocol path. Disabling diagnostics sampling remains supported and does not change RSX3 protocol.

Diagnostics defaults **off**. Runtime collection can be paused without changing transport configuration. To remove the APIs entirely, disable panel diagnostics and remove the three mainland frpc fields, then restart those processes during a maintenance window.

## What is implemented

### 1. Live telemetry and recent history

- Both frpc endpoints: actual QUIC packet bytes/counts, loss declarations, spurious loss, RTT/minimum/variation, PTO count, congestion window, in-flight bytes, UDP drop reasons, and connection lifecycle.
- Brutal: actual controller snapshots (target/max, computed pacing, raw ACK sample ratio, compensation-clamped ratio, window and burst). These are not mislabelled as observed network throughput.
- STREAM retransmitted bytes count **repeated offset ranges**, not lost packets. State is bounded; after coverage limits are reached the metric becomes unavailable, not a false zero.
- Go/Python: live TCP/UDP sessions, connection attempts/results, per-operation close/error, bytes read versus forwarded, wait counters, slow writes, latency samples and identities.
- QUIC samples every second, Windows summaries every two seconds, chart points every five seconds. All waits and I/O are outside forwarding except bounded counter updates.
- Sixty-minute memory ring; persistent UTC minute aggregates in a private history directory. Null/stale data is never replaced with healthy zeroes. One process/connection counter reset does not produce a negative rate.

### 2. Self-test and three isolated benchmarks

The self-test checks managed process state, generated WARP-only transport configuration, WARP CONNECT, a **real** SOCKS5 UDP DNS response, frps control status, QUIC state, authenticated node RPC, QUIC DATAGRAM echo, RSX3 TCP/UDP, and target SOCKS5 handshakes.

- **A / raw QUIC:** authenticated, bounded synthetic data over independent QUIC unidirectional streams. It does not involve Xray or the website. SHA-256 verifies received bytes. Directions shown in x-ui are relative to Windows.
- **B / RSX3 SOCKS5:** starts at the actual Windows SOCKS5 listener, crosses RSX3, and reaches a random one-time-authorized synthetic Go endpoint. It deliberately excludes Xray/WARP final website performance. Unknown reserved targets cannot open sockets or bypass normal routing.
- **C / target egress:** HTTPS download through the configured local target SOCKS5 (or explicitly selected WARP SOCKS5). It tests the VPS final egress, not the cross-border tunnel. Redirects are not silently followed; body reads are capped.
- Sizes: 1, 10 or 50 MB. One heavy task at a time, with timeout and cancellation. There is no automatic background throughput test.
- Xray route verification uses **fresh access-log evidence**, exact socket tuple and destination, with session-time filtering for historical traces. It reports an actual outbound tag only if evidence is unambiguous. The exact routing-rule index is not exposed by ordinary access logs and is explicitly unverified.
- A separate HTTPS IP-echo check accepts plain IP or JSON `ip`. Its observed source IP applies only to that particular echo hostname. It cannot certify an unrelated domain's egress.

### 3. Correlation and unredacted reports

- RSX3 epoch + SID identifies a session. frpc maps actual local TCP socket tuples to QUIC original destination CID and Stream ID. Observed tuple timestamps permit safe closed-session lookup; reused tuples without matching time bounds remain unlinked.
- Native qlog event capture on **already-running** connections: 30/60/300 seconds, memory bounded to 16 MiB per endpoint. Packet callbacks never write files or block waiting for the export. Contention, size limits and absent events are recorded as incomplete coverage. Existing native `QLOGDIR` output remains separate.
- Mid-connection recording cannot recreate earlier handshake events. The capture manifest explicitly says so.
- HTML reports are offline and escaped. ZIP reports include JSON snapshots, event/task evidence, recorded frpc logs, bounded Xray access-log tails, and both endpoint qlogs when available. Each source's absence/incompleteness is declared.
- Reports are **not redacted**: original IP addresses, domains, ports, IDs and existing log text remain. Diagnostics does not scrape unrelated credential files or capture business payloads. No report is uploaded automatically.
- Private output directory, bounded report/history retention, file-path checks, authenticated download/delete endpoints and SHA-256 manifests.

### 4. Alerts and longer-term stability

- Thresholds/hold time/recovery hysteresis configurable in the existing page.
- Default rules: XTCP down, repeated WARP failures, high RTT/loss, TCP establishment failure rate (minimum sample count), unexpected frpc failure, frequent reconnects, stale node metrics and repeated target-SOCKS failure.
- A sustained fault produces one active alert and one recovery event, not a notification per poll. Browser notifications are optional and require user permission; they work while the dashboard is open, not as a background external notification service.
- 24h/7d/30d history uses separate bounded JSONL minute aggregates, not x-ui's configuration database. Default retention 30 days, budget 128 MiB; reports default budget 256 MiB.
- Availability uses **each metric's observed time** as denominator and separately shows collector and per-metric coverage. Five-second, minute and long-term aggregates preserve `value_seconds`; a minute with only one known WARP sample never counts as sixty seconds of measured uptime. Panel downtime, pause intervals and missing data are not silently counted as uptime.
- Long-term latency quantiles merge fixed histogram buckets; they are labelled upper-bound estimates. No averages of P95 values are presented as a global P95.

## Metric interpretation

- Go/Python byte counters mean bytes accepted by the next local socket, not bytes parsed by a browser or an HTTP body count.
- QUIC wire counters exclude outer UDP/IP, SOCKS5 and WARP tunnel framing.
- Loss declarations can arrive after the packet's send window; 10s/60s ratios are descriptive counter deltas, not a certified end-to-end datagram loss probability. Spurious loss is separate.
- The authenticated DATAGRAM echo reports actual sent/unique replies and receive timestamps. Its RTT does not include time spent waiting in the diagnostic result queue.
- The existing WARP green badge still means handshake checks. Diagnostics provides the stronger payload round-trip test separately.
- Generic TCP reset is not automatically a transport outage. The first error's direction/operation is recorded; a secondary cancellation is not treated as an independent root cause.

## Private API surface

All panel endpoints inherit the existing authenticated `/xui/reverse-socks` route group:

- POST `diagnostics/snapshot`, `history`, `settings`
- POST `diagnostics/task`, `cancel`, `trace`
- POST `diagnostics/reports`, `delete-report`
- GET `diagnostics/download/:id/:format`

The local frpc API is `/v1/snapshot`, `/lookup`, `/benchmark`, `/udp-probe`, `/cancel-test`, `/capture` and `/capture/data`. It cannot run arbitrary commands or fetch arbitrary URLs. Never expose it with a reverse proxy.

## Verification and limits

Tests cover native QUIC packet events, byte/offset accounting, HMAC and bounded input rejection, two real QUIC endpoints, actual Go/Python capability negotiation, **strict rejection of unsupported diagnostic peers**, true TCP/UDP payloads, qlog generation, fixed-port-frpc regression, bounded histories/reports, settings persistence, alert deduplication, raw-report HTML escaping, authentication inheritance and dashboard null/gap semantics.

Review fixes: target HTTPS benchmarks only pass if the **full requested byte count** arrives; a clean but premature EOF is a failed test. Task states distinguish `completed_passed` from `completed_failed` and `cancelled`; HTTP 200 from the diagnostic API does not establish success. Short history (5/15/60 min) reads both persisted UTC minute aggregates and live in-memory samples after restart. Independent Python and Go TCP failure rates are aggregated without conflating attempts. The dashboard labels collector coverage separately from the valid coverage of each metric. The ZIP `manifest.json` records stable member SHA-256 digests; the final archive SHA-256 and byte count live in the post-write `<id>.json` sidecar because a ZIP cannot embed its own final digest.

The full integration fixture uses unmodified upstream frps 0.69.0, two custom frpc processes, Python3.13 and the Go Agent with a local fake WARP. No CI test claims production cross-border bandwidth or a completed seven-day soak. After deployment, use the dashboard's coverage and stability history for that real-world acceptance step.

The diagnostics workflow pins companion commits, rather than depending on PR branches that can be deleted after merge. The tested pair is frp `3b7aeb62777bbd898ffd0720a1a672ca3a7e1293` and nat_node `04245af4c41810efe19eddb7805b2b4cc2c67c53`. Each build's `diagnostics-versions.txt` records the actual three repository revisions alongside the binary checksums.
