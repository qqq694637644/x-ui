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
- PublicEndpoint 只随机轮换二级子域名（本文指 `<random>.asdasdasdas.shop` 这一层）；path 不随机，始终沿用对应 Inbound 当前配置。
- V1 只托管 `VLESS + XHTTP + listen=127.0.0.1 + security=none + 单客户端`，其他协议/传输/内部 TLS 一律拒绝发布。
- Xray 内部监听地址、端口、UUID 和内部 XHTTP path 保持不变。
- Caddy 负责随机公网 hostname + 固定 path 到本地 Inbound 的转发。
- x-ui 提供稳定的 `GET /sub/:token` 客户端订阅。
- V1 的主要操作是“一键全部随机”：一次为所有已发布代理生成新的随机子域名，并以一个批次完成验证、切换和回滚。

## 2. 非目标

- 不从第三方 URL 拉取别人的订阅。
- 不在 V1 实现 Clash/Mihomo YAML、HWID、复杂多租户订阅。
- 不在日常轮换中调用 Cloudflare DNS API。
- 不为了轮换公网地址而修改 Inbound UUID、内部端口或 XHTTP path；path 在轮换前后保持不变。
- 不兼容 VMess、Trojan、Shadowsocks、WS、HTTP/H2、gRPC、非本地监听、Inbound TLS/REALITY 或自定义公网 SNI 的托管发布。
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
- PublicEndpoint 只保存客户端公网 Host/Port 和生命周期；公网 TLS/SNI 固定为当前 Host，传输 path 继续由 Inbound 的 StreamSettings 提供。
- Subscription 不保存第二份完整节点配置，而是动态读取 Inbound + active PublicEndpoint 生成。
- Caddy route 由 PublicEndpoint 派生。
- 一个 Inbound 在数据库层最多只有一个 active Endpoint；轮换期间允许 pending / active / draining 并存。

## 5. 数据模型

### 5.1 PublicEndpoint

新增模型，概念字段：

```go
type PublicEndpoint struct {
    Id        int
    InboundId int

    Host     string
    Port     int

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

- `Host` 在全部 Endpoint 历史中必须唯一；retired hostname 永不重新分配。
- SQLite 使用 partial unique index 保证 `UNIQUE(inbound_id) WHERE status='active'`；业务层读取 active 时也要求数量严格等于 1。
- 失败的 pending 不 DELETE，而是转成 retired，确保随机生成过的 hostname 也不会被重新分配。
- 删除 Inbound 时保留 Endpoint 历史，继续占用已经使用过的 hostname。

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
subscriptionBaseUrl
publicBaseDomain
publicPort
hostRandomLength
endpointDrainSeconds
```

示例：

```text
subscriptionBaseUrl  = https://sub.example.net/xui
publicBaseDomain     = asdasdasdas.shop
publicPort           = 443
hostRandomLength     = 10
endpointDrainSeconds = 1800
```

以后需要多个订阅分组时，再增加 Subscription / SubscriptionInbound 关联表。

## 6. 安全随机值

当前 `util/random/random.go` 使用 `math/rand`。普通 UI 随机值可以继续用，但以下值必须使用 `crypto/rand`：

- subscription token
- public hostname label
建议：随机子域名 label 使用 `[a-z0-9]` 10-14 位，最终生成 `<random>.<publicBaseDomain>`；subscription token 至少 32 字节随机数据并使用 base64url 或 hex 编码。XHTTP/WS 等 path 不参与随机化。

订阅 token 视为 bearer secret：不写日志、只在登录管理页展示、允许手动重新生成。

## 7. Go LinkService

新增后端统一分享链接生成器，例如 `web/service/link.go`：

```go
GenerateInboundLink(inbound, endpoint) (string, error)
```

职责（严格模式）：

- 只接受 VLESS + XHTTP + `127.0.0.1` + `security=none` + 单客户端。
- 从 Inbound 读取 UUID、固定 XHTTP path 和 mode。
- 用 PublicEndpoint 覆盖客户端侧 address/port，并固定输出 `security=tls`、`host=<PublicEndpoint.Host>`、`sni=<PublicEndpoint.Host>`。
- 任意不满足严格托管约束的配置直接报错，不做协议兼容或降级。

当前前端 VLESS/XHTTP 分享链接和 3x-ui 的服务端 VLESS/XHTTP 生成逻辑作为行为参考。

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

订阅 URL 使用显式配置的 `subscriptionBaseUrl + /sub/:token`，不依赖管理页面当前的 `location.origin`。节点 hostname 可以变化，但 path 保持与 Inbound 当前配置一致，token 默认保持不变。

严格部署规则：管理面板和稳定订阅入口的 hostname 都不得属于 managed `publicBaseDomain` 区域。保存 Endpoint 设置时，如果当前管理请求 Host 落在该 zone，直接拒绝；`subscriptionBaseUrl` 的 hostname 落在该 zone 也直接拒绝。这样破坏式接管 `*.publicBaseDomain` 时不会把面板/订阅入口一起删除。

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

对于当前 `publicBaseDomain`，x-ui 采用破坏式所有权：首次托管时删除该域名下旧的 exact-host 站点块以及旧 `*.publicBaseDomain` wildcard 站点块，之后只保留数据库生成的唯一 managed wildcard 站点。其他无关域名的 Caddy 配置保留。更新继续复用现有 `CaddyService` 的 validate -> backup/write -> reload -> rollback。

启动恢复采用 fail-closed：如果数据库存在 active/draining Endpoint，或上次中断留下 pending Endpoint，启动时必须先把 pending 转 retired 并重新生成/应用完整 managed Caddy。reconcile 失败时标记 managed state unhealthy；公开订阅返回 503，轮换和首次初始化也拒绝继续，直到一次完整 Caddy 同步成功。

### 9.3 固定 path

轮换只改变 hostname，不改变 path。Caddy 直接使用 Inbound 当前 transport path 作为 matcher，不做随机 path rewrite。

示例：

```text
轮换前：
  a.y.z/q8Fa72Lm9x* -> 127.0.0.1:26417

轮换后：
  d.y.z/q8Fa72Lm9x* -> 127.0.0.1:26417
```

这样客户端订阅中的 `path=/q8Fa72Lm9x` 始终不变，只更新 server/host 字段。Portal XHTTP 同理保留自己的固定 path。

## 10. 一键全部随机

V1 的主操作不是逐条修改，而是“全部随机一次”。点击一次后，为所有 `Enable + Publish` 的代理分别生成新的随机子域名，所有 path 保持原值。

```text
1. 读取全部当前 active Endpoint
2. 为每个待发布 Inbound 用 crypto/rand 生成新的随机子域名
3. 批量创建 pending Endpoint
4. 一次渲染 Caddy：全部旧 active + 全部新 pending 同时存在
5. Caddy validate
6. Caddy save + reload（只 reload 一次）
7. 健康检查全部新 hostname + 原 path
8. 全部成功后执行一次数据库事务：全部 pending -> active；全部 old active -> draining
9. /sub/:token 从此一次性输出整批新 hostname，所有 path 保持不变
10. draining 到期后批量移除旧 route 并标记 retired
```

失败处理：

- validate 失败：不修改当前 active。
- reload 失败：依赖现有 CaddyService 回滚。
- reload 成功但健康检查失败：恢复前一版 managed block，pending 全部转 retired 并永久保留 hostname 历史。
- DB active 切换失败：恢复旧 Caddy managed block，避免 Caddy 与订阅状态分裂。

正常轮换不修改 Cloudflare DNS、UUID、内部端口、XHTTP path，因此无需 Restart Xray。

## 11. 批量原子性

“全部随机一次”必须做成批量事务式流程，而不是逐个节点独立 reload：

```text
读取全部待轮换 Inbound
 -> 为每个生成新 hostname 的 pending Endpoint
 -> 一次渲染所有旧 + 新 route
 -> 一次 validate
 -> 一次 reload
 -> 健康检查全部新 Endpoint
 -> 全部成功后一次 DB transaction 切 active/draining
 -> 下一次订阅刷新立即得到全部新配置
```

V1 默认任意关键 Endpoint 失败就整批回滚，避免出现不可预期的半新半旧状态。

## 12. 健康检查

发布前必须做真实传输链路探测，而不是只检查 Caddy 自己返回 204：

- 临时启动一个使用同 UUID/path/mode 的 Xray VLESS/XHTTP 客户端。
- 临时客户端通过新 hostname 建立 `TLS/XHTTP -> Cloudflare -> Caddy -> h2c -> 本地 Xray` 连接。
- 再通过该真实代理链路访问仅用于验证的 204 URL；只有整条 VLESS/XHTTP 链路可用才算成功。
- 临时 Xray 进程、配置文件和本地代理端口均在探测后清理。
- 同 hostname group 关联的 Portal XHTTP route 至少检查对应 `PortalListenPort` 是否正在 `127.0.0.1` 监听；任一 Portal 监听不可达则整批轮换失败并回滚。

检查应有明确超时、有限重试，并避免在错误信息中泄露 token/credential。

探测配置必须用当前固定 Xray 26.3.27 做 `run -test` 自动化校验。

## 13. Portal / Tunnel 联动

当前部署可能在同一公网 hostname 下同时存在普通 VLESS/XHTTP 和 Portal XHTTP：

```text
普通 route   /public-path -> 127.0.0.1:26417
Portal route /portal-path -> 127.0.0.1:26418
```

因此必须区分“跟随 hostname 轮换的 route”和“是否发布到客户端订阅”：

- 普通代理 Inbound route 可以进入 Subscription。
- Portal/Tunnel route 可以跟随同一 hostname group 一起轮换，但绝不进入客户端订阅。
- hostname 切换前必须检查关联 Portal XHTTP 的本地 `PortalListenPort`；普通代理真实链路成功但 Portal 本地监听失败时不得提交轮换。

实现时预留 route group / binding 概念，并根据当前 Tunnel 的 `RemoteAddress`、`PortalListenPort`、`XHttpPath` 数据流确定最终绑定方式。

## 14. 管理 API

建议登录保护下增加：

```text
GET  /xui/subscription/status
POST /xui/subscription/settings
POST /xui/subscription/token/regenerate

GET  /xui/endpoint/list
POST /xui/endpoint/init-batch
POST /xui/endpoint/rotate-all
POST /xui/endpoint/:id/retire
```

公开接口只有：

```text
GET /sub/:token
```

## 15. 管理页面

新增“订阅 / 公网入口”页面，展示稳定订阅 URL、基础域名、子域名随机长度、drain 时间，以及每个严格可发布 VLESS/XHTTP 的当前公网 Endpoint 和固定 path。

每行至少支持：

- 是否发布到订阅。
- 复制当前分享链接。
- 查看 active / pending / draining 状态。
- 手工 retire 旧 Endpoint。

页面顶部提供“批量初始化当前入口”和主要操作“全部随机一次”。首次接管时批量初始化会一次列出所有启用且符合严格托管约束、尚未初始化的 Inbound，要求管理员全部填写当前 Host 后再提交；不再提供单节点 Initialize。

## 16. 首次启用与迁移

新增模型继续使用 GORM `AutoMigrate`。

首次启用时不要自动猜测 Inbound 与公网 Host 的关系。首次接管必须是批量原子操作：管理员一次填写所有启用且符合严格 VLESS/XHTTP 约束的 Inbound -> 当前公网 Host/Port 映射，服务端先在一个数据库事务中批量写 pending，然后一次生成完整 Caddy、一次 validate/reload，逐项完成普通 VLESS/XHTTP 与关联 Portal 健康检查，全部成功后再用一个数据库事务统一切 active。任何节点失败都恢复接管前 Caddy，全部 pending 转 retired。单节点 Initialize 不存在。

## 17. 测试计划

### LinkService

- VLESS/XHTTP path 保持与 Inbound StreamSettings 一致。
- XHTTP `mode`、公网 TLS、`SNI=Host`。
- 非 VLESS、非 XHTTP、内部 TLS、非本地监听、多客户端等严格拒绝。
- URL 编码和 remark。

### Subscription

- 只输出 Enable + Publish + active endpoint。
- `a.y.z -> d.y.z` 后订阅立即反映新值。
- draining Endpoint 不进入新订阅。
- 任意已发布节点缺 endpoint 或链接生成失败时，整个订阅请求失败，不返回残缺订阅。
- 任意已发布 Inbound 的 active Endpoint 数量不是恰好 1 时整个订阅失败。
- managed startup reconcile 失败时订阅返回 503。
- 稳定订阅 URL 使用显式 `subscriptionBaseUrl`，并拒绝落在 managed zone。
- 多 Inbound 顺序稳定。
- Base64 可正确解码为多行链接。
- 无效 token 被拒绝。
- 响应禁止缓存。

### Caddy renderer

- 当前基础域名下旧 exact-host/wildcard 块被破坏式清理，无关域名配置保持不变。
- 单节点 / 多节点 / 同 hostname 多 path。
- hostname 变化时固定 path matcher 保持不变。
- 同 Host 下较长 path 优先于较短前缀 path。
- pending + active + draining 并存。
- retired 不生成 route。

### Rotation

- 全部 hostname 一次性成功切换，path 全部保持不变。
- validate / reload / health check / DB 切换任一失败都能回滚。
- rotate-all 全成功只 reload 一次。
- rotate-all 任意失败保持旧 active。
- Caddy apply 成功但真实 XHTTP 探测失败时恢复旧 Caddy。
- DB 状态切换失败时恢复旧 Caddy，并把本批 pending 转为 retired 保留 hostname 历史。
- 首次批量初始化少录任一启用且符合托管条件的 Inbound 时，在 Caddy apply 之前拒绝。
- 首次批量初始化全部映射只 apply/reload 一次，全部健康后才统一 active。
- 关联 PortalListenPort 不可达时整批回滚。
- 轮换失败产生的 pending 转 retired，不删除 hostname 历史。

## 18. 实施阶段

### Phase 1：服务端链接与订阅

- 实现 `crypto/rand` 工具。
- 实现 Go LinkService。
- 增加订阅设置和稳定 token。
- 实现 `GET /sub/:token`。
- 增加单元测试。

目标：客户端先能用一个稳定 URL 同步多个严格托管的 VLESS/XHTTP 配置；任一节点异常时 fail closed。

### Phase 2：PublicEndpoint 与 Caddy

- 新增 PublicEndpoint 模型。
- Caddy managed block renderer。
- active/draining/retired 状态。
- 健康检查和回滚。

目标：公网 Host 与固定的 `127.0.0.1 + VLESS/XHTTP security=none` 内部配置解耦，同时保持 transport path 不变，并让 x-ui 成为该 wildcard Caddy 区域的唯一真源。

### Phase 3：自动随机轮换

- `rotate-all` 作为 V1 主要且默认的轮换入口。
- drain/retire。
- UI 操作和状态展示。

目标：完成“全部随机子域名一次 + 固定 path + 订阅自动同步”的一键工作流。

### Phase 4：Portal/Tunnel 联动

- 明确 Portal route 与普通 Inbound 的 group/binding。
- 同 hostname 的相关 route 原子轮换。
- Portal 不进入客户端订阅。

## 19. V1 验收标准

1. 多个启用代理配置可以通过一个固定订阅 URL 一次返回。
2. 客户端无需修改订阅 URL。
3. 一次操作可以为全部已发布代理生成新的随机子域名；所有 path 保持原值。
4. `a.y.z -> d.y.z` 后，下一次客户端刷新订阅只拿到新的 active Endpoint。
5. 旧 Endpoint 在 drain 窗口内继续可用，但不再发布给新订阅。
6. Caddy 修改或新 Endpoint 验证失败时，当前 active 配置保持可用。
7. 批量轮换只执行一次 Caddy reload，并且默认全部成功才切换。
8. 正常轮换无需 Cloudflare API，也无需重启 Xray。
9. 轮换 hostname 前后，XHTTP path 不发生变化；retired hostname 永不重新分配。
10. LinkService、Subscription、Caddy renderer、rotation rollback 都有自动化测试。
11. 只有真实 `VLESS/XHTTP -> Cloudflare -> Caddy -> h2c -> Xray` 探测成功，pending 才允许切 active。
