# RSX2 反向 SOCKS5 / WARP（仅两端 frpc 定制）

本仓库 x-ui 负责 **境外 VPS**。境外 Go Agent 通过本地 frpc XTCP Visitor TCP 端口 **主动连接境内** Python 3.13 SOCKS5 服务；境内 frpc 仍为 XTCP Proxy。第三方 frps 不需任何改动。

要求两端使用 [定制 frpc QUIC DATAGRAM](https://github.com/qqq694637644/frp/pull/1)。Go Agent 与 [nat_node](https://github.com/qqq694637644/nat_node/pull/1) 使用 **RSX2**，故意不兼容旧 RSX1，无 UDP-over-TCP 兜底。

## TCP / UDP

- TCP：境内 SOCKS5 CONNECT -> Python RSX2 TCP 多路复用 -> frpc XTCP QUIC Stream -> Go Agent -> **WARP SOCKS5 CONNECT** -> 目标。
- UDP：境内 SOCKS5 UDP ASSOCIATE -> Python UDP 127.0.0.1:9314 -> frpc QUIC DATAGRAM -> Go UDP 127.0.0.1:6002 -> **WARP SOCKS5 UDP ASSOCIATE** -> 目标。每个 UDP 会话独立保留 WARP TCP 控制连接。
- WARP SOCKS5 只能配置 127.0.0.1:PORT；不可用时返回失败。Go Agent 没有直连目标的代码路径，UDP 也不会退回 XTCP TCP 字节流。
- UDP 封装为 4 字节 Session ID + 标准 SOCKS5 UDP 报文，整个 DATAGRAM 最大 1100 字节，超限丢弃、不重传。
- WARP SOCKS5 UDP ASSOCIATE 返回的 relay 地址必须是 IPv4 本地回环地址；非本地地址会被拒绝，防止 UDP 暴露 VPS 的公网源地址。

## x-ui Web 配置

登录面板，左侧打开 **反向 SOCKS5 / WARP**，可配置：

- XTCP Visitor TCP：127.0.0.1:6000
- Go UDP 本地绑定（frpc datagram.peerAddr）：127.0.0.1:6002
- frpc UDP 本地接收（frpc datagram.listenAddr）：127.0.0.1:6001
- WARP SOCKS5 地址（必填、本地回环）：127.0.0.1:40000，用户名/密码可选
- RSX2 共享密钥（至少 16 字符，与境内 REVERSE_SOCKS_TOKEN 一致）
- 启用/停止、保存并立即应用、实时连接状态与错误提示

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

## 境外 frpc 配置（既有第三方 frps 保持不变）

```toml
[[visitors]]
name = "rsx2_outside"
type = "xtcp"
serverName = "rsx2_node"
secretKey = "same-xtcp-secret-as-mainland"
protocol = "quic"
keepTunnelOpen = true
bindAddr = "127.0.0.1"
bindPort = 6000

[visitors.datagram]
listenAddr = "127.0.0.1:6001"
peerAddr = "127.0.0.1:6002"
```

境内 frpc、Python 端口和部署说明见 [nat_node README](https://github.com/qqq694637644/nat_node/blob/gpt/xtcp-reverse-socks5/README.md)。

## 测试

```sh
go test ./reverseexit
go build -o reverse-socks-exit ./cmd/reverse-socks-exit
```

另在 nat_node 仓库设置 REVERSE_SOCKS_EXIT_BIN 指向刚编译的 Go Agent，执行 python3.13 -m unittest -v tests.test_end_to_end。测试使用本地模拟 WARP SOCKS5 和模拟 frpc DATAGRAM，验证 UDP 真正经 SOCKS5 ASSOCIATE 而不是直连，并测试 WARP 中断的失败保护。真实跨境性能、第三方 frps/WARP 打洞路径仍需部署验证。
