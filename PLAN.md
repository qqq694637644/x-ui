# MyTRN 接入 x-ui：B 端 Go 整合计划（A 保留 Python）

> **状态：架构设计，供审查；尚未实现。** 依据 `qqq694637644/x-ui` 当前 `main` 与已经在真实 A/B 网络成功验证的 `qqq694637644/mytrn` Python Agent、Xray-core **v26.3.27**。
>
> **本次唯一开发目标：把 B 上独立运行的 Python MyTRN 控制服务及其专用 Xray 数据面，整合进已有 x-ui Go 后端和它管理的 Xray。A 端继续运行现有 Python，不迁移 Go。** 不重写网络协议，不加兼容层，不重构现有 CF/CDN、Caddy、VLESS 或 WARP。

## 1. 最终架构约束（作为实现验收依据）

| 范围 | 已确定的选择 |
| --- | --- |
| A（中国 Windows） | **继续使用现有 `mytrn` Python Agent**，包括 UDP 39999、STUN、HTTP 上报、A 本地 Xray v26.3.27 和 Web/配置；**本期不写 A Go** |
| B（境外 Linux） | 在**现有 x-ui Go 进程**内实现 MyTRN 的配置、控制 HTTP 接口及状态；不保留生产部署中的 B Python Agent |
| B Xray | **复用 x-ui 管理的唯一 Xray 进程和统一 `bin/config.json`**，不为 MyTRN 起第二个 Xray，也不生成多套配置 |
| x-ui 界面 | **复用现有“入站列表”展示和管理一条 MyTRN 业务记录**，不新建独立页面或侧栏菜单；MyTRN 实际是 B 主动拨出的业务，不伪造监听端口 |
| 过墙控制链 | 保持已工作的 **A v2rayN SOCKS5 → CF CDN → Caddy/现有 VLESS `26417` → B HTTP 控制 IP:PORT**；VLESS `26417` 只是传输代理，不是 MyTRN 控制协议 |
| 控制 API | **B x-ui 自己的普通 Go HTTP 监听服务**，直接接收 A 通过现有代理发来的 STUN 公网映射；**不使用 VLESS reverse，也不新增 Xray 控制入站** |
| 数据通道 | **B Xray 的 VLESS/mKCP/TLS 出站通过已有 WARP SOCKS5 UDP 主动连接 A**；A 的用户请求借助已验证的 VLESS **数据面 reverse** 到 B，由 B 正常出口访问外网 |
| WARP | **仅使用 B 已存在的 SOCKS5 地址 `127.0.0.1:40000`**；x-ui 不启动、不配置、不托管 WARP，只让 Xray 把 mKCP UDP 数据报交给这个现成端口 |
| NAT endpoint 更新 | 只有 A 已确认公网 IP:PORT 真变化才更新 B 记录，随后复用 x-ui 的 **`RestartXray(false)` 整进程重启**；接受现有入站短暂断线 |
| 明确不做 | Xray runtime API 热更新、第二个 Xray 进程、A Go、旧 QUIC/VMess Portal 兼容、自写 KCP/TCP 代理、额外兜底通道 |

**目标不变：A 的浏览器最终从 B 的 VPS 访问互联网。** 不是让 B 访问 A 的内网，也不是把用户网页流量经过 CF CDN 传回 B。

## 2. 两条链路：控制是普通入站，反向只属于数据面

### 2.1 控制面：A 主动发送 HTTP，B 普通 HTTP 服务接收

```text
A Python Agent
    │ STUN 探测 A 本机 UDP 39999 得到公网 IP:PORT
    │
    └── HTTP POST /control/mapping（业务内容：A 的公网 IP:PORT）
              │
              ▼
       A 已有 v2rayN SOCKS5 :10810
              │ 现有 CF/VLESS 代理链
              ▼
       Cloudflare CDN / Caddy / B 现有 VLESS :26417
              │ 已有代理正常转发 TCP 请求
              ▼
       B x-ui Go 的 HTTP 控制服务（既定 IP:PORT，例如 :18080）
              │ 鉴权、校验、保存新 endpoint
              ▼
       x-ui 重新生成 B Xray 配置并在需要时重启
```

**控制面不需要反向代理：**这是 A 发起、B 正常接收的普通 HTTP 请求。B 控制 API 直接监听可通过现有 VLESS 代理访问到的 IP:PORT；x-ui Go 处理 JSON 即可。不要在控制链路中加入 `reverse-in`、mKCP、WARP 或 A 数据面 SOCKS5，也**不要专门修改 Caddy 转发路径或 `26417` 的传输配置**。

重要边界：控制请求必须走 A **现有** v2rayN/CF/VLESS 出站，不可绕回 A 的 MyTRN 数据 SOCKS5 `127.0.0.1:10808`，否则数据面断开时会形成循环依赖。使用已验证可达的 B 控制 HTTP IP:PORT；不要把未经验证的 `127.0.0.1` 可达性当作前提，也不需要新建一套反代服务。

### 2.2 数据面：B 主动连接 A，A 通过 B 上网

```text
A 浏览器 / 本机应用
      │
      ▼
A 现有 v2rayN（用户本地代理/路由）
      │ 把应走 MyTRN 的请求交给 A 数据代理
      ▼
A Xray SOCKS5 127.0.0.1:10808
      │ Xray VLESS 数据面 reverse-out
      ▼
A Xray VLESS/mKCP/TLS UDP 127.0.0.1:40001
      ⇅
A Python UDP Gateway :39999（同 socket STUN、透明 UDP 转发）
      ⇅
A 光猫路由器 + 电信 NAT 公网 IP:PORT
      ⇅
B 现有 WARP SOCKS5 UDP 127.0.0.1:40000
      ⇅
B x-ui 管理的 Xray：VLESS/mKCP/TLS outbound（B 主动拨入 A）
      │ Xray VLESS 数据面 reverse-in
      ▼
B Xray 现有 freedom 出站
      ▼
境外互联网（Google / GitHub / 其他网站）
```

这里**只在数据面使用 Xray 原生 VLESS reverse**，因为传输连接由 B→A 建立，但上网业务请求要由 A→B 发出。`reverse-in` 是 Xray **内部的数据请求分发标签**，不是 Go 控制 HTTP 入站、更不是新增公网监听端口。它与控制面的 VLESS `26417` 完全无关。

A Xray 本身、VLESS reverse、mKCP/TLS、SOCKS5 CONNECT、网站 TCP 连接、可靠传输都继续由 **Xray-core v26.3.27** 完成。A Python 仅负责已有 STUN/UDP Gateway、上报、进程和配置，**不在本期修改 A 架构**。

## 3. WARP 只是一个已有的 SOCKS5 UDP 端口

B 已有 WARP 本地 SOCKS5 代理：`127.0.0.1:40000`。MyTRN **只需要让 B 的 Xray 把到 A endpoint 的 mKCP UDP 数据经它发送**。

在已通过实网验证的 Xray v26.3.27 中，做法就是：

- MyTRN 的 VLESS/mKCP 出站设置 `streamSettings.sockopt.dialerProxy = "mytrn-warp"`。
- `mytrn-warp` 是**同一份 Xray JSON 中一个 SOCKS5 客户端 outbound 记录**，其唯一上游地址是 `127.0.0.1:40000`。它只是告诉 Xray 向**已经存在**的 SOCKS5 端口发起 UDP ASSOCIATE，**不是新建 SOCKS5 监听端口、WARP 进程、转发服务器或额外独立代理模块**。
- 用户访问外网的目标 TCP 连接由 B 的现有 `freedom` 出站建立。**WARP 不负责用户网站出口，只有 B→A 的 mKCP UDP 走 WARP。**

**不开发 WARP 安装、启停、状态管理、路由规则、自动替代出口或第二个转发层。** UI 最多保留一个可编辑的 SOCKS5 地址/端口字段；默认沿用已经验证的 `127.0.0.1:40000`。

## 4. B 端 Xray：复用已有进程、配置和 freedom

根据 x-ui 当前源码：

- `web/service/xray.go:GetXrayConfig()` 读取模板，将所有启用的普通入站、已有 Tunnel 业务合并为一个 `xray.Config`。
- `xray/config.go:Config` 已包含统一 `inbounds`、`outbounds`、`routing`；`xray/process.go` 用**单一 `bin/config.json`**运行**同一个 Xray 子进程**。
- 模板 `web/service/config.json` **已经有 `freedom` 出站**，位于 `outbounds` 的第一项（目前未设置 tag）；同时存在 `blocked`、API、私网与 BitTorrent 路由规则。

**不能因为 MyTRN 再复制出一个 `freedom` 模块。** MyTRN 新增的必要配置仅有：

| 配置元素 | 作用 | 是否额外服务/进程 |
| --- | --- | --- |
| `mytrn-dial`（VLESS outbound） | B 通过 mKCP/TLS 主动拨 A 当前 STUN IP:PORT，启用数据面 reverse | 否：已有 Xray 内部配置 |
| `mytrn-warp`（SOCKS5 outbound） | Xray 的 mKCP 拨号使用**现有 WARP SOCKS5 端口** | 否：只是 Xray SOCKS5 客户端配置 |
| `mytrn-data-in`（VLESS 内部 reverse tag） | **仅数据面**：将 A 的上网请求路由到 B `freedom` | 否：不是监听端口，也不属于控制面 |
| 模板**已有 `freedom`** | B 到外网的 TCP 出口；为路由引用补充稳定 tag（如 `mytrn-freedom`） | 否：复用原配置对象，不增加第二条 freedom |

### 4.1 最小配置结构（片段，不是另一个 config.json）

以下由 x-ui 生成并追加/合并进**现有**统一配置。具体证书/UUID/endpoint 由 MyTRN 当前单例记录提供：

```json
{
  "outbounds": [
    {
      "tag": "mytrn-dial",
      "protocol": "vless",
      "settings": {
        "address": "<A 最新 STUN 公网 IP>",
        "port": 57197,
        "id": "<与 A 匹配的 VLESS UUID>",
        "encryption": "none",
        "reverse": { "tag": "mytrn-data-in" }
      },
      "streamSettings": {
        "network": "kcp",
        "security": "tls",
        "kcpSettings": { "mtu": 1200 },
        "tlsSettings": {
          "serverName": "mytrn-a.test",
          "allowInsecure": false,
          "disableSystemRoot": true,
          "certificates": [
            { "certificateFile": "<B 上保存的 A 公钥证书路径>", "usage": "verify" }
          ]
        },
        "sockopt": { "dialerProxy": "mytrn-warp" }
      }
    },
    {
      "tag": "mytrn-warp",
      "protocol": "socks",
      "settings": { "servers": [ { "address": "127.0.0.1", "port": 40000 } ] }
    }
  ],
  "routing": {
    "rules": [
      {
        "type": "field",
        "inboundTag": ["mytrn-data-in"],
        "outboundTag": "mytrn-freedom"
      }
    ]
  }
}
```

**合并要求：**上面的数组是 MyTRN 的**新增部分**，不是替换 x-ui 已有的 `outbounds`/`routing.rules`。在现有模板第一个 `freedom` 出站上增加 `tag: "mytrn-freedom"`，保持它原本的配置、排序和默认出口语义；MyTRN 的 `mytrn-data-in → mytrn-freedom` 精确规则要和现有 API、私网阻断、BitTorrent 规则共存，**不能绕过现有私网禁止策略**。如果当前模板缺少预期的 freedom、出现重复 tag 或无法正确组合规则，生成配置时**直接报错**，不自动生成备用出站或重设其他业务路由。

`mytrn-data-in` 是 **B Xray 根据 VLESS `settings.reverse` 创建的内部数据面 inbound tag**，**绝不是控制服务**。控制服务由 x-ui Go 的普通 HTTP listener 接收 A 发来的 POST，**不在这份 Xray 片段里创建任何控制入站**。

## 5. B Go 控制 API：直接接 A Python 当前请求

A 已验证运行的 Python 代码在 `mytrn/mytrn/control.py:register_from_a()` 中，向 B 发出普通 HTTP：

```http
POST /control/mapping HTTP/1.1
X-Control-Token: <A/B 匹配的随机密钥>
Content-Type: application/json

{"node":"a", "ip":"119.98.144.218", "port":57197, "certificate":"<A 的 PEM 公钥证书>"}
```

**核心业务数据是 A 公网 `IP:PORT`**。`node`、`certificate` 和 header 是 A Python **当前唯一有效的注册格式**；B Go 直接实现同一接口，以便 A **完全不必为了 B 迁移而改代码**。这不是旧协议兼容，也不是控制面反向代理。上面的公网地址/端口仅为历史示例，运行时以实时 STUN 注册值为准。

B Go 服务处理逻辑：

1. x-ui Go 在指定的、**现有 v2rayN/CF/VLESS 链路可到达**的 `IP:PORT` 上提供普通 HTTP 入站。监听端口建议沿用 Python B 已实测的 `18080`，但 B Python Agent 退出后才由 x-ui Go 绑定，**不能两个进程同时监听**。
2. 用 `X-Control-Token` 验证请求身份。严格校验 JSON 节点、IPv4、端口和 PEM 证书；限制请求体大小，拒绝未知字段，日志不打印 token。
3. 初次配对固定 A 的 TLS 证书指纹（与现有 Python B 行为一致）；后续证书改变则拒绝，不自动更换信任；**B 永远不需要 A 的私钥**。
4. 持久化最近一次已认证的 A `ip:port`，与已应用 endpoint 比较：**相同则直接 ACK，不重启；不同才标记需要更新 Xray 配置**。最新 endpoint 是 B 的统一 Xray 配置生成时的唯一来源。
5. 先正确完成 HTTP 应答，再通过现有重启调度路径应用配置。多次变化在调度周期内合并；B Go 服务不自己运行另一个 Xray。
6. x-ui 重启后从其持久化记录恢复当前 A endpoint；没有有效 endpoint 时 **MyTRN 标记等待注册，不生成假的 A 拨号目的地**，同时不妨碍其他已有 VLESS 入站。

**控制 API 的外部可达性由既有过墙代理保证**。不需要 MyTRN 使用 `reverse-in`，不需要另配 Caddy 路由，也不需要 CF 转发 HTTP 到新的 MyTRN 路径。B Go 控制服务的监听地址与访问控制按实机已有路由选择，不凭猜测强制设为 `127.0.0.1` 或无防护公开到 `0.0.0.0`。

## 6. A 保留现有 Python，按原样承担全部 A 端职责

**本次开发对 A 不安排 Go 迁移，也不重写已验证的 Python STUN/UDP/控制面。** 保留：

- `python -m mytrn a`：A Python Agent 和已有本机 Web 配置。
- A UDP `39999`：同一 socket STUN 获取公网映射并透明转发 mKCP 数据到本机 `127.0.0.1:40001`；保留已验证的 Windows `WSAECONNRESET` 处理。
- STUN 约每 20 秒检测、连续两次确认映射变化；偶发超时不清除旧 endpoint。
- 启动及 IP:PORT 真变化时通过原有 v2rayN SOCKS5/CF/VLESS 发送 `/control/mapping`；日常低频刷新不触发 B 重启。
- A Xray 26.3.27 仍负责 VLESS/mKCP/TLS `reverse-out` 和本地 `127.0.0.1:10808` SOCKS5 数据入口。
- A 的 v2rayN 继续按现有可用方式把本机用户代理流量导向 A 数据 SOCKS5；控制面请求固定走 CF/VLESS，**不能经过 A 数据 SOCKS5**。

本期唯一迁移边界是：**停止 B 侧 Python 控制 Agent/专用 Xray，改由 x-ui Go + x-ui 原 Xray 接管同样的 B 侧职责**。A Python 继续运行并保持现有配置与上报报文不变。

## 7. x-ui 现有架构如何复用（只改必须改的地方）

### 7.1 管理界面

在现有 **入站列表**新增一条 `MyTRN（反向上网）` **业务条目**，不建新页面。复用当前列表的启停、编辑弹窗、状态展示与操作风格；只管理**一个** A/B 配对。

- `协议/类型` 显示 `MyTRN`；`端口` 显示 `—（主动出站）`，而不是假装 B 有一个新的 Xray 入站端口。
- 编辑字段只保留启用、备注、匹配 A 的 VLESS UUID、A 的 TLS 公钥证书/信任状态、控制 token、控制 HTTP 监听 IP:PORT、现有 WARP SOCKS5 的地址/端口。
- 当前 A 公网 endpoint 由控制注册更新，只读展示；不要让用户每次 STUN 变化都去 Web 手工改 IP。
- MyTRN 是数据面 outbound 业务，**不要作为 `model.Inbound{Protocol:"mytrn"}` 传进 `GenXrayInboundConfig()`**。使用一个简洁的 MyTRN 单例模型，合并展示到**同一个入站列表 UI**；也不生成订阅链接、二维码或入站流量配额。
- 状态区分：等待 A 上报、已有 endpoint、配置已应用、实际数据面测试成功。不可只凭 `Xray 运行中` 显示 `MyTRN 已可用`。

### 7.2 后端 Go 代码责任

| 当前 x-ui 位置 | 计划改动 |
| --- | --- |
| `database/model`、已有 SQLite/GORM 数据层 | 添加单例 MyTRN 设置、A TLS 证书/指纹及最新公网 endpoint 的持久化；不改已有 VLESS Inbound 数据 |
| `web/service/xray.go:GetXrayConfig()` | 在当前入站、Tunnel、模板合并完成后，再合并 MyTRN 的**两个**必要 outbound、引用现有 freedom 的精确数据面路由 |
| `web/service/xray.go:RestartXray(false)` | **直接复用**当前配置生成、`xray run -test`、整进程重启；不修改生命周期模型 |
| `web/controller/inbound.go` 中现有重启调度 | 复用 `SetToNeedRestart()` 标记与重启锁/调度周期；endpoint 一致不设置重启标志 |
| `web/web.go`、现有 Go 服务路由/控制器 | 添加普通 HTTP `POST /control/mapping` 接口，仅 IP:PORT 注册业务，使用现有 Python A 请求格式 |
| `web/html/xui/inbounds.html` | 现有列表中增加单条 MyTRN 业务行/编辑弹窗/状态；不新增菜单或页面 |
| `web/service/tunnel.go`、已有 Caddy/CF/VLESS 服务 | **保持原样**；MyTRN 不复用 VMess Portal、不更改 Caddy/VLESS 26417 |

B 的 Go 控制逻辑负责 HTTP 请求、配置合并、状态；**Xray 内核负责全部代理与反向数据通道**。不引入自研 SOCKS5 协议服务器、KCP 实现或新 WARP 进程。

## 8. endpoint 更新：直接采用 x-ui 现有整进程重启

```text
A Python STUN 连续确认公网 IP:PORT 有变化
     → A 通过 v2rayN/CF/VLESS 发送普通 HTTP POST
     → B x-ui Go 控制 API 保存新 endpoint、回复 ACK
     → x-ui 原有重启标志/调度器
     → GetXrayConfig() 重新生成唯一 config.json
     → ValidateConfig / xray run -test
     → RestartXray(false)：停止旧 Xray、启动新 Xray
     → 新 Xray 通过已有 WARP SOCKS5 UDP 主动拨 A
     → A 浏览器恢复通过 B 上网
```

**接受的代价**：x-ui 单 Xray 进程重启时，已有 `26417`、`16360` 等 VLESS 入站会短暂中断。Caddy 与 x-ui Go HTTP 控制服务不因 Xray 进程重启而退出；但 `26417` 暂停期间 A 过墙代理也会短暂不可用。个人使用，公网映射变化不频繁，**不为这点中断再做第二进程、API 热更或备用隧道**。

A 上报相同 endpoint 只 ACK，不生成新 Xray 配置、不触发重启。原 x-ui 的配置验证和启动失败处理保留，但**不为 MyTRN 增加额外的容错协议或版本兼容**。

## 9. 实施顺序：B 独立迁移、A 保持不动

### 第一阶段：x-ui 统一 Xray 中实现已验证的 B 数据面

1. B MyTRN 单例存储、配置合并、标签/路由冲突检查、现有 freedom tag 复用。
2. 在现有入站列表增加 MyTRN 业务条目与编辑操作。
3. 停止**旧 B Python Agent 和它的专用 Xray**，避免控制端口与数据连接重复；不要停止 x-ui、Caddy、VLESS 26417 或 WARP。
4. 在 x-ui 的一个 Xray 进程中合并 MyTRN 的 VLESS/mKCP 拨号与 SOCKS5 outbound，先用 A **原有 Python** 配置和已知公网 endpoint 验证 A→B→外网；确认原 VLESS 入站功能不受影响。

### 第二阶段：由 x-ui Go 接管 B 控制 HTTP 注册

1. 在 Go 里实现已验证的 Python A `/control/mapping` **同一报文格式**、token 校验、TLS 公钥指纹信任、endpoint 持久化。
2. A 仍使用原有 v2rayN/CF/VLESS 过墙代理发送请求到 B 控制 IP:PORT；不做新反向代理或 Caddy 代理配置。
3. 验证相同 endpoint 不重启、变化 endpoint 触发 x-ui 一次配置重建/整进程重启，旧 VLESS 入站在重启后恢复。

### 第三阶段：真实网络验收

- **核心业务**：在 A Windows 使用 `curl.exe --proxy socks5h://127.0.0.1:10808 https://ifconfig.me`，确认网站看到的是 B 正常境外出口；检查 B Xray 日志存在 `reverse-in → freedom` 的真实出站连接。
- **实际控制**：A Python 的 STUN 检测和自动 POST 到 B Go 控制接口成功；确认控制请求不依赖 MyTRN 数据代理，也不要求更改原有 CF/Caddy/VLESS。
- **动态 IP:PORT**：模拟或真实变更 A NAT 映射，确认 B x-ui 保存并重启 Xray 后恢复网页代理；同 endpoint 刷新不引起多余重启。
- **既有业务**：确认 VLESS `26417`、`16360` 在重启后恢复正常，x-ui 其他入站、Tunnel、Caddy 配置没有被 MyTRN 覆盖或修改。
- **稳定性和安全**：Windows A STUN 失败不触发频繁重启；B 只有 Xray 数据路径通过已有 WARP；B Go 控制服务拒绝错误 token，A 证书不静默更换，日志不泄露密钥。

## 10. 明确不做

- **A 端 Go 迁移不在本次计划中**。A 继续 Python，现有 A STUN/UDP Gateway、Xray 配置和 v2rayN 流量分配方案不推倒重做。
- **控制面不使用反向代理**，不为控制面生成 `reverse-in`、mKCP 隧道、WARP 出站或额外 Xray 入站。控制请求直接进入 B 的 Go HTTP 服务。
- 不新建/管理 WARP SOCKS5 服务，不重复创建 freedom 业务出口；仅让 Xray 把 mKCP UDP **发给既定的 SOCKS5 `127.0.0.1:40000`**。
- 不单独开发 MyTRN 页面、第二个 B Xray 进程或第二份配置；不使用 Xray 动态 `AddOutbound/RemoveOutbound`，只用既有整进程重启。
- 不改 Cloudflare、Caddy、现有 VLESS `26417` 的协议/路径，不迁移 x-ui 原有 Tunnel/订阅/PublicEndpoint。
- 不自写 QUIC/KCP/VLESS/TCP 代理协议，不增加备用连接方向、第三台服务器、多用户集群或旧版本协议/配置兼容。

**审查要点：A Python 原样保留；B x-ui Go 只整合普通 HTTP 控制入站与既有 Xray 的 VLESS/mKCP 数据出站；WARP 仅是指定 SOCKS5 UDP 端口；`reverse-in` 仅用于数据面，不在控制面；公网 endpoint 变化时接受单 Xray 进程短暂重启。**
