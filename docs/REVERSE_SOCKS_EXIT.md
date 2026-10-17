# RSX2 反向 SOCKS5 / WARP（仅两端 frpc 定制）

本仓库 x-ui 负责 **境外 VPS**。境外 Go Agent 通过本地 frpc XTCP Visitor TCP 端口 **主动连接境内** Python 3.13 SOCKS5 服务；境内 frpc 仍为 XTCP Proxy。第三方 frps 不需任何改动。

要求两端使用 [已经合并到 frp/dev 的定制 frpc QUIC DATAGRAM](https://github.com/qqq694637644/frp/pull/1)。Go Agent 与 [nat_node](https://github.com/qqq694637644/nat_node/pull/1) 使用 **RSX2**，故意不兼容旧 RSX1，无 UDP-over-TCP 兜底。

## TCP / UDP

- TCP：境内 SOCKS5 CONNECT -> Python RSX2 TCP 多路复用 -> frpc XTCP QUIC Stream -> Go Agent -> **WARP SOCKS5 CONNECT** -> 目标。
- UDP：境内 SOCKS5 UDP ASSOCIATE -> Python UDP 127.0.0.1:9314 -> frpc QUIC DATAGRAM -> Go UDP 127.0.0.1:6002 -> **WARP SOCKS5 UDP ASSOCIATE** -> 目标。每个 UDP 会话独立保留 WARP TCP 控制连接。
- WARP SOCKS5 只能配置 127.0.0.1:PORT；不可用时返回失败。Go Agent 没有直连目标的代码路径，UDP 也不会退回 XTCP TCP 字节流。
- UDP 封装为 4 字节 Session ID + 标准 SOCKS5 UDP 报文，整个 DATAGRAM 最大 1100 字节，超限丢弃、不重传；不支持任意尺寸的 UDP 或 HTTP/3 初始 QUIC 报文。
- **TCP 视频长连接已改成逐会话 32 帧信用窗口**：只有接收端完成写入，才批量（每 8 帧）返还发送额度；正常慢客户端不再因为队列满就断流。
- WARP SOCKS5 UDP ASSOCIATE 返回的 relay 地址必须是 IPv4 本地回环地址；非本地地址会被拒绝，防止 UDP 暴露 VPS 的公网源地址。

## x-ui Web 配置

登录面板，左侧打开 **反向 SOCKS5 / WARP**（`/xui/reverse-socks`）。**本页面现在直接管理一份独立的自定义 frpc，而不再只是生成 TOML 让用户手动启动。**

先配置 **自定义 frpc 进程与服务端配置**：

| 字段 | 默认/说明 |
| --- | --- |
| 自定义 frpc 可执行文件 | `/opt/customfrp/frpc_linux_amd64`（用户已放置的私有 frpc） |
| x-ui 专用 frpc.toml | `/etc/x-ui/reverse-socks/frpc.toml`（由 x-ui 自动生成和覆盖） |
| frps IP/域名与 TCP 端口 | 按服务商实际信息填写；端口默认 `7000` |
| frps Auth Token | 服务商提供；单用户 Web 页面直接显示和编辑实际值 |
| 境内 XTCP Proxy 名称与 secretKey | 默认 `rsx2_node`，密钥与境内 Proxy 完全相同 |
| STUN 服务器 | **域名或 IPv4:端口**，例如 `stun.miwifi.com:3478` 或 `74.125.250.129:19302` |

再配置出口通道：

- XTCP Visitor TCP：127.0.0.1:6000
- Go UDP 本地绑定（frpc datagram.peerAddr）：127.0.0.1:6002
- frpc UDP 本地接收（frpc datagram.listenAddr）：127.0.0.1:6001
- WARP SOCKS5 地址（必填、本地回环）：127.0.0.1:40000，用户名/密码可选
- RSX2 共享密钥（至少 16 字符，与境内 REVERSE_SOCKS_TOKEN 一致）
- 启用/停止、保存并立即应用、独立的 XTCP/RSX2 状态与 WARP CONNECT/UDP ASSOCIATE 探测状态（每 30 秒）。健康探测并不能证明最终 WARP 公网 IP。
- 受管 **frpc** 单独显示运行状态、PID 和最近退出错误；提供 **校验当前填写的配置（不保存）** 与 **重启 frpc + Agent** 按钮。
- 下方 **frpc.toml 完整配置预览** 实时显示当前表单填写的实际 Token、密钥及 WARP SOCKS5 密码，真实文件仍由后端生成。
- 此为单用户 Web 管理页面：**不再隐藏已保存的密码和密钥，也不再使用“留空保持原值”**。重新加载页面会填入真实值，保存时空字符串即清空。仅已登录的 x-ui 会话可以访问此接口，请务必保护面板账户与 HTTPS。
- **校验按钮读取当前表单，包括尚未保存的 STUN 地址**，只验证 frpc 和 TOML，不保存 SQLite、不替换线上文件，也不重启进程。原实现仅校验旧数据库值，会导致数字 IP 已填写但仍提示旧域名格式错误。

### 管理进程与严格校验

启用时 x-ui 执行如下步骤，顺序不可反转：

1. 验证配置字段、**指定的绝对 frpc 路径是否存在且可执行**、WARP 只能使用 `127.0.0.1`，并将完整 TOML 写入临时 `0600` 文件；STUN 接受域名或 IPv4:端口；
2. 调用指定的 frpc 执行 `verify -c <临时配置>`；然后再用 **删除 `udpProxyURL`**、**删除 `transport.proxyURL`** 的两份不安全配置分别执行 `verify -c`，确认这个 frpc **确实拒绝**缺少隐私必需字段的配置。旧版/官方 frpc 若忽略新字段，即使第一轮验证成功，也会被拒绝；
3. 停止本面板原来的 Go Agent 与**自己启动**的 frpc，先检测 Visitor TCP 端口是否被其他进程占用；
4. 将验证过的文件原子替换为目标 `frpc.toml`（`0600`），启动受管 frpc，再启动 Go Exit Agent。

**如果校验失败，旧的受管进程与数据库配置不会被改变；如果更新后启动失败，两组件保持失败/停止状态，绝不回退为未经 WARP 的原生 UDP。** GET 接口直接返回已保存的 Token、WARP 密码和 XTCP 密钥以供单用户编辑，不再返回只有是否设置的布尔标志；不要在多人共用的面板启用此模式。

页面「停止」只停止 x-ui 自己启动的 frpc（优先 SIGTERM，超时强制结束**该 PID**），不会 `pkill frpc`、不会接管服务商或其他 systemd/frpc 实例。面板重新启动后会从本机 SQLite 恢复已启用的受管 frpc 和 Go Agent。原有不含 frpc 配置信息的旧版 SQLite 配置**不能自动启用**，必须通过本页面填写后重新验证，这是故意的破坏式更新。

执行文件/配置文件建议仅允许运行 x-ui 的账户访问；不要将密钥文件交给其他用户。如果 `/opt/customfrp/frpc_linux_amd64` 没有可执行权限，先在 VPS 执行 `chmod 755 /opt/customfrp/frpc_linux_amd64`。若 x-ui 不是以有权写入 `/etc/x-ui/reverse-socks` 的账户运行，需要先调整专用目录权限或在 Web 指定其他绝对路径。

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

如需自定义定期 WARP 出口连通探测目标，两种 CLI 都支持 `-warp-probe 1.1.1.1:443`，面板也有对应输入框。探测分别尝试真实的 SOCKS5 CONNECT 和 UDP ASSOCIATE，不会使用 VPS 直连，不能代替出口 IP 核验。

## 境外 frpc 自动生成配置参考（既有第三方 frps 保持不变）

**仅配置 `transport.proxyURL` 不够。** XTCP 默认 STUN/打洞仍使用原生 UDP Socket；为阻止泄漏，必须使用本 fork 的 `natTraversal.udpProxyURL` 将 STUN、打洞及 QUIC 改为本地 WARP SOCKS5 UDP ASSOCIATE。两端代理字段必须指向同一个回环 SOCKS5 端口，否则新 frpc 拒绝启动对应 Visitor。下面仅展示 x-ui 写入的 TOML 格式，**不应手动覆盖受管文件**：

```toml
serverAddr = "REPLACE_WITH_FRPS_IP"
serverPort = 7000
auth.method = "token"
auth.token = "REPLACE_WITH_FRPS_TOKEN"
transport.protocol = "tcp"
transport.wireProtocol = "v1"
transport.proxyURL = "socks5://127.0.0.1:40000"
natHoleSTUNServer = "REPLACE_WITH_VERIFIED_STUN_IP:3478"

[[visitors]]
name = "rsx2_outside"
type = "xtcp"
serverName = "rsx2_node"
secretKey = "same-xtcp-secret-as-mainland"
protocol = "quic"
keepTunnelOpen = true
minRetryInterval = 10
maxRetriesAnHour = 120
bindAddr = "127.0.0.1"
bindPort = 6000

[visitors.datagram]
listenAddr = "127.0.0.1:6001"
peerAddr = "127.0.0.1:6002"

[visitors.natTraversal]
disableAssistedAddrs = true
udpProxyURL = "socks5://127.0.0.1:40000"
```

**STUN 域名现在允许直接填写**，如 `stun.miwifi.com:3478`，但 frpc 在主机解析域名时可能使用 VPS 系统 DNS（这段 DNS 查询不一定经 WARP SOCKS5 代理）。更注重 DNS 不暴露时可改用经验证的数字 IPv4。SOCKS5 UDP 中继无法控制 IP TTL，也无法在复杂 NAT 下创建多个原生端口，这两种特殊打洞优化会降级或拒绝，绝不直连回退。真实 WARP+ 是否容许 P2P 入站仍需测试。

境内 frpc、Python 端口和部署说明见 [nat_node README](https://github.com/qqq694637644/nat_node/blob/gpt/xtcp-reverse-socks5/README.md)。

## 测试

```sh
go test ./reverseexit
go test ./web/service -run 'TestReverseSocksWebConfigSecretsAndLifecycle|TestReverseSocksManagedFRPCVerifyLifecycleAndIsolation|TestManagedFRPCWithoutDatabase|TestManagedFRPCRealForkPrivacyValidation'
go build -o reverse-socks-exit ./cmd/reverse-socks-exit
```

进程管理测试使用实际运行的 Go mock frpc 可执行文件，验证正/反向 `verify`、PID 生命周期、未经管理的其他进程不受影响、配置恢复和 `0600` 权限。设定 `REVERSE_SOCKS_TEST_REAL_FRPC_BIN` 可以在测试中额外验证你私有编译的真实 frpc 可执行文件。另在 nat_node 仓库设置 REVERSE_SOCKS_EXIT_BIN 指向刚编译的 Go Agent，执行 python3.13 -m unittest -v tests.test_end_to_end。真实跨境性能、第三方 frps/WARP+ 真实网络打洞与 VPS 出口仍需实地验证。
