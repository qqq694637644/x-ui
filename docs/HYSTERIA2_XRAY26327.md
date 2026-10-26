# Xray 26.3.27 原生 Reverse Bridge：IPv6 / Hysteria2 / QUIC Brutal

## 正确的连接方向

**境外 VPS 主动连接境内 Windows，境内通过本机 SOCKS5 发起业务请求，真正目标网站由境外出口访问。**

```text
境内 Windows
  应用/浏览器 --> 127.0.0.1:1080 SOCKS5
                       |
                 Xray reverse.portals
                       |
                 Hysteria2 UDP 入站
                       ^          IPv6 QUIC/TLS/force-brutal
                       |          由 VPS WARP SOCKS5 的 UDP ASSOCIATE 承载
                       |
 境外 VPS        Xray reverse.bridges
                       |
                 Xray Hysteria2 出站 --> WARP SOCKS5 --> 境内 DDNS AAAA / UDP 18443
                       |
                 真实出口（独立、可选）
                      / \
                  freedom  本机 SOCKS5（例如 127.0.0.1:40001）
                      \   /
                     目标互联网服务
```

这里有两个必须分开的网络路径：**隧道传输本身必须经由现有 WARP SOCKS5 出站连接境内 IPv6**；真正访问目标网站的 VPS 出站则可单独选择 freedom 或本机 SOCKS5。QUIC 不能裸连境内 IPv6。隧道 WARP SOCKS5 必须支持 UDP ASSOCIATE 与 IPv6，否则无法承载 QUIC。

不需要 FRPC、FRPS、STUN、XTCP、RSX3 Python 和自定义 QUIC 协议。原有 `/xui/reverse-socks` 旧 FRP / WARP 页面、设置和进程代码完整保留，两种方案可以独立配置。**避免同时让新旧 Windows SOCKS5 进程监听同一个 1080 端口。**

## 境内 Windows：reverse_node

项目：<https://github.com/qqq694637644/reverse_node>

- 使用 Xray-core 26.3.27 的 `reverse.portals` 和 `protocol: hysteria` 入站。
- 本机 SOCKS5 `127.0.0.1:1080` 支持 TCP CONNECT、UDP ASSOCIATE。
- 用 DDNS AAAA 指向 Windows 实际公网 IPv6，在 Windows 防火墙和光猫的 IPv6 入站策略允许 UDP 18443。
- 用指定 `--xray-path` 启动 Xray；默认生成 TLS 自签名证书、随机 auth、SHA-256 pin。
- 境内主动连接 VPS 的普通 Hysteria2 客户端配置**不是此项目需要的方向**。

例子：

```powershell
./reverse_node.exe init --xray-path "C:/xray/xray.exe" --public-host "home.example.net" --port 18443 --brutal-up 16mbps --brutal-down 24mbps
./reverse_node.exe run --xray-path "C:/xray/xray.exe"
```

然后查看生成的 `overseas-bridge.fragment.json`，复制到 x-ui 的新页面。不要把认证密钥、证书私钥提交到公开仓库。

## 境外 VPS：x-ui 新界面

访问 **`/xui/reverse-quic`**（独立于旧 FRP 页面）。

设置项：

| 项目 | 含义 |
| --- | --- |
| 启用 Bridge | 仅开启后才为已有 Xray 添加 reverse Bridge、Hysteria2 出站及出口 |
| 境内 DDNS / IPv6 和 UDP 端口 | VPS 主动连接的位置，默认 UDP 18443 |
| 私有 reverse 域名 | 两端必须一致，默认 `reverse-node.internal`；这是 Xray 内部路由键，不需实际 DNS |
| Hysteria2 auth | 与 reverse_node 一致，至少 32 字符 |
| TLS ServerName 和证书 pin | 不跳过验证；Windows 默认自签名证书必须提供 64 位 SHA-256 pin |
| 隧道传输 WARP SOCKS5 出站 Tag | 默认 warp；必须指向已有 protocol=socks 的 WARP 出站 |
| VPS Brutal 上传 / 下载 | 按 VPS 自己的发送/接收视角，默认分别 24mbps、16mbps |
| 真实出口 | 选择 VPS freedom 直连，或本机 SOCKS5 |
| 本机 SOCKS5 | 例如 `127.0.0.1:40001`，支持可选用户名和密码，仅允许回环地址 |

可以把境内生成的整段 `overseas-bridge.fragment.json` 复制到页面底部进行一次本地解析，自动填充 DDNS、端口、认证、证书 pin、私有域名、Brutal 速率。**导入不更改已经选择的真实出口，保存并应用后才会更改 Xray 运行配置。**

### Xray 内核真正生成什么

境外生成以下配置，合并到已有 Xray 进程而不是替换整份模板：

- `reverse.bridges`：Xray 内建反向控制面，域名与境内 Portal 一致。
- 传输出站 `reverse-node-hysteria`：`protocol: hysteria`，`network: hysteria`、TLS/h3、`finalmask.quicParams.congestion: force-brutal`，并通过 `streamSettings.sockopt.dialerProxy` 强制链到现有 WARP SOCKS5 出站 Tag（默认 `warp`）。目标域名使用 `UseIPv6`，隧道 UDP/QUIC 本身不裸连。
- WARP SOCKS5 传输代理必须支持 SOCKS5 `UDP ASSOCIATE`，因为 Hysteria2 的 QUIC 是 UDP。若 WARP 本机代理仅支持 TCP CONNECT，不能承载本方案的 QUIC；必须先确认所用 WARP SOCKS5 实现的 UDP 能力，不能假设普通 SOCKS5 一定支持。
- 真实出口 `reverse-node-egress`：选直连时协议为 `freedom`，选本机 SOCKS5 时协议为 `socks`。
- 两条优先路由：**反向注册私有域名 → Hysteria2 出站**；**Bridge 反向业务入站 → 指定真实出口**。这些规则优先于已有的泛用阻断和直连规则，确保本机 SOCKS5 不会因旧规则失效而被静默绕过。

其他已有出站、路由、入站、reverse.portals 都保持原样。遇到保留 tag 或 reverse 域名冲突时拒绝应用，而不是覆盖旧配置。x-ui 重启 Xray 失败时回滚保存的 Bridge 设置并保留旧运行配置。

### Brutal 强制行为

新页面**只支持 Hysteria2 / QUIC Brutal**，不提供 TCP、VLESS、XTCP、CUBIC、BBR 等备用传输选择，且不会静默降级。如果配置缺少 `force-brutal`、速率、TLS 或 auth，原生 Xray 26.3.27 会在应用前拒绝配置。

境内 reverse_node 上传 16 Mbps、下载 24 Mbps 时，境外 VPS 的反向发送方向与之相反，因此 VPS `brutalUp=24mbps`、`brutalDown=16mbps`。

## 验证

### x-ui 状态面板：自动检测与异常告警

在 /xui/reverse-quic 顶部显示专用状态卡片，默认每 **5 秒**刷新：
- VPS 管理的 Xray 进程状态。
- WARP SOCKS5 传输：检查本机 WARP SOCKS5 的实际 UDP ASSOCIATE 握手，不只是探测 TCP 端口。
- 可选真实出口 SOCKS5：实际握手是否接受；freedom 仅表示配置，不代表目标网站可达。
- 最近的 Hysteria2 出站业务流量与样本 Mbps：从原有 Xray 26.3.27 StatsService **只读**获取出站字节计数，不重置统计；不是全链路压力测速结果。
- Xray、WARP SOCKS5 UDP 或真实出口 SOCKS5 明确失败时弹红色告警。仅有流量长时间不变则显示“待确认”，因为无法区分空闲和 Portal 掉线。

**重要边界：** Xray 26.3.27 的 StatsService 不能直接返回 reverse.bridges 当前成功注册的 Worker 数量。WARP UDP 握手成功或 Xray 进程运行不等于境内 Portal 已上线，真实连通性需要境内 Windows 的 SOCKS5 发起请求测试。

### Windows 1081：连通性、测速和掉线检测

按照当前的 --socks-port 1081，保持 Windows reverse_node.exe run 的窗口运行，另开 PowerShell：

```powershell
curl.exe -v --proxy socks5h://127.0.0.1:1081 --connect-timeout 10 --max-time 30 https://www.cloudflare.com/cdn-cgi/trace
curl.exe -L --proxy socks5h://127.0.0.1:1081 --connect-timeout 10 --max-time 120 -o NUL -sS -w "HTTP %{http_code} | %{size_download} B | %{time_total}s | %{speed_download} B/s" "https://speed.cloudflare.com/__down?bytes=20000000"
```

curl 的 speed_download 是 B/s，Mbps = B/s × 8 / 1,000,000。40 Mbps 的理论带宽约为 5 MB/s；真实速度还取决于 WARP、VPS 出口、服务器等因素。

可使用本仓库 [Windows PowerShell 测试脚本](../scripts/windows_reverse_test.ps1) 自动计算 Mbps、持续检测掉线，先下载该脚本至 Windows：

```powershell
Invoke-WebRequest -Uri "https://raw.githubusercontent.com/qqq694637644/x-ui/gpt/xray26327-native-hysteria2/scripts/windows_reverse_test.ps1" -OutFile ".\windows_reverse_test.ps1"
./windows_reverse_test.ps1 -Mode Probe -SocksPort 1081
./windows_reverse_test.ps1 -Mode Speed -SocksPort 1081 -SizeMB 20
./windows_reverse_test.ps1 -Mode Watch -SocksPort 1081 -IntervalSeconds 15
```

Watch 每 15 秒通过整个反向路径请求 HTTPS，失败显示红色 [DOWN]、恢复显示 [RECOVERED]。x-ui 的自动轮询和 Windows 端到端检测相互补充。测速结果属于完整链路，不等于 QUIC Brutal 单独的理论吞吐。

```bash
go test ./web/service -run TestReverseQUIC -count=1
go run ./cmd/reverse-quic-fixture --profile profile.json --output bridge.json
/path/to/xray run -test -c bridge.json
```

Linux CI 另外拉取固定的 `reverse_node` 源码和固定提交的 Xray 26.3.27，运行 `scripts/smoke_reverse_quic_bridge.py`：

1. 启动境内 Portal + 本机 SOCKS5。
2. 用 x-ui 的生产配置生成器启动境外 Bridge。
3. 在 VPS 启动本机 WARP SOCKS5（支持 UDP ASSOCIATE）和独立的最终出口 SOCKS5。
4. 验证 QUIC/UDP 确实经 WARP SOCKS5，再验证 TCP HTTP、SOCKS5 UDP ASSOCIATE 反向到境外并经所选最终出口访问目标。
5. 关闭传输 WARP SOCKS5，验证隧道请求失败；恢复 WARP 后验证重连。
6. 关闭最终出口 SOCKS5，验证请求失败且**不直连兜底**。
7. 检查双方 Xray 日志中真实 Brutal 激活。

本机/CI 环回成功不代表跨境真实网络或运营商过滤一定正常；公网联调仍需实际 DDNS、IPv6 入站防火墙及 VPS 环境验证。
