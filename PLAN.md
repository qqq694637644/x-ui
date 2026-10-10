# MyTRN 与 x-ui 整合计划：A Python、B Go、单 Xray 进程

> **状态：待审查，尚未实施。** 依据已在真实 A/B 网络通过的 MyTRN Python/Xray-core **v26.3.27** 链路，以及当前 x-ui `main` 的 Go 配置生成和进程管理方式。
>
> **本次范围：仅将 B 的 MyTRN 管理与数据面整合进现有 x-ui。A 仍运行 Python，只为简化控制上报格式做必要的小调整。** 个人单 A/B 使用，不做旧协议兼容、复杂认证、备用通道或多实例设计。

## 1. 先确定职责，不重复建设

| 组件 | 本期职责 | 不做什么 |
| --- | --- | --- |
| **A Python Agent** | 保留 UDP `39999`、同 socket STUN、UDP 网关、本地 Xray v26.3.27、控制上报、现有 Web 配置 | **不迁 Go**，不重写 mKCP/VLESS/TLS，不换代理入口 |
| **A v2rayN** | 本地用户代理入口/分流；MyTRN 控制请求走已有 CF/VLESS SOCKS5 `127.0.0.1:10810` | 不让控制请求走会随数据面断开的 MyTRN SOCKS5 |
| **B 现有 CF CDN + Caddy + VLESS `26417`** | 让 A 能通过已工作的过墙链路访问 B 的普通 HTTP 控制端口 | **不修改现有过墙协议/路径**，不用于传用户网站数据 |
| **B x-ui Go** | 在现有程序内接收 A 的 `IP:PORT`、保存 endpoint、生成 Xray 配置、触发已有重启 | 不起 B Python Agent，不添加第二个 MyTRN 管理系统 |
| **B 现有 Xray** | **一个 Xray 进程、一份 `bin/config.json`**，运行既有入站 + MyTRN VLESS/mKCP 数据出站 | 不起第二个 Xray，不用运行时出站热更新 |
| **B 现有 WARP SOCKS5** | 仅提供 `127.0.0.1:40000`，供 B Xray 把发往 A 的 mKCP UDP 包交过去 | 不安装、不托管、不创建新 WARP/SOCKS5 服务，也不默认负责网站出口 |

**业务目标**：国内 A 的应用经已建立的反向数据通道，由国外 B 的正常 `freedom` 出站访问互联网。不是 B 访问 A 内网。

## 2. 两条链路完全分离

### 2.1 控制面：普通 HTTP + 一个 16 字符 key

```text
A Python STUN（同一 UDP 39999）发现公网 IP:PORT
       │
       └─ HTTP POST /control/mapping（仅 IP、PORT）
               │ 头部带双方相同的 16 字符 key
               ▼
         A v2rayN SOCKS5 :10810
               │ 现有 CF CDN / Caddy / VLESS 26417 过墙链
               ▼
         B x-ui Go 普通 HTTP 控制 IP:PORT（例如 :18080）
               │ 比对 key → 保存映射 → endpoint 真变更才重启 Xray
               ▼
         B 统一 Xray 配置中的 MyTRN mKCP 拨号目标
```

- **控制面不使用 Xray reverse、mKCP、WARP，也不搞公钥/私钥、证书交换、指纹绑定或签名鉴权**。它本质上只是通过现有过墙代理发送一个公网 `IP:PORT`。
- 只使用**一个双方预先填写的随机 16 位字母数字 key**（大小写字母 + 数字，安全随机生成），放在 `X-Control-Token` HTTP header。B 做普通固定长度与常量时间比较；无需 JWT、HMAC、时间戳、挑战应答、证书注册或账户体系。
- B 控制服务只是 x-ui Go 的**普通 HTTP 入站**，直接监听现有代理可以访问的 B IP:PORT。控制面的 `26417` 是过墙代理通道，不是 MyTRN 的控制服务，也不需要新 Caddy path 或另一层反向代理。
- **控制流量始终走现有 v2rayN→CF/VLESS**，绝不回送 A 数据面 `127.0.0.1:10808`，否则数据通道掉线会导致控制面也不可用。
- 单人使用不增加多节点身份、用户管理或其他鉴权框架。控制 key 与 x-ui 管理员登录口令、数据面的 VLESS UUID 相互独立。

### 2.2 数据面：只有这里用 Xray VLESS reverse

```text
A 本机浏览器/应用 → A v2rayN（用户流量分流）
   → A Xray SOCKS5 127.0.0.1:10808
   → A Xray VLESS reverse-out
   ⇅ A Xray mKCP/TLS UDP 127.0.0.1:40001
   ⇅ A Python Gateway UDP :39999（同 socket STUN）
   ⇅ A 路由器/运营商 NAT 公网 IP:PORT
   ⇅ B 现有 WARP SOCKS5 UDP 127.0.0.1:40000
   ⇅ B x-ui 统一 Xray 的 VLESS/mKCP/TLS 主动出站
   → B Xray 数据面 reverse-in（内部 tag）
   → B Xray 原有 freedom
   → 境外网站
```

- **B 主动拨入 A；A 发起网站请求，最终由 B 出网。**
- `reverse-in` **只是数据面**为了让 A 的 SOCKS5 请求沿 B 建立的隧道返回 B 而生成的 Xray 内部标签；**不属于控制面 HTTP**，不需要建立新的公网入站。
- 本期继续使用真实网络已验证的 **Xray-core v26.3.27**、mKCP、VLESS reverse、TLS 和 SOCKS5 组合；不要重新造 QUIC/KCP/TCP 代理。

## 3. B WARP 和 freedom：只复用已有组件

B 已经有 WARP SOCKS5 地址 `127.0.0.1:40000`，**直接用即可**。

在 x-ui 的统一 Xray JSON 中新增 **两个 outbound 配置项**（不是两个进程或端口）：

1. `mytrn-dial`：VLESS/mKCP/TLS 出站，`settings.address/port` 来自 A 最近一次上报的公网 endpoint，`settings.reverse.tag` 指向数据面内部 `mytrn-data-in`。
2. `mytrn-warp`：Xray SOCKS5 **客户端 outbound**，服务器字段指向现有 `127.0.0.1:40000`；`mytrn-dial.streamSettings.sockopt.dialerProxy = "mytrn-warp"`，让 mKCP UDP 由现成 WARP SOCKS5 发送。

x-ui 现有模板已经包含 `freedom` 出站。**复用现有对象**并赋予可被路由引用的 tag（如 `mytrn-freedom`），而不是为 MyTRN 再加一个 `freedom` 实例。只需一条 **数据面**精确路由：`inboundTag = mytrn-data-in → outboundTag = mytrn-freedom`。原有入站、默认 freedom 行为、路由顺序、私网限制和现有 Tunnel 不应被覆盖。

**不要把 `mytrn-warp` 当成要创建/运行的新 SOCKS5 服务**；它只是 Xray 为连接现有 WARP 端口而需要的一条客户端配置。网站 TCP 出口仍是 B 的正常 `freedom`，不是 WARP。

### 数据面 TLS 与控制 key 是两回事

用户要求去掉的是**控制面的证书/指纹认证**，不是推翻已经跑通的 Xray mKCP/TLS 数据通道。数据面保持现有 TLS 验证（`allowInsecure=false`）：

- A Python 已会生成数据面 `a-cert.pem` / `a-key.pem`；**私钥仅留在 A**。
- B x-ui 首次配置 MyTRN 时，从 A 手工复制一次 **公用证书 `a-cert.pem`** 到 B 的私有配置目录，Xray VLESS/mKCP/TLS 出站用它验证 A。它是**原本数据面所必需的 Xray TLS 配置**，不参与 HTTP 控制请求。
- 每次 STUN 注册只发送 IP:PORT 和 16 字符 key，**不发送 PEM、私钥、指纹或 VLESS UUID**。不要把证书字段塞回控制 API，也不另做“控制面证书绑定”。

### B 数据面 Xray 最小生成要求

```json
{
  "outbounds": [
    {
      "tag": "mytrn-dial",
      "protocol": "vless",
      "settings": {
        "address": "<A 当前公网 IPv4>",
        "port": 57197,
        "id": "<A 的 VLESS UUID>",
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
          "certificates": [{"certificateFile": "<B 私有目录中的 A 证书>", "usage": "verify"}]
        },
        "sockopt": { "dialerProxy": "mytrn-warp" }
      }
    },
    {
      "tag": "mytrn-warp",
      "protocol": "socks",
      "settings": { "servers": [{ "address": "127.0.0.1", "port": 40000 }] }
    }
  ],
  "routing": {
    "rules": [{ "type": "field", "inboundTag": ["mytrn-data-in"], "outboundTag": "mytrn-freedom" }]
  }
}
```

这只是**待合并的片段**：`GetXrayConfig()` 要追加 MyTRN 的两个 outbound、精确路由，并在现有 freedom 上设置 tag；**不能替换原 `outbounds`、`routing` 或 `inbounds` 数组**。示例里的 `57197` 不是固定配置，运行时必须来自 A 当前 STUN 上报。

## 4. 控制协议：只剩下 IP:PORT + 16 字符 key

唯一接口：`POST /control/mapping`。

```http
POST /control/mapping HTTP/1.1
X-Control-Token: <16 个随机大小写字母数字字符>
Content-Type: application/json

{"ip":"119.98.144.218","port":57197}
```

**这是未来要实现的新格式，目前 Python A 代码还没有改成它。** 旧 Python `register_from_a()` 的 body 仍是 `{"node":"a","ip":... ,"port":... ,"certificate":...}`，`config.py` 也要求 `control_token` 至少 32 字符。因此 B Go 迁移时必须同时做下面这几项**仅限 Python 控制面的微小修改**：

- A Python `register_from_a()` 只发送 `ip`、`port`，HTTP header 携带 `X-Control-Token`；删除注册函数的 `certificate` 参数及其调用点传参。
- A Python 配置中只有 `control_token` 改为**正好 16 个随机字母数字字符**；新建配置按此生成、已有 A 配置在切换前手动填入与 B 相同的新 key。**`admin_token` 等其他既有口令规则、STUN、Xray/TLS、UDP Gateway 均不改。**
- B x-ui Go 控制 API 只接受这一个新 JSON 格式，不接受旧 `node/certificate` 字段，**不做双协议兼容**。

B Go 处理流程只需：

1. 校验 `X-Control-Token` 是否为双方预设的 16 字符 key；不匹配直接拒绝。
2. JSON 严格读取单播 IPv4 `ip` 与合法整型 `port`；只接受这两个字段，限制报文大小。
3. 将最新认证过的 `ip:port` 保存在 x-ui SQLite；**相同 endpoint 回复成功、不重启**，真正变化才标记更新。
4. 返回简单响应，如 `{"ok":true,"changed":false}`；新 endpoint 通过原有 x-ui 调度程序合并到统一 JSON 并重启其 Xray。

控制 API 不接收公钥、不检查指纹、不签名；不新增另一套密码学依赖。控制链使用现有 CF/VLESS 传输，控制 Go HTTP 服务在 B 上监听的地址与端口由现有可达方案决定，**不改 Caddy/VLESS `26417` 的任何配置**。

## 5. A 保持 Python；B 只由 x-ui Go 管理

A 的核心程序仍是 `python -m mytrn a`：

- UDP `39999` 同 socket STUN 与 mKCP 数据报透明转发到 `127.0.0.1:40001`；保留已验证的 Windows UDP `WSAECONNRESET` 处理。
- 启动后和映射真正变化时上报 IP:PORT；每约 20 秒 STUN 检查、两次确认变化，偶发 STUN 超时不删掉可用映射；相同 endpoint 低频刷新。
- A Xray v26.3.27 保持本地 `SOCKS5 127.0.0.1:10808`、`reverse-out`、mKCP/TLS 与 A 本机 Web UI；A v2rayN 继续保持用户代理分流与 CF/VLESS 控制出站。
- **仅第 4 节明确的控制请求 JSON/16 字符 key 有小改动；不写 A Go、不重做 A 网络架构，也不保留旧控制请求兼容格式。**

B 迁移完成后，关闭**旧 B Python Agent 与它的专用 Xray**（会与新控制端口/数据连接冲突）；x-ui 原有 Go 进程和其唯一 Xray 接管 B 侧数据、控制 API、配置及重启。现有 x-ui VLESS `26417`、`16360`、Caddy 和 WARP 保持原样。

## 6. UI 和 x-ui 代码落点：用现有设计，不另起炉灶

**在现有“入站列表”显示一条 `MyTRN` 业务记录**，复用编辑/启停/状态展示，不新增独立页面或侧栏。它是 x-ui 的**业务类型**，不是 Xray 原生 `protocol: mytrn` 入站，也没有 B 公网监听端口；列表的端口列显示 `—`。

最小编辑字段：启用、备注、**16 字符控制 key**、数据面 VLESS UUID、**数据面 A TLS 证书（首次配置一次）**、现有 WARP SOCKS5 地址 `127.0.0.1:40000`、Go HTTP 控制服务监听地址/端口。A 的动态公网 endpoint 为自动更新的只读状态。不要把证书与控制 key 混为一项配置，也不加入订阅链接、二维码或额外用户管理。

| 现有 x-ui 位置 | 本期最小职责 |
| --- | --- |
| `database/model`、SQLite/GORM | 单例 MyTRN 配置、16 字符 key、A 数据面公用证书路径、最新 A endpoint |
| `web/service/xray.go:GetXrayConfig()` | 合并 MyTRN VLESS/mKCP 出站、指向已有 WARP 端口的 SOCKS5 客户端 outbound 和指向现有 freedom 的数据面路由 |
| `web/service/xray.go:RestartXray(false)` | 复用现有配置生成、`run -test` 和重启；不改为 Xray Runtime API 动态出站 |
| 现有 `SetToNeedRestart()` 调度 | A endpoint 真变化才触发，重复上报不重启 |
| x-ui Go HTTP 控制 handler | `POST /control/mapping`，只接收 `ip:port` + `X-Control-Token` |
| `web/html/xui/inbounds.html` | 现有列表中的 MyTRN 业务行与编辑方式，不建新页 |
| x-ui 已有 Tunnel/Caddy/CF/VLESS | 保持不动，不复用旧 VMess Portal 当 MyTRN |
| `qqq694637644/mytrn` 的 A Python 控制代码 | **仅同步新的上报 JSON 和 16 字符 key 校验**，其他 Python/Xray 数据面逻辑不变 |

MyTRN 开启而 A 尚未上报有效 endpoint 时，显示“等待 A 注册”，**不生成虚构拨号地址**；关闭 MyTRN 时不在统一 Xray 配置加入 MyTRN 的出站/路由。

## 7. endpoint 变化直接重启 Xray

```text
A Python STUN 两次确认公网 IP:PORT 改变
    → 原有 v2rayN / CF/VLESS 链路 POST IP:PORT + 16 字符 key
    → B x-ui Go 验证并保存当前 endpoint，回复 ACK
    → x-ui 既有 SetToNeedRestart() / RestartXray(false)
    → 统一 bin/config.json 重新生成、run -test
    → 同一 Xray 进程重启，B 经现有 WARP SOCKS5 UDP 重新拨 A
    → A 的 SOCKS5→B freedom 外网请求恢复
```

**接受代价**：x-ui 的单 Xray 进程重启会短暂影响当前 `26417`、`16360` 等入站；x-ui Go 控制 HTTP 服务和独立的 Caddy 不随它重启。用户个人使用，映射变化频率低，**不做第二个 B Xray、不实现运行时出站热更或任何备用/兜底连接**。同样的 IP:PORT 刷新只 ACK，不触发重启。

## 8. 实施顺序与验收

### 第一阶段：在 x-ui 一次完成 B 侧所需能力（不先破坏现网）

1. 加入 MyTRN 单例数据模型、16 字符 key、现有入站列表中的业务条目、数据面 Xray 配置生成与合并。
2. 加入 x-ui 内置的简单 Go HTTP `POST /control/mapping`、保存最新 endpoint、相同映射不重启的判定；配置与数据面使用**同一条存储记录**。
3. 对**完整**统一 Xray JSON 做配置校验及本地真实 Xray 26.3.27 集成测试：原入站和 MyTRN 都存在，`mytrn-dial` 经指定 WARP SOCKS5，A 的浏览请求真正经 B `freedom` 返回。
4. 这些代码准备就绪后再切换 B 运行服务；不提前停掉现有 x-ui、Caddy、VLESS 或 WARP。

### 第二阶段：一次性切换控制格式和 B 管理权

1. 在 B x-ui MyTRN 业务项中填好**与 A 匹配的 VLESS UUID**、一次性取得的 **A TLS 公用证书**、16 字符控制 key 与现有 WARP 地址。
2. **只改 A Python 控制上报函数和控制 key 规则**：发送 `{"ip":..., "port":...}`，请求头带同一个 16 字符 key；重新配置 A 的 `control_token`，A 仍由 Python 运行。
3. 停掉**旧 B Python Agent/专用 Xray**，让 x-ui Go 监听旧控制 HTTP 端口并由 x-ui 已有 Xray 承载数据面。无需 B Python 常驻，也不生成第二个 Xray JSON。
4. 验证 A Python 通过原 CF/VLESS 代理把 IP:PORT 发到 B Go HTTP 接口，B 数据面主动拨回 A 并正常上网；**不同时运行两个控制服务或兼容新旧接口**。

### 第三阶段：真实网络恢复测试

- A 上运行 `curl.exe --proxy socks5h://127.0.0.1:10808 https://ifconfig.me`，确认外网出口属于 B；Xray 日志能看到 B `freedom` 发起真实网站连接。
- 同一 endpoint 重复注册，B 不重启 Xray；A STUN 偶发超时也不导致映射清除和重启。
- A 端公网 IP/PORT 真变化后，B 保存并重启现有 Xray，A SOCKS5 自动恢复；重启后原 VLESS `26417`、`16360` 恢复正常。
- 控制接口拒绝错误 key/非法 IP:PORT，不传 PEM 或私钥，密钥不写入日志；确认数据面 TLS 校验依然开启。
- 现有 Caddy、CF/VLESS、WARP 没被重配置；旧 B Python Agent 与其专用 Xray 不再运行。

## 9. 明确不做

- 不开发 A Go；**A 继续 Python**，除简化控制 POST 和 16 字符 key 外不修改既有稳定链路。
- **控制面不做证书/公私钥/指纹认证**，不加入 `node`、`certificate`、时间戳、JWT、HMAC 或兼容旧格式；只用 16 字符随机 key 和 `ip:port`。
- 不新建/维护 WARP SOCKS5 进程；Xray outbound 只是连接现成的 `127.0.0.1:40000`。
- 不重复生成 freedom，不让控制面走 Xray reverse；数据面 `mytrn-data-in` 只是内部路由标签，不是新监听端口。
- 不创建第二个 B Xray、不做运行时 API 热更、不开发新 MyTRN 页面、不做额外兜底、第三台服务器或多用户体系。
- 不改原有 Cloudflare、Caddy、VLESS `26417` 的工作方式；不造自写 QUIC/KCP/VLESS/TCP 转发，也不恢复旧 VMess Portal/QUIC 方案。

**审查结论：个人单 A/B，控制上报只用 16 字符 key + 公网 IP:PORT；A 保留 Python；B 复用 x-ui Go、单 Xray、统一 JSON；WARP 只是既定 SOCKS5 UDP 端口；控制面普通 HTTP，Xray reverse 仅用于数据面；endpoint 变化只需整进程重启。**
