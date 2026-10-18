# RSX3 Go Exit Agent -> Xray SOCKS5 分流（TCP + UDP）

## 目标与边界

本配置区分 **frpc 的跨境隧道** 与 **RSX3 最终网站出口**：

| 组件 | SOCKS5 代理 | 路径 |
| --- | --- | --- |
| 境外 frpc -> frps、STUN、XTCP QUIC 和 DATAGRAM | `warpAddr = 127.0.0.1:40000` | **必须经过 WARP**，不可使用 VPS 原生公网 IP |
| Go Exit Agent 访问最终目标 TCP / UDP | `targetSocksAddr = 127.0.0.1:40001` | 先进入 Xray，由其 `routing.rules` 选择 `warp` / `freedom` / `blocked` |
| Xray `warp` 出站 | `127.0.0.1:40000` | 目标网站看到 WARP 出口 |
| Xray `freedom` 出站 | 服务器原生出口 | 目标网站可能看到 VPS 真实 IP，**这是用户自主选择的分流行为** |

两类出口独立，**不要**把 x-ui 面板的 `WARP SOCKS5 地址` 直接改成 Xray 的 `40001`。这样会把 frpc 的控制连接、STUN 和 QUIC 也交给 Xray 的 `freedom` 规则，违反 frps 通信必须经过 WARP 的要求。

## 1. 在 x-ui「入站列表」新建 Xray SOCKS5

- 协议：`socks`；监听 IP：`127.0.0.1`；端口：`40001`。
- 「密码认证」可关闭（仅监听回环地址）；「启用 udp」开启，UDP IP 填 `127.0.0.1`。
- 请确保最终运行的 Xray 进程已经加载此入站，且端口不与任何服务冲突。不要打开公网防火墙的 40001。

已有 Xray 配置模板中 `warp` 出站仍指向 `127.0.0.1:40000`，`freedom` 仍为默认出站。保留现有 `routing.rules`：按顺序检查域名、IP 和拒绝规则。**不要添加 RSX3 入站的强制 WARP 兜底规则**，否则未匹配的网站无法使用 VPS 的自由出站。

## 2. 在 x-ui「反向 SOCKS5 / WARP 出口」设置 Go Agent

- **frpc 专用 WARP SOCKS5 地址：`127.0.0.1:40000`**（不变）。
- **Go Agent 最终目标 SOCKS5：`127.0.0.1:40001`**（新增）。
- 若 Xray 入站无认证，目标 SOCKS5 用户名/密码留空；若设置密码认证，在此单独填写，不会覆写 WARP 凭据。
- 保存并应用后，Go Exit Agent 的 TCP SOCKS5 CONNECT 和 UDP ASSOCIATE 都使用 Xray `40001`。frpc.toml 仍独立生成 WARP `40000`，原有 Brutal 目标速率、固定 UDP 39999、RSX3 端口和协议保持不变。
- 若 Go Agent 新目标地址空白，会自动采用**旧版默认全走 WARP**，与以前相同。这是未配置时的默认选择，而不是故障时的回退；显式配置 Xray 端口但 Xray 不可用，则返回目标访问失败，不会通过 WARP 或原生 VPS 绕行。

单独运行 Go Agent 的用户可使用：

```bash
REVERSE_SOCKS_TOKEN='<与 Windows Python 相同的共享密钥>' \
./reverse-socks-exit -protocol rsx3 \
  -visitor 127.0.0.1:6000 -udp-bind 127.0.0.1:6002 -udp-frpc 127.0.0.1:6001 \
  -warp-socks 127.0.0.1:40000 -target-socks 127.0.0.1:40001
```

如 Xray SOCKS5 需密码，增加 `-target-socks-user <user>` 并在环境变量 `RSX_TARGET_SOCKS_PASSWORD` 设置密码。不要把这些明文秘密发布到日志/工单。不要在 x-ui 内管理 Agent 时另起同端口独立 Agent。

## 3. 验证分流

先在 VPS 上直接验证 Xray 入口：

```bash
curl --socks5-hostname 127.0.0.1:40001 -sS --max-time 20 https://api.ipify.org
```

如果 `api.ipify.org` **未**匹配任何 WARP 域名规则，则应该是 VPS 出口。临时为 `api.ipify.org` 增加 WARP 规则并重新加载 Xray，第二次则应该为 WARP 出口。完成验证后按需要恢复规则。

在 Windows 通过 RSX3 SOCKS5 `127.0.0.1:1080` 重复域名测试；这样也会走同一套 Xray 规则。**使用 `--socks5-hostname` 保留目标域名**，避免 Windows 提前 DNS 解析成 IP，导致 Xray 无法匹配部分域名/geosite 规则。IP 目标由 Xray `ip` 规则处理。HTTP/HTTPS 的域名会由 SOCKS5 传递给 Xray，不需要单独在 Go Agent 维护域名列表。

要确认 frpc 的公网 IP 没有泄露，需单独检查 frpc 的 `transport.proxyURL` 和 `visitors.natTraversal.udpProxyURL` 仍是 `socks5://127.0.0.1:40000`，`disableAssistedAddrs=true`，并用 VPS 出站抓包核查 P2P 的 UDP 仍未从服务器原生公网发出。Xray 的 `freedom` 目标流量**允许**从 VPS 原生公网发出，与 frpc 隧道保密要求并不冲突。

## 回滚

- Go Agent 的 `targetSocksAddr` 留空并保存，自动恢复旧版全部应用目标走 WARP 的行为。
- 切回后无需修改 frps、Windows Python、境内 frpc 或 Brutal 参数。
