# RSX3 Go Exit Agent / WARP / Xray

**只支持 RSX3。** 境内 Windows 的 SOCKS5 `127.0.0.1:1080` 与独立 QUIC Stream、DATAGRAM 经过定制 XTCP frpc 连接到 VPS Go Agent。旧版控制流复用协议代码与选择已移除；旧部署需要同时更新 Windows Python、两端定制 frpc、x-ui/Go Agent。

x-ui「反向 SOCKS5」提供管理 frpc 和 Go Agent 的启停、参数、PID、WARP 检查、Xray 目标 SOCKS5 与 Diagnostics。
- **frpc 控制、STUN、打洞、QUIC** 必须强制 WARP SOCKS5 `127.0.0.1:40000`，拒绝原生回退；frps v0.69.0 原样保留。
- **最终网站 TCP/UDP** 可经 Xray SOCKS5 `127.0.0.1:40001` 分流到 `warp` 或 `freedom`，由用户的规则决定。Xray 不可用时不自动直连或绕过。
- **Brutal** 双端可独立设置 `targetMbps/maxMbps`，境内 frpc 配置需手动维护。
- **Diagnostics** 为可选运行功能，启用时必须完成配套版本/能力认证，不允许不兼容节点继续透明转发。frpc 诊断端口限定回环（Windows 9320，VPS 9321），并且需要认证。诊断报告可按用户要求保持原文、不脱敏、不上传。

参考：
- [完整 RSX3 分流配置](../RSX3_XRAY_ROUTING.md)
- [四阶段 Diagnostics 部署与测量口径](../RSX3_DIAGNOSTICS.md)
- [境内节点使用说明](https://github.com/qqq694637644/nat_node/blob/gpt/rsx3-diagnostics/README.md)

以前保留的另一套控制复用协议已经停止支持。不修改官方 frps；启用和关闭诊断功能仅改变诊断能力，不改变 RSX3 业务协议。
