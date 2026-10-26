# IPv6 DDNS Direct QUIC / RSX3

本功能独立于旧的 `/xui/reverse-socks` FRPC/FRPS/XTCP/WARP 路径。

## 架构

```text
Windows Python nat_node SOCKS5 1080
    -> IPv6 QUIC UDP 8080 + TLS pin + RSX3 HMAC
    <- VPS x-ui /xui/reverse-socks-ipv6
           -> 原生 VPS TCP/UDP，或单独的 Xray SOCKS5
```

没有 FRP、STUN、XTCP 或隧道用 WARP；`reverseexit` 复用现有 RSX3 的每连接独立 Stream、
会话控制和 UDP DATAGRAM。Linux 启动和重连时解析 DDNS AAAA，每 60 秒检查该记录是否仍包含已连接的 IPv6，
不包含则重连。DDNS 域名记录的更新由路由器或独立 DDNS 客户端完成，不要求固定中国电信 IPv6 前缀。

## 操作

1. 为 Windows 全球 IPv6 设置 DDNS AAAA，确保域名在前缀变动后自动更新。
2. 在 [Windows Python 对端](https://github.com/qqq694637644/nat_node) 安装并运行配套的
   `--direct-quic` 模式，使用一个 TLS 证书，其 DNS SAN 包含该 DDNS 域名。
3. 在 `/xui/reverse-socks-ipv6` 输入 DDNS 主机名、QUIC UDP 端口、**证书 DER 的 SHA-256 指纹**、
   与 Windows 一致的 RSX3 共享 Token。
4. 明确选择目标出口：`native` 使用 VPS 原生 TCP/UDP；`socks5` 使用回环地址的 Xray SOCKS5
   （比如 `127.0.0.1:40001`）。不会在故障时自动切换出口。
5. 启用并保存。此时管理的是第二套独立运行实例，不触碰旧配置键 `reverseSocksConfig`
   和它管理的 FRPC 进程。

必须能从 VPS 通过 IPv6 UDP 访问 Windows Listener；不需要 IPv4 DMZ。只开放必要的 IPv6 UDP
端口，Windows SOCKS5 1080 不应对公网开放。若旧 FRP Python 同时运行，请使用不同的
Windows 本机 SOCKS5 端口，避免监听端口冲突。

新模式保持单一已认证控制连接、QUIC TLS、证书指纹 + DNS SAN + 有效期验证，
以及原本的 RSX3 HMAC 控制/数据流认证。证书替换时应同时更新面板指纹。
端点只能通过 AAAA 解析；未配置或解析失败会进入错误/重连状态。

当前本模式不支持旧版 XTCP 专用 Diagnostics、Brutal 拥塞调参，不能将旧模式的诊断数据误当作新模式数据。
旧功能未被删除，关闭此页面即可回到原来的 FRP/XTCP/WARP 部署。
