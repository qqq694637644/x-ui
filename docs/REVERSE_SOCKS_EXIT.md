# RSX2 反向 SOCKS5 / WARP（仅两端 frpc 定制）

本仓库 x-ui 负责 **境外 VPS**。境外 Go Agent 通过本地 frpc XTCP Visitor TCP 端口 **主动连接境内** Python 3.13 SOCKS5 服务；境内 frpc 仍为 XTCP Proxy。第三方 frps 不需任何改动。

要求两端使用 [定制 frpc QUIC DATAGRAM](https://github.com/qqq694637644/frp/pull/1)。Go Agent 与 [nat_node](https://github.com/qqq694637644/nat_node/pull/1) 使用 **RSX2**，故意不兼容旧 RSX1，无 UDP-over-TCP 兜底。

## TCP / UDP

- TCP：境内 SOCKS5 CONNECT -> Python RSX2 TCP 多路复用 -> frpc XTCP QUIC Stream -> Go Agent -> **WARP SOCKS5 CONNECT** -> 目标。
- UDP：境内 SOCKS5 UDP ASSOCIATE -> Python UDP 127.0.0.1:9314 -> frpc QUIC DATAGRAM -> Go UDP 127.0.0.1:6002 -> **WARP SOCKS5 UDP ASSOCIATE** -> 目标。每个 UDP 会话独立保留 WARP TCP 控制连接。
- WARP SOCKS5 只能配置 127.0.0.1:PORT；不可用时返回失败。Go Agent 没有直连目标的代码路径，UDP 也不会退回 XTCP TCP 字节流。
- UDP 封装为 4 字节 Session ID + 标准 SOCKS5 UDP 报文，整个 DATAGRAM 最大 1100 字节，超限丢弃、不重传；不支持任意尺寸的 UDP 或 HTTP/3 初始 QUIC 报文。
- **TCP 视频长连接已改成逐会话 32 帧信用窗口**：只有接收端完成写入，才批量（每 8 帧）返还发送额度；正常慢客户端不再因为队列满就断流。
- WARP SOCKS5 UDP ASSOCIATE 返回的 relay 地址必须是 IPv4 本地回环地址；非本地地址会被拒绝，防止 UDP 暴露 VPS 的公网源地址。

## x-ui Web 配置

登录面板，左侧打开 **反向 SOCKS5 / WARP**，可配置：

- XTCP Visitor TCP：127.0.0.1:6000
- Go UDP 本地绑定（frpc datagram.peerAddr）：127.0.0.1:6002
- frpc UDP 本地接收（frpc datagram.listenAddr）：127.0.0.1:6001
- WARP SOCKS5 地址（必填、本地回环）：127.0.0.1:40000，用户名/密码可选
- RSX2 共享密钥（至少 16 字符，与境内 REVERSE_SOCKS_TOKEN 一致）
- 启用/停止、保存并立即应用、独立的 XTCP/RSX2 状态与 WARP CONNECT/UDP ASSOCIATE 探测状态（每 30 秒）。健康探测并不能证明最终 WARP 公网 IP。
- 新增 **境外 frpc 默认配置（隐藏原始 IP 模式）** 生成器：同时填写本地 WARP SOCKS5 控制连接代理及 SOCKS5 UDP 打洞代理，默认 `wireProtocol=v1`（官方 frps 0.69.0 测试通过）、`minRetryInterval=10`、`maxRetriesAnHour=120`、`disableAssistedAddrs=true`。模板中的 frps Token / XTCP secret 只在浏览器当前页面填写，不持久化。

面板将配置持久化在本机 SQLite 中，启动时恢复启用状态。GET 配置接口仅返回是否已设置密钥和 WARP 密码，绝不返回它们的原文。请妥善保护本机 SQLite 和备份。

## 命令行（不用面板）

```sh
go build -o reverse-socks-exit ./cmd/reverse-socks-exit
export REVERSE_SOCKS_TOKEN='same-strong-rsx2-token-as-nat-node'
./reverse-socks-exit -visitor 127.0.0.1:6000 \
  -udp-bind 127.0.0.1:6002 -udp-frpc 127.0.0.1:6001 \
  -warp-socks 127.0.0.1:40000
```

如果 WARP SOCKS5 需要认证，增加 -warp-user USER，并将 WARP_SOCKS_PASSWORD 作为环境变量提供（禁止在命令行上泄露密码）。

相同选项也支持主程序：

```sh
./x-ui reverse-socks-exit -visitor 127.0.0.1:6000 \
  -udp-bind 127.0.0.1:6002 -udp-frpc 127.0.0.1:6001 \
  -warp-socks 127.0.0.1:40000
```

如需自定义定期 WARP 出口连通探测目标，两种 CLI 都支持 `-warp-probe 1.1.1.1:443`，面板也有对应输入框。探测分别尝试真实的 SOCKS5 CONNECT 和 UDP ASSOCIATE，不会使用 VPS 直连，不能代替出口 IP 核验。

## 境外 frpc 配置（既有第三方 frps 保持不变）

**仅配置 `transport.proxyURL` 不够。** XTCP 默认 STUN/打洞仍使用原生 UDP Socket；为阻止泄漏，必须使用本 fork 的 `natTraversal.udpProxyURL` 将 STUN、打洞及 QUIC 改为本地 WARP SOCKS5 UDP ASSOCIATE。两端代理字段必须指向同一个回环 SOCKS5 端口，否则新 frpc 拒绝启动对应 Visitor。以下段落为境外独立 `frpc.toml` 示例：

```toml
serverAddr = "REPLACE_WITH_FRPS_IP"
serverPort = 7000
auth.method = "token"
auth.token = "REPLACE_WITH_FRPS_TOKEN"
transport.protocol = "tcp"
transport.wireProtocol = "v1"
transport.proxyURL = "socks5://127.0.0.1:40000"
natHoleSTUNServer = "REPLACE_WITH_VERIFIED_STUN_IP:3478"

[[visitors]]
name = "rsx2_outside"
type = "xtcp"
serverName = "rsx2_node"
secretKey = "same-xtcp-secret-as-mainland"
protocol = "quic"
keepTunnelOpen = true
minRetryInterval = 10
maxRetriesAnHour = 120
bindAddr = "127.0.0.1"
bindPort = 6000

[visitors.datagram]
listenAddr = "127.0.0.1:6001"
peerAddr = "127.0.0.1:6002"

[visitors.natTraversal]
disableAssistedAddrs = true
udpProxyURL = "socks5://127.0.0.1:40000"
```

优先使用**数字 STUN IP:端口**，避免 STUN 域名在本机 DNS 解析时额外走原始网络。SOCKS5 UDP 中继无法控制 IP TTL，也无法在复杂 NAT 下创建多个原生端口，这两种特殊打洞优化会降级或拒绝，绝不直连回退。真实 WARP+ 是否容许 P2P 入站仍需测试。

境内 frpc、Python 端口和部署说明见 [nat_node README](https://github.com/qqq694637644/nat_node/blob/gpt/xtcp-reverse-socks5/README.md)。

## 测试

```sh
go test ./reverseexit
go build -o reverse-socks-exit ./cmd/reverse-socks-exit
```

另在 nat_node 仓库设置 REVERSE_SOCKS_EXIT_BIN 指向刚编译的 Go Agent，执行 python3.13 -m unittest -v tests.test_end_to_end。测试覆盖慢速视频 TCP 背压、浏览器暂停读取、WARP TCP/UDP 和断线恢复。frp fork 另有官方 frps 0.69.0 + 本地模拟 WARP SOCKS5 的 XTCP TCP/UDP 实际 P2P 测试。真实跨境性能、第三方 frps/WARP+ 真实网络打洞与 VPS 出口仍需实地验证。
