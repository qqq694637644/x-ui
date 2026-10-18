# MyTRN v3 — fixed localhost UDP / hot NAT endpoint (destructive)

This is a **single-Xray, no-frp** deployment. The A Python agent and its CF/VLESS/XHTTP authenticated control plane remain unchanged. MyTRN's old B Xray public IP:port outbound, `mytrn-warp` SOCKS outbound and `dialerProxy` settings have been **deleted**, not retained as a fallback.

## Network

```text
Windows A: local SOCKS5 10808 → A Xray VLESS reverse/Hysteria UDP :40001
                           ↕ Python STUN + opaque UDP router-forward :39999
                           ↕ A current NAT public IP:port (changes at runtime)
                           ↕ WARP network
VPS B: WARP local SOCKS5 :40000 UDP ASSOCIATE (no direct fallback)
                           ↕ Go MyTRN relay owned by x-ui process
                           ↕ 127.0.0.1:41001/udp (loopback only)
        B single Xray Hysteria client → VLESS reverse-in → existing freedom
```

The only remote destination in the generated Xray outbound is **127.0.0.1:41001**. The B Go relay alone sends UDP frames to the **local SOCKS5 UDP BND address**, with the authenticated, current A public IP:port carried inside the SOCKS5 frame. The Go relay never opens any native UDP socket to A; its own UDP sockets bind **127.0.0.1** only. A failed WARP, missing SOCKS5 association, unregistered endpoint, malformed response or unexpected non-loopback BND **drops data** instead of falling back to native VPS traffic.

**Multiple QUIC sockets:** different Xray UDP source ports must not share a SOCKS5 UDP ASSOCIATE, since a SOCKS5 reply only identifies the *A destination*, not which Xray local socket originated the packet. The single fixed listener demultiplexes by the local Xray source address and maintains one SOCKS5 TCP/UDP association per client. Associations are bounded to 64 clients and retired after three minutes of inactivity. This is **not** 64 extra independent NAT mappings or 64 persistent Hy2 channels.

## Lifecycle

- On x-ui startup, the MyTRN Go relay binds loopback 41001 (only if MyTRN is enabled); an independent goroutine negotiates WARP SOCKS5 UDP ASSOCIATE and reconnects on failure. The existing Xray process starts normally regardless of MyTRN reachability.
- The first authenticated A certificate/endpoint registration requests **one** x-ui-managed Xray restart to install its reverse outbound. The Xray destination is always localhost.
- Later A public IP/port changes update the Go relay's current SOCKS5 destination header and database. **No Xray restart or configuration changes**. Existing QUIC sessions *may* need to reconnect after a real NAT/path change; the mapping update itself never restarts unrelated VLESS inbounds.
- Changing the B WARP SOCKS5 loopback IP/port invalidates the old SOCKS5 association, drops packets while the new association is negotiated, and does not restart Xray.
- Disabling MyTRN stops the Go relay and removes the managed reverse outbound on the next legitimate Xray configuration application. Trust reset clears the relay target immediately; A must re-register a trusted TLS certificate.
- Changing B Hy2 Brutal rates, VLESS UUID, derived auth/control token, trust/certificate or enable state really changes Xray and still uses the existing managed restart flow. There is no alternate old protocol route.

Go relay status is shown inside the existing MyTRN x-ui inbound UI. `applied` means the process has a matching static Xray outbound **and** every known active local Xray UDP client has a WARP SOCKS association; it **does not** prove that remote A or a website is reachable. `relay_listening` means the fixed listener is up but no Xray QUIC packet has started an association yet. `warp_degraded` indicates only some clients have an association. `warp_unavailable` and `relay_unavailable` are failures, not acceptable green statuses.

## Configuration

- Fixed VPS local UDP: **127.0.0.1:41001**. Do not open this on a public firewall or configure a second Xray inbound on this UDP port.
- Mandatory WARP SOCKS5: usually **127.0.0.1:40000**. Must be a real IPv4 loopback socket. WARP SOCKS5 UDP replies with an IPv4 loopback UDP BND, or a zero BND replaced by the validated local proxy host; arbitrary public/non-loopback BND is rejected.
- A Windows remains **router-forwarded UDP 39999**, Python gateway forwarding to A Xray **127.0.0.1:40001**. There is no Windows config migration for this update.
- Credential derivation: same MyTRN shared control token (HMAC-SHA256 derived Hy2 auth), unchanged TLS pinning and ALPN h3; no second credential or protocol compatibility mode.
- User websites still exit via B's previously configured `freedom` route; WARP is mandatory only for B→A Hysteria/QUIC transport.

**Upgrade:** backup the x-ui database and pinned A certificate; deploy the updated x-ui binary from the branch containing this change; restart x-ui once to initialize the new UDP relay and static local Xray outbound. No need to replace a running Windows A Hy2 node. Never keep a separate old `mytrn-warp` Xray outbound as a fallback.

## Verification

- Go `go test ./mytrnrelay` exercises an actual local SOCKS5 UDP ASSOCIATE fake, same UDP Xray socket across A endpoint changes, **two independent concurrent Xray UDP client sockets and correct reply demultiplexing**, WARP proxy re-association, fail-close when the SOCKS5 server disappears, and rejection of public BND/listen addresses.
- `go test ./web/service -run 'TestMyTRN'` validates the regenerated single-Xray JSON, absence of the old SOCKS outbound/`dialerProxy`, unchanged Xray configuration when the A NAT IP/port changes, first-registration restart and no restart on later mapping or WARP proxy changes.
- With `XUI_HEALTHCHECK_XRAY_BIN` set to Xray v26.3.27, the MyTRN fixture is checked with `xray run -test`.
- Live acceptance additionally requires actual Windows SOCKS5 HTTP requests after STUN endpoint changes, WARP downtime, and x-ui current unrelated VLESS inbounds continuing without restart. Local mocks and CI do not certify real cross-border throughput or zero packet loss.
