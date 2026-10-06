# Public Endpoint 与客户端订阅实施计划

## 1. 目标

当前 x-ui 已经维护多个 Inbound，最新主线也已经支持 VLESS/XHTTP。Cloudflare 已配置 `*.asdasdasdas.shop` wildcard DNS，因此日常轮换不再需要修改 Cloudflare DNS。

本计划要实现的最终体验：

```text
初始订阅：
a.y.z
b.y.z
c.y.z

a.y.z 不再使用后：
d.y.z
b.y.z
c.y.z
```

客户端始终保存同一个稳定订阅 URL，只需要刷新订阅，就自动得到 x-ui 当前最新的全部代理配置。

核心目标：

- 每个可发布 Inbound 拥有独立 PublicEndpoint。
- PublicEndpoint 的 hostname 和公网 path 可以随机轮换。
- Xray 内部监听地址、端口、UUID/密码和内部 XHTTP path 尽量保持不变。
- Caddy 负责公网 hostname/path 到本地 Inbound 的转发与 path rewrite。
- x-ui 提供稳定的 `GET /sub/:token` 客户端订阅。
- 单节点和全部节点都支持安全轮换、健康检查与回滚。

## 2. 非目标

- 不从第三方 URL 拉取别人的订阅。
- 不在 V1 实现 Clash/Mihomo YAML、HWID、复杂多租户订阅。
- 不在日常轮换中调用 Cloudflare DNS API。
- 不为了轮换公网地址而修改 Inbound UUID、内部端口或内部 XHTTP path。
- 不整体移植 3x-ui，只参考其服务端链接生成、XHTTP 参数序列化和 raw subscription 思路。

## 3. 当前 x-ui 基线

- `database/model/model.go` 的 `Inbound` 保存真实 Xray 配置：`Listen`、`Port`、`Protocol`、`Settings`、`StreamSettings` 等。
- `web/service/xray.go` 从所有启用 Inbound 生成实际 Xray 配置。
- `web/assets/js/model/xray.js` 已支持 VLESS/XHTTP 的 `type=xhttp`、`path`、`host`、`mode` 分享参数。
- 分享链接目前主要由浏览器端 JavaScript 生成，订阅需要增加 Go 后端统一生成器。
- `web/service/caddy.go` 已具备 validate、backup、write、reload 和 reload 失败回滚能力，应直接复用。

## 4. 总体架构

```text
                 x-ui
                  |
             Inbound #1
       真实 Xray 内部配置
                  |
      127.0.0.1:26417
      VLESS / XHTTP
      internalPath=/q8Fa72Lm9x
                  |
                  v
           PublicEndpoint
      host=d8k2m9xq.asdasdasdas.shop
      port=443
      path=/P9xKa2LmQ7
      security=tls
      status=active
                  |
       +----------+----------+
       |                     |
       v                     v
     Caddy             SubscriptionService
public -> internal            |
                              v
                       /sub/<stable-token>
                              |
                              v
                          客户端更新
```

原则：

- Inbound 是 Xray 内部配置的事实来源。
- PublicEndpoint 是客户端公网连接信息的事实来源。
- Subscription 不保存第二份完整节点配置，而是动态读取 Inbound + active PublicEndpoint 生成。
- Caddy route 由 PublicEndpoint 派生。
- 一个 Inbound 正常只有一个 active Endpoint；轮换期间允许 pending / active / draining 并存。

## 5. 数据模型

### 5.1 PublicEndpoint

新增模型，概念字段：

```go
type PublicEndpoint struct {
    Id        int
    InboundId int

    Host     string
    Port     int
    Path     string
    Security string
    SNI      string

    Status    string
    CreatedAt int64
    RetireAt  int64
}
```

状态：

- `pending`：新生成，尚未正式发布。
- `active`：当前订阅应输出的 Endpoint。
- `draining`：旧 Endpoint，暂时继续由 Caddy 接受，但不再出现在新订阅。
- `retired`：已退出，不再生成 Caddy route。

建议约束：

- `Host + Path` 唯一。
- 业务层保证同一 Inbound 最多一个 active Endpoint。
- 删除 Inbound 时同步清理 Endpoint。

### 5.2 Inbound 发布开关

建议给 Inbound 增加 `Publish bool`。订阅默认包含：

```text
Enable == true
AND Publish == true
AND 存在 active PublicEndpoint
AND 协议支持生成分享链接
```

### 5.3 Subscription 设置

V1 只提供一个默认订阅，复用现有 `Setting` 表：

```text
subscriptionEnable
subscriptionToken
subscriptionTitle
publicBaseDomain
publicPort
hostRandomLength
pathRandomLength
endpointDrainSeconds
```

示例：

```text
publicBaseDomain     = asdasdasdas.shop
publicPort           = 443
hostRandomLength     = 10
pathRandomLength     = 16
endpointDrainSeconds = 1800
```

以后需要多个订阅分组时，再增加 Subscription / SubscriptionInbound 关联表。

## 6. 安全随机值

当前 `util/random/random.go` 使用 `math/rand`。普通 UI 随机值可以继续用，但以下值新增 `crypto/rand` 实现：

- subscription token
- public hostname label
- public path

建议：hostname 使用 `[a-z0-9]` 10-14 位；path 使用 `[A-Za-z0-9]` 16 位左右；subscription token 至少 32 字节随机数据并使用 base64url 或 hex 编码。

订阅 token 视为 bearer secret：不写日志、只在登录管理页展示、允许手动重新生成。

## 7. Go LinkService

新增后端统一分享链接生成器，例如 `web/service/link.go`：

```go
GenerateInboundLink(inbound, endpoint) (string, error)
```

职责：

- 从 Inbound 读取协议、UUID/密码、streamSettings 等内部配置。
- 用 PublicEndpoint 覆盖客户端侧 address、port、path、TLS/security、SNI/Host。
- V1 支持 VMess、VLESS、Trojan、Shadowsocks。
- VLESS/XHTTP 正确输出 `type=xhttp`、公网 `path`、`host`、`mode` 和客户端需要的 TLS 参数。

当前前端 `genVmessLink`、`genVLESSLink`、`genSSLink`、`genTrojanLink` 作为行为基线，同时参考 3x-ui 的服务端 link generator。

长期应让“复制链接 / 二维码 / 订阅”全部使用同一个后端 LinkService，避免前后端两套协议序列化逻辑漂移。

## 8. 客户端订阅接口

新增公开接口：

```text
GET /sub/:token
```

处理流程：

```text
校验 token
 -> 读取可发布 Inbound
 -> 读取各 Inbound 的 active PublicEndpoint
 -> LinkService 生成分享链接
 -> 每行一个链接
 -> Base64
 -> 返回客户端
```

响应至少设置：

```text
Content-Type: text/plain; charset=utf-8
Cache-Control: no-store
```

订阅 URL 长期稳定；节点 hostname/path 可以变化，但 token 默认保持不变。

## 9. Caddy 管理

### 9.1 Wildcard

部署前置条件已经是：

```text
*.asdasdasdas.shop -> origin
```

Caddy 使用对应 wildcard origin certificate，并长期接受 `*.asdasdasdas.shop`，因此每次 hostname 轮换不需要改 DNS 或证书。

### 9.2 Managed block

自动化不覆盖整个 Caddyfile，只维护：

```text
# BEGIN XUI MANAGED ENDPOINTS

... generated routes ...

# END XUI MANAGED ENDPOINTS
```

Managed block 之外的手工配置必须原样保留。更新继续复用现有 `CaddyService` 的 validate -> backup/write -> reload -> rollback。

### 9.3 XHTTP path rewrite

示例：

```text
PublicEndpoint:
  host = d8k2m9xq.asdasdasdas.shop
  path = /P9xKa2LmQ7

Inbound:
  listen = 127.0.0.1
  port = 26417
  internal path = /q8Fa72Lm9x
```

Caddy 要把公网 path prefix 替换为内部 path prefix，同时保留后续 suffix：

```text
/P9xKa2LmQ7/<suffix>
       ->
/q8Fa72Lm9x/<suffix>
```

实现时使用 strip public prefix + prepend internal prefix 或等价方式，并增加 suffix 测试。

## 10. 单节点轮换

```text
1. 读取当前 active Endpoint
2. crypto/rand 生成新 hostname + path
3. 创建 pending Endpoint
4. 渲染 Caddy：旧 active + 新 pending 同时存在
5. Caddy validate
6. Caddy save + reload
7. 对新 Endpoint 做健康检查
8. 数据库事务：pending -> active；old active -> draining
9. /sub/:token 从此自动输出新 Endpoint
10. draining 到期后移除旧 route 并标记 retired
```

失败处理：

- validate 失败：不修改当前 active。
- reload 失败：依赖现有 CaddyService 回滚。
- reload 成功但健康检查失败：恢复前一版 managed block，清理 pending。
- DB active 切换失败：恢复旧 Caddy managed block，避免 Caddy 与订阅状态分裂。

正常轮换不修改 Cloudflare DNS、UUID、内部端口、内部 XHTTP path，因此无需 Restart Xray。

## 11. 批量轮换

“全部随机更换”必须做成批量事务式流程，而不是逐个节点独立 reload：

```text
读取全部待轮换 Inbound
 -> 为每个生成 pending Endpoint
 -> 一次渲染所有旧 + 新 route
 -> 一次 validate
 -> 一次 reload
 -> 健康检查全部新 Endpoint
 -> 全部成功后一次 DB transaction 切 active/draining
 -> 下一次订阅刷新立即得到全部新配置
```

V1 默认任意关键 Endpoint 失败就整批回滚，避免出现不可预期的半新半旧状态。

## 12. 健康检查

发布前至少验证：

- 新随机 hostname 能通过 TLS/CDN 到达 Caddy。
- host/path matcher 命中正确 route。
- XHTTP 公网 path 正确映射到内部 path。
- 后端本地端口可达。

检查应有明确超时、有限重试，并避免在错误信息中泄露 token/credential。

如果普通 HTTP 状态不足以验证 XHTTP，需要基于当前固定 Xray 26.3.27 增加最小 transport-aware probe，并用测试确认。

## 13. Portal / Tunnel 联动

当前部署可能在同一公网 hostname 下同时存在普通 VLESS/XHTTP 和 Portal XHTTP：

```text
普通 route   /public-path -> 127.0.0.1:26417
Portal route /portal-path -> 127.0.0.1:26418
```

因此必须区分“跟随 hostname 轮换的 route”和“是否发布到客户端订阅”：

- 普通代理 Inbound route 可以进入 Subscription。
- Portal/Tunnel route 可以跟随同一 hostname group 一起轮换，但绝不进入客户端订阅。

实现时预留 route group / binding 概念，并根据当前 Tunnel 的 `RemoteAddress`、`PortalListenPort`、`XHttpPath` 数据流确定最终绑定方式。

## 14. 管理 API

建议登录保护下增加：

```text
GET  /xui/subscription/status
POST /xui/subscription/settings
POST /xui/subscription/token/regenerate

GET  /xui/endpoint/list
POST /xui/endpoint/:inboundId/rotate
POST /xui/endpoint/rotate-all
POST /xui/endpoint/:id/retire
```

公开接口只有：

```text
GET /sub/:token
```

## 15. 管理页面

新增“订阅 / 公网入口”页面，展示稳定订阅 URL、基础域名、随机长度、drain 时间，以及每个可发布代理的当前公网 Endpoint。

每行至少支持：

- 是否发布到订阅。
- 随机更换。
- 复制当前分享链接。
- 查看 active / pending / draining 状态。
- 手工 retire 旧 Endpoint。

页面顶部支持“全部随机更换”。

## 16. 首次启用与迁移

新增模型继续使用 GORM `AutoMigrate`。

首次启用时不要自动猜测 Caddyfile。提供显式初始化，让管理员为已有 Inbound 填写当前公网 Host、Path、Port、TLS/SNI，保存为第一条 active PublicEndpoint，并预览最终客户端分享链接。

## 17. 测试计划

### LinkService

- VLESS/XHTTP public path 覆盖 internal path。
- XHTTP `mode`、TLS、SNI/Host。
- VMess、Trojan、Shadowsocks。
- URL 编码、remark、IPv6 host:port。

### Subscription

- 只输出 Enable + Publish + active endpoint。
- `a.y.z -> d.y.z` 后订阅立即反映新值。
- draining Endpoint 不进入新订阅。
- 多 Inbound 顺序稳定。
- Base64 可正确解码为多行链接。
- 无效 token 被拒绝。
- 响应禁止缓存。

### Caddy renderer

- Managed block 外内容不变。
- 单节点 / 多节点 / 同 hostname 多 path。
- public/internal prefix rewrite 保留 suffix。
- pending + active + draining 并存。
- retired 不生成 route。

### Rotation

- 单节点成功切换。
- validate / reload / health check / DB 切换任一失败都能回滚。
- rotate-all 全成功只 reload 一次。
- rotate-all 任意失败保持旧 active。

## 18. 实施阶段

### Phase 1：服务端链接与订阅

- 实现 `crypto/rand` 工具。
- 实现 Go LinkService。
- 增加订阅设置和稳定 token。
- 实现 `GET /sub/:token`。
- 增加单元测试。

目标：客户端先能用一个稳定 URL 同步多个 x-ui 代理配置。

### Phase 2：PublicEndpoint 与 Caddy

- 新增 PublicEndpoint 模型。
- Caddy managed block renderer。
- active/draining/retired 状态。
- 健康检查和回滚。

目标：公网 Host/Path 与 Xray 内部配置解耦。

### Phase 3：自动随机轮换

- 单节点 rotate。
- rotate-all。
- drain/retire。
- UI 操作和状态展示。

目标：完成“随机 hostname + 随机 path + 订阅自动同步”的一键工作流。

### Phase 4：Portal/Tunnel 联动

- 明确 Portal route 与普通 Inbound 的 group/binding。
- 同 hostname 的相关 route 原子轮换。
- Portal 不进入客户端订阅。

## 19. V1 验收标准

1. 多个启用代理配置可以通过一个固定订阅 URL 一次返回。
2. 客户端无需修改订阅 URL。
3. 单个代理公网 hostname/path 可随机生成并安全切换。
4. `a.y.z -> d.y.z` 后，下一次客户端刷新订阅只拿到新的 active Endpoint。
5. 旧 Endpoint 在 drain 窗口内继续可用，但不再发布给新订阅。
6. Caddy 修改或新 Endpoint 验证失败时，当前 active 配置保持可用。
7. 批量轮换只执行一次 Caddy reload，并且默认全部成功才切换。
8. 正常轮换无需 Cloudflare API，也无需重启 Xray。
9. XHTTP path rewrite 保留请求 suffix。
10. LinkService、Subscription、Caddy renderer、rotation rollback 都有自动化测试。
