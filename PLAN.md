# MyTRN 接入 x-ui 的 A/B 整体架构与实施计划

> **状态：待审查，尚未实施。** 基线：`qqq694637644/x-ui` 的 `main`，提交 `ca2d037d4dc88a9195444d4c8db6ebf70bf6146f`（2026-10-10）；数据内核固定为 **Xray-core v26.3.27**。本文件只规定即将实施的目标架构，不表示 x-ui 已具备 MyTRN 功能。
>
> **已定方案**：个人使用、单 A / 单 B；**B 复用 x-ui 的 Go 后端、现有入站列表、同一个 Xray 进程和同一份 `bin/config.json`**；A 公网映射确实变化时，调用 x-ui **现有的 Xray 重启路径**。**不用 Xray 运行时增删出站 API，不做第二个 B Xray 进程，不新建 MyTRN 独立管理页面，不写兜底协议或旧实现兼容层。**

## 1. 目标、事实与方向

唯一业务目标：**中国境内 Windows A 的浏览器/应用经境外 Linux VPS B 访问外网，以绕过 GFW 限制**。B 公网 IP 已被墙，A 不能依赖直接连接 B 的公网 IP；B 使用现有 **WARP 本地 SOCKS5 UDP** 主动连接 A 的运营商 NAT 公网映射。

必须区分两条方向：

- **连接建立方向**：`B Xray → B WARP SOCKS5 UDP → A 经 STUN 得到的公网 IP:PORT → A UDP 39999`。
- **业务请求方向**：`A 应用 → v2rayN → A Xray 本地 SOCKS5 → VLESS reverse-out → 已建立的 mKCP/TLS 连接 → B Xray reverse-in → freedom → 境外网站`。网站响应从原路返回。

**已有真实验证**：在 A/B 实机使用 Xray 26.3.27 和 Python PoC，B 经 WARP SOCKS5 UDP 成功连接 A；A 对 `ifconfig.me:443` 的 SOCKS5 CONNECT 经 B 的 `reverse-in -> b-internet`，B 的 `freedom` 实际打开网站 TCP 连接。Python Agent 的本机集成测试另验证了注册、映射变化和重新拨号；**它的完整自动化在真实 A/B 上也已由用户报告成功**。不要退回 Python QUIC、手写 KCP 或 B→A 内网服务转发的错误目标。

## 2. 不动的既有设施

- **B x-ui**：已有两个启用的 VLESS 入站，截图中的 `26417` 和 `16360`；继续作为原有业务使用，尤其 `26417` 承担 **A→B 控制链的过墙代理通道**。
- **Cloudflare CDN + Caddy + VLESS `26417`**：现有正常运行的过墙设施。**无需为 MyTRN 增加 Caddy path、CF DNS 规则、专用域名或改动 `26417` 的入站传输。** 控制请求只是经已有 v2rayN/CF/VLESS 链路去访问 B 的 Go HTTP 控制服务的 **IP:PORT**。
- **B WARP**：保持现有本地 SOCKS5 `127.0.0.1:40000`，用于 **B→A mKCP UDP 数据包**，不是网站 TCP 流量的默认出口。
- **A 光猫/路由器**：保留 `UDP 39999 → A 固定内网 IP:39999`，不暴露 A Xray 的本机 `40001`。
- **x-ui 其他入站、订阅/PublicEndpoint、已有 Tunnel、Caddy 管理和面板功能**：MyTRN 不接管、不迁移、不改变已有数据定义；特别注意已有 PublicEndpoint 描述的是客户端连接 B 的公网域名，不是 A 的 NAT 映射。

现有成功链路优先不动。**不新增第三台服务器、不让 A 主动拨被墙的 B 公网 IP，不将用户网站流量绕回 CF 控制面。**

## 3. 控制面与数据面：两条逻辑链路

### 3.1 控制面（只发送 A 的公网映射和必要认证）

```text
中国 A（Go Agent）
  A UDP 39999 ── STUN ──> 发现真实 NAT IP:PORT
        │
        └── POST {"ip":"x.x.x.x", "port":57197}
              │
              ▼
        A v2rayN SOCKS5（现有代理）
              │ 使用原有 CF/VLESS 出站规则
              ▼
          Cloudflare CDN
              │
              ▼
          B 现有 VLESS :26417
              │
              ▼
        B x-ui 内置 Go 控制 API（IP:PORT，例如 :18080）
              │ 认证并记录新映射
              ▼
        x-ui MyTRN 状态 → Xray 配置生成 / 按需重启
```

`26417` **不是控制服务本身**，而是 A 到 B 控制服务的过墙传输。HTTP POST 的最终目标是 B Go 控制服务的地址/端口；Caddy/CF 不解析或转发这个 MyTRN API 的专用 URL。

控制服务由 **x-ui Go 进程**提供，而非新 Python 服务或新 Xray 入站。建议监听 B `127.0.0.1:18080`，**前提是当前 VLESS 代理路径确实能把经代理的请求送至 B 回环服务**；如果现有 Xray 路由阻止 `127.0.0.1`/私网目的地址，实施时应使用现网已有的可达 B 目标地址并限制 TCP 端口的外部访问，**先实测，不假设 loopback 一定可达，也不改变 Caddy/26417**。无论监听方式，必须进行 token 鉴权，不默认暴露无认证控制 API。

### 3.2 数据面（Xray 原生 mKCP/VLESS reverse）

```text
A 浏览器/应用
      │
      ▼
A v2rayN 本地 SOCKS5 / 路由入口
      │ 用户流量：交给 A Xray 本地 SOCKS5
      ▼
A Xray SOCKS5 127.0.0.1:10808
      │ VLESS reverse-out
      ▼
A Xray VLESS/mKCP/TLS 127.0.0.1:40001（UDP）
      ▲
      │ A Go UDP Gateway（不透明数据报）
      ▼
A UDP :39999 ── 家用路由器映射 ── 运营商 NAT 公网 IP:PORT
      ▲                                       │
      │                                       ▼
      └────────────── B WARP SOCKS5 UDP 127.0.0.1:40000
                                              ▲
                                              │
                              B x-ui 管理的 Xray mKCP 出站
                                              │
                                   VLESS reverse-in
                                              │
                                      B freedom
                                              │
                                         境外互联网
```

**数据面的连接由 B 主动发起；代理上网请求由 A 发起。** 双向字节流、TCP 管理、mKCP 重传、VLESS 认证和 TLS 加密**全部由 Xray-core v26.3.27 完成**。A Go 只负责 UDP/STUN 网关、上报、Xray 编排；B Go 只负责配置、控制 API、重启和状态。

### 3.3 A 的 v2rayN 路由隔离

v2rayN 是 A 本机现有 SOCKS5/路由入口，不是 A 与 B 间新的专用隧道：

- **用户流量**应转给 A Xray 的 `127.0.0.1:10808`（SOCKS5）；最终从 B `freedom` 访问互联网。
- **MyTRN 控制请求**必须固定经过原有 CF/VLESS 出站到 B Go 控制 IP:PORT，**不能**交给 A 的 `10808` 业务代理，否则数据面断开时控制面也随之断开。
- 通过 v2rayN **已有明确路由规则/入站标记**实现区分；不能假定仅凭目标地址自动分流正确。上线前单独验证控制请求在 B 的数据面 Xray 停止时仍可抵达控制 API。
- 网站目标域名建议由 B 侧解析（SOCKS5 以域名发送，使用 `socks5h` 验收），避免用户请求在 A 本地 DNS 泄漏。

## 4. B 直接复用 x-ui：一个 Xray 进程，一份 JSON

源码基线已核对：

- `web/service/xray.go` 的 `GetXrayConfig()` 先读取 Xray 模板、合并启用的 `Inbound`，再调用 `TunnelService.ApplyToXrayConfig()`；生成**同一个** `xray.Config`。
- `xray/config.go` 已有 `inbounds`、`outbounds`、`routing` 等字段；`xray/process.go` 将它们写入 **`bin/config.json`**，启动**一个** Xray 进程。`go.mod` 里的旧 Xray Go 库依赖版本**不能代表**运行时二进制版本，部署前需核对实际 `bin/xray-linux-amd64 version` 为 v26.3.27。
- `XrayService.RestartXray(false)` 已实现：生成配置、`ValidateConfig` 调用 Xray `run -test`、配置未变化则不重启；需要变化时停止旧进程并启动新进程。启动失败时现有实现尝试恢复上次配置；这是 x-ui 原有的基本事务性保障，**不为 MyTRN 增加额外兜底通道/多套协议兼容**。
- `web/controller/inbound.go` 已有修改后 `SetToNeedRestart()` 和每约 10 秒检查重启标志的流程。MyTRN endpoint 变更**复用现有 Xray 配置/重启序列化路径**，不自己 `kill`/`exec` Xray。

**新增配置合并点**：在 `GetXrayConfig()` 中调用 `MyTRNService.ApplyToXrayConfig(xrayConfig)`（或等价的现有组合模式），将下面的 B 配置片段合并入原有的 `outbounds`/`routing`，**不能覆盖**模板出站、`26417`/`16360` 入站或既有 Tunnel 生成结果。

MyTRN 在 B 不监听新端口，所以**不生成一个假的 Xray `inbound`**。Xray 的 VLESS `reverse-in` 是反向机制创建的内部接入 tag，不是需要公网监听的入站配置。

### 4.1 B 生成的 Xray 组件（固定 tag）

| tag | Xray 角色 | 固定行为 |
| --- | --- | --- |
| `mytrn-reverse-dial` | VLESS **outbound** | 目标是 A 最新经 STUN 确认的 `IP:PORT`；`settings.reverse.tag = mytrn-reverse-in` |
| `mytrn-warp-socks5` | SOCKS5 **outbound** | `127.0.0.1:40000`，通过 UDP ASSOCIATE 携带 mKCP；供 `dialerProxy` 使用 |
| `mytrn-internet` | `freedom` **outbound** | B 正常互联网出口；不是默认通过 WARP 出网站流量 |
| `mytrn-reverse-in` | VLESS **内部反向入站 tag** | 将 A 请求交给 B 的 `mytrn-internet`；由 Xray VLESS reverse 创建 |

`mytrn-reverse-dial` 固定：`streamSettings.network=kcp`、`security=tls`、与实测 PoC 一致的 mKCP 参数（起步 `mtu=1200`）、`sockopt.dialerProxy=mytrn-warp-socks5`。B 必须用已可信的 A 证书和稳定 `serverName` 验证 TLS：`allowInsecure=false`；自签证书在 v26.3.27 可按 PoC 使用 `disableSystemRoot=true` + `usage=verify`，**绝不为了连通关闭证书验证**。

配置只允许**1 个 MyTRN 实例**；标签冲突、A endpoint 缺失、证书缺失/过期、UUID 错误、SOCKS5 地址错误时明确报错，不猜默认值、不隐式退化为 B 公网直连。MyTRN 未启用时不输出其业务出站与路由。

路由要求：将 `mytrn-reverse-in` **精确**导向 `mytrn-internet`；不得修改其他真实入站（包括 `26417`）的路由。合并时检查既有 `geoip:private`、黑洞和 Tunnel 规则的相对顺序，避免 MyTRN 规则误吞其他流量或给 A 提供非预期内网访问。保留原有规则优先语义，并用实际规则匹配测试验证。

## 5. UI：仍是现有入站列表，不新增页面

用户打开现有 **“入站列表”** 就能看见一条 `MyTRN（反向上网）` 业务记录，与 VLESS `26417`、`16360` 一起显示，沿用当前列表的编辑、启用/停用、状态展示交互；**不新建侧边栏菜单或独立数据面页面**。

- `协议` 列显示 `MyTRN`；`端口` 列显示 `—（B 主动出站）`，**不伪造一个监听端口**。备注可由用户编辑。
- 操作中允许配置：启用开关、VLESS UUID、A 证书（仅公钥）、稳定 TLS `serverName`、WARP SOCKS5 地址、控制 API token/监听端口；当前 A 公网 `IP:PORT` **只读、由控制面上报维护**。
- 状态至少区分：**未收到 A endpoint / 已收到 endpoint / Xray 配置已应用 / 真实 A→B→外网已验证**。仅 Xray 进程存活不代表 MyTRN 代理已可用。
- MyTRN 不是客户端订阅节点：不生成二维码、分享 URL、公网 PublicEndpoint，也不计入真实“入站数量”或套用普通 Inbound 的流量/到期配额。
- 数据层建议单独建立**单例 MyTRN 配置模型**（UUID/公钥证书路径/WARP/控制 token/endpoint 等），复用现有 GORM/SQLite、service/controller 和同一张入站列表的 UI 渲染。**不把 `protocol=mytrn` 直接写进原 `model.Inbound` 并交给 `GenXrayInboundConfig()`**，因为 Xray 没有名叫 `mytrn` 的入站协议。
- UI 通过类型标记识别业务条目，复用列表组件但走 MyTRN 自己的管理方法；不改普通 VLESS 行的数据格式，不把已有 Tunnel 的 VMess Portal 当成 MyTRN。

## 6. B 控制服务与动态映射更新

### 6.1 API（最小、单一职责）

**x-ui 内置 Go HTTP 服务**提供 `POST /control/mapping`（单 A、单 B）：

```http
POST /control/mapping HTTP/1.1
X-Control-Token: <独立随机密钥>
Content-Type: application/json

{"ip":"119.98.144.218","port":57197}
```

上面的公网地址/端口**只是曾经观测到的示例值**；启动后只使用 A 当时 STUN 实测并注册的 endpoint，绝不硬编码。

- 只允许 IPv4 单播有效端口与**准确的 JSON 字段集合**；拒绝缺字段/未知字段/未授权或超大请求。
- token 与 x-ui 登录账号、VLESS UUID 分离；控制报文不含私钥，服务日志不能打印 token。A 端目标地址必须强制由原有 v2rayN CF/VLESS 代理访问，不得失败后偷偷直连 B 被封 IP。
- A 的证书和 UUID 在**初次配对时明确配置**到 x-ui；A 控制面日常只报新的 IP:PORT，不在线静默更换被 B 信任的 TLS 证书。这比每次重新传证书更小且可审查。
- B 收到有效请求后与当前已保存 endpoint 比较：**相同则只确认请求，不触发重启**；变化则验证并持久化 `ip/port`，用现有 `SetToNeedRestart()`/`RestartXray(false)` 合并重启请求。
- 这里的持久化 endpoint 是 x-ui 生成 `bin/config.json` 的**唯一来源**。x-ui 进程重新启动后仍能读取最新目标，不会回退到最初的 Demo 端口。
- 多次快速上报采用现有重启标志/锁机制合并，以最终确认的 endpoint 为准；避免每个 POST 直接杀进程。
- 当 MyTRN 未配置、未启用、证书错误或 Xray 配置校验失败时返回明确错误；不要输出损坏的 Xray 配置或自动改变路由方向。

### 6.2 A 的 STUN 行为

- A Go 进程独占 UDP `39999`，**同一 socket** 向 STUN 服务器发送请求并收 B 的 mKCP UDP 数据；其内部 Xray 只监听 `127.0.0.1:40001`。
- 每约 20 秒探测；连续 2 次探测结果一致且与已确认映射不同，才认定 IP:PORT 变化。**单次 STUN 超时不清空旧映射，不重启 B**。
- A 启动发现 endpoint 后注册；映射真实变化时立即注册；平时低频刷新即可（起步 30 分钟，按实网需要调整）。B 重启后通过持久化目标自动重新拨号。
- A Gateway 只实现 UDP 数据报分流与双向 peer 映射，**不解码、不修改 mKCP/VLESS，也不实现 TCP 传输**。Windows 保留已经在 Python 版验证过的 UDP ICMP/`WSAECONNRESET` 处理语义。
- STUN 只证明对 STUN 服务器观察到的地址；必须继续用 B 真正经 WARP 到 A 的 mKCP 流量验证该映射可达，不能仅凭 NAT 标签推断。

### 6.3 明确接受重启中断

此次选择**整进程重启**，不研究 Xray 的运行时 `AddOutbound`/`RemoveOutbound`，也不修它们的反向会话生命周期。

一次已确认的 A endpoint 变化会触发：

```text
A 两次 STUN 确认新映射
  → 经原有 CF/VLESS 控制链 POST IP:PORT 到 B Go API
  → B 保存新 endpoint
  → x-ui 生成整个新 Xray JSON、验证
  → x-ui 停止并重启自己管理的唯一 Xray 进程
  → B 再经 WARP SOCKS5 UDP 拨 A 新 endpoint
  → A 的用户代理请求恢复
```

**代价被明确接受**：这次重启会短暂中断 x-ui 当前所有 Xray 入站，包括过墙 VLESS `26417`、`16360` 的会话。x-ui 的 Go HTTP 控制服务和独立 Caddy 服务无需因此重启，但 `26417` 在 Xray 停启期间不可用；A 已通过控制链发送成功的 endpoint 会先持久化。映射变化不频繁，个人使用，**不为消除这几秒中断增加第二个 B Xray 或复杂动态热更**。现有 x-ui 的配置校验及启动失败处理保持不变。

## 7. A 端正式实现范围（Go）

A 独立轻量 Go Agent，只负责：

1. 固定 UDP `39999` 入口、同 socket STUN、透明 UDP peer 转发到 `127.0.0.1:40001`。
2. 经现有 v2rayN SOCKS5/CF/VLESS 将当前公网 IP:PORT 上报 B Go 控制服务。
3. 为本机 Xray v26.3.27 生成/管理已验证的 `socks` + `vless` reverse-out/mKCP/TLS 配置；A 本地 SOCKS5 默认 `127.0.0.1:10808`。
4. 映射确认、错误与健康状态；退出时正常释放 UDP socket 与自己管理的 Xray 进程。

A 的 v2rayN 是**现有的本机用户入口与过墙控制链代理**，不由新 Agent 替换。第一版在现有配置文件/CLI 下运行即可，不为 A/B 同时造两套新 Web UI；B 管理从 x-ui 现有页面完成。

使用 [`qqq694637644/mytrn`](https://github.com/qqq694637644/mytrn) 的已验证 Python 实现和 `poc/xray26327` 作为**测试行为基线**，迁移时仅重写编排逻辑，不保留 Python QUIC 兼容、不重新实现 Xray 的数据协议。B 数据面迁入 x-ui 后，正式部署不再同时启动 B 的 Python Agent/独立 Demo Xray；端口和实例统一由 x-ui 管理。

## 8. 实施顺序（按依赖，从最小正确链路开始）

### 阶段 I：B x-ui 数据面原生化

- 在 B x-ui 新增单实例 MyTRN 配置模型、Go 配置生成器及合并检查；统一生成 `outbounds`/`routing`，**不新增真实 Xray inbound**。
- 在现有“入站列表”中显示 MyTRN 业务行，复用原有组件完成编辑、启停、状态；不用新页面。
- Xray 严格锁定 v26.3.27，配置先校验再应用；用 **B 现有 `127.0.0.1:40000` WARP** 主动拨入 A。
- 初期可以使用已成功的 Python A PoC/Agent 作为**测试端**，确认 B 的 x-ui 单进程配置中，A 的 `curl --proxy socks5h://127.0.0.1:10808 https://ifconfig.me` 仍实际从 B `freedom` 出网，同时 `26417`/`16360` 保持可用。这只是集成测试，不引入正式兼容模式。

### 阶段 II：将 B HTTP 控制面内建到 x-ui

- Go HTTP 注册、token 校验、端点持久化、确认变化才调 `SetToNeedRestart()`；与已有 x-ui 主进程共享服务生命周期。
- 通过已存在的 **A v2rayN → CF CDN → B VLESS `26417`** 发送 IP:PORT；测试 B 数据面中断时 A 控制面依然能发送/排队到控制 API；不为此修改 Caddy/CF 代理配置。
- 模拟 A 公网端口变化，核验 B 单 Xray 重启、新 endpoint 拨号恢复、原有入站重启后的健康状态。

### 阶段 III：A Python 编排迁移 Go

- 重写 A UDP/STUN、控制注册和 Xray 本地配置/进程管理；协议交给同一 Xray 26.3.27。
- 验证 A v2rayN 用户流量交给 A 的数据 SOCKS5、控制请求始终走 CF/VLESS，不发生路由循环。
- 删除生产部署里 B Python Demo/Agent 与独立 Xray 进程的启动路径；不引入旧 JSON 自动兼容。

### 阶段 IV：验收后部署

- 真实 A/B：`B WARP → A STUN endpoint`、VLESS reverse、A 网站请求走 B `freedom`；出口 IP 与 B 一致，DNS 不漏回 A。
- 验证单次 STUN 失败、A 端口变化、B 的 x-ui/Xray 重启、B WARP SOCKS5 重启，记录从发现映射到浏览器恢复可用的时间。
- 验证一次 x-ui 单进程重启确实只造成**约定的短时中断**，不会持久破坏 `26417` 的控制过墙链或 `16360` 现有业务；Caddy/订阅仍按原配置运行。
- 检查配置/日志不泄漏控制 token、VLESS UUID 和 A TLS 私钥；B 只存 A 公钥证书。持续运行并收集实际成功率与资源使用。

## 9. 最小测试矩阵与不能假装通过的项目

| 验收项 | 验证方式 | 通过要求 |
| --- | --- | --- |
| 合并配置 | Go 单测检查包含 VLESS `26417`、`16360`、已有 Tunnel、MyTRN outbounds/routing | 新增功能不覆盖老配置、不造成 tag/路由冲突 |
| Xray 配置有效性 | `xray run -test -config`，固定 26.3.27 | 完整单 JSON 可加载，出站配置与 Python 实测一致 |
| 无效/相同 endpoint | 控制 API 测试 | 非法输入拒绝；IP:PORT 不变不重启 |
| 新 endpoint | 控制 API + x-ui 进程管理 | 持久化后只触发一次有效配置重启，新目的 UDP 端口收到 mKCP 数据 |
| B 入站恢复 | 访问既有 `26417`、`16360` 入口 | 重启后恢复正常；不把整进程重启期间的短时中断视为故障 |
| A→B 控制链 | A Go 通过 v2rayN/CF/VLESS POST 实际 IP:PORT | 不靠数据面就能触达 Go 控制服务；不绕过现有 CF/VLESS |
| 真实用户上网 | A 上 `curl --proxy socks5h://127.0.0.1:10808 https://ifconfig.me` | 经 B `freedom` 访问、显示预期境外出口；DNS 按预期解析 |
| NAT 更新恢复 | 真实/可控模拟 A 公网 PORT 改变 | B 按已确认变化重启 Xray，A 无人工更新 B JSON 即恢复上网 |
| STUN 抖动 | 诱发一次/数次超时 | 不清空当前可用 endpoint、不为单次失败重启 Xray |

## 10. 明确不做

- **不创建第二个 B Xray 进程、不单独开发 MyTRN Web 页面、不实现 Xray runtime API 动态出站更新**。
- **不修改**既有 Caddy、CF CDN、VLESS `26417` 的传输协议、路径或其他运行参数。
- **不自写** QUIC/KCP/VLESS/TCP 代理；不让 B 访问 A 内网服务替代 A 上网需求；不把 WARP 当全系统默认路由。
- **不兼容**旧 Python QUIC Agent、旧 VMess Portal/Bridge、历史业务协议或不被此架构验证的配置格式；不增加备用数据通道、双进程自动切换、第三台服务器等兜底机制。
- 不引入 TUN、全局透明代理、复杂多租户控制台或与本目标无关的订阅/Caddy 新功能。

## 11. 代码落点（实施时的审查清单）

| x-ui 现有位置 | 预期最小改动 |
| --- | --- |
| `database/model/model.go`、`database/db.go` | 新增**单例 MyTRN 业务配置/最近 endpoint**，不改原 `Inbound`/`PublicEndpoint` 定义 |
| `web/service/xray.go` | 在单个 `GetXrayConfig()` 合并 MyTRN 出站与路由；endpoint 变化走现有 `RestartXray(false)` |
| `web/service/tunnel.go` | 保留旧 Tunnel 原样；不混用 VMess Portal，不把 reverse 当原来的 Tunnel 模式 |
| 新 `web/service/mytrn.go`（名称可随现有代码规范调整） | 严格配置校验、单 A endpoint 保存、原生 Xray JSON 片段生成 |
| 新 `web/controller/mytrn.go` + x-ui 内置 Go HTTP 控制监听 | 仅 MyTRN 业务编辑/状态、带 token 的 IP:PORT 注册；管理接口复用既有登录鉴权 |
| `web/html/xui/inbounds.html` | 在**现有列表**显示一条独立 MyTRN 业务记录、编辑弹框/开关/状态，不增加侧边栏页面 |
| `xray/process.go` | 原则上**不改进程模型**；复用已存在的版本校验、`run -test`、重启/基本启动失败处理 |
| `qqq694637644/mytrn` 中 A 侧代码（独立仓库） | 后续把 Python UDP/STUN/控制面/Xray 编排迁 Go，不与 B x-ui 的 Go UI/业务数据模型混在一起 |

**审查结论待用户确认：单 B Xray 进程、统一 JSON，MyTRN 出站显示在 x-ui 原入站列表；A endpoint 真变更后重启整个 Xray，接受已有 VLESS 入站短暂中断；除此之外不扩大架构。**
