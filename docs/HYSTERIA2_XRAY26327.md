# Xray-core 26.3.27 原生 Hysteria2 / QUIC Brutal

## 目标与边界

境内 Windows 运行 **Xray-core v26.3.27**（不再运行 nat_node 的自定义 QUIC Listener），
本机 SOCKS5 接受浏览器 TCP 和 UDP，直接经 Hysteria2/QUIC 连接 VPS x-ui 现有 Xray 进程。

```text
Windows 浏览器 → 127.0.0.1:1080 Xray SOCKS5 (TCP + UDP)
              → Hysteria2 QUIC / TLS / Brutal (公网 IPv4 或 IPv6)
              → VPS x-ui 已有 Xray Hysteria2 入站
              → VPS 现有 routing（freedom / 现有 WARP SOCKS5 / block 等）
```

- **不创建**第二个代理协议、Go Agent、frpc、frps、STUN 或自定义 RSX3 数据流。
- 原 FRP/XTCP/WARP 页面、路由和进程 **全部保留**，不受启用 Hysteria2 影响。
- 本功能仍由现有 `/xui/inbounds` 入站管理，和普通入站一样使用原有持久化、Xray
  配置生成、重启回滚与流量统计。
- 这里是 Windows **主动连接** VPS，因此不依赖 Windows 的公网 IPv6/DDNS；
  如果 Windows 与 VPS 都可走 IPv6，可将客户端出站的
  `streamSettings.sockopt.domainStrategy` 设为 `UseIPv6` 并让 VPS 域名提供 AAAA
  记录。不要把需要 IPv6 的路由指向仅有 IPv4 的主机。

## VPS：创建普通的 Xray Hysteria2 入站

1. 确认现有 x-ui 的 Xray **确实为 26.3.27**。Release CI 固定构建
   `qqq694637644/Xray-core` 的 `d2758a0`。
2. 进入现有 **入站列表** → 新增 → 协议选 `hysteria`。
3. 监听地址填 `::`（如仅允许某个 IPv6 监听，也可填具体地址），UDP 端口例如
   `18443`。必须在 VPS 系统防火墙和云服务商安全组中开放 **UDP** 18443。
4. 认证：点击生成强随机 auth，保存后复制此值到 Windows Xray 客户端。
5. Brutal：配置 `brutalUp`（VPS 发送）和 `brutalDown`（VPS 接收协商提示）。
   面板强制生成 `finalmask.quicParams.congestion=force-brutal`，如果任何发送速率
   不合法会拒绝保存，**不会降级到 BBR**。这里采用 Xray 原生 Brutal 丢包补偿，
   不是旧定制 FRP 的 target/max 两参数硬上限。
6. TLS：使用现有证书文件的服务器路径（证书链及私钥必须能由 Xray 进程读取）。
   ALPN 固定 `h3`。不要把私钥放到 Git。
7. 保存和重启 Xray。已有模板内的 `outbounds`、`routing`（包括 WARP 分流）
   不会被本功能覆盖。

面板会通过已有入站数据库保存以下形状（此处是**一个入站对象**，不是覆盖整份 Xray 模板）：

```json
{
  "tag": "hy2-native-18443",
  "listen": "::",
  "port": 18443,
  "protocol": "hysteria",
  "settings": {
    "version": 2,
    "clients": [
      {
        "auth": "<强随机密钥>"
      }
    ]
  },
  "streamSettings": {
    "network": "hysteria",
    "security": "tls",
    "tlsSettings": {
      "alpn": ["h3"],
      "certificates": [
        {
          "certificateFile": "/path/to/fullchain.pem",
          "keyFile": "/path/to/privkey.pem"
        }
      ]
    },
    "hysteriaSettings": {
      "version": 2
    },
    "finalmask": {
      "quicParams": {
        "congestion": "force-brutal",
        "brutalUp": "16mbps",
        "brutalDown": "24mbps"
      }
    }
  }
}
```

**不需要**在服务端 `hysteriaSettings.auth` 再配置第二份密钥；
内核使用 `settings.clients[].auth` 进行用户认证。Windows 客户端才需要
`hysteriaSettings.auth`。

## Windows：仅需 Xray-core 26.3.27

使用与 VPS 版本匹配的 `xray.exe`，保存一份客户端配置文件，例如
`C:\xray\hysteria-client.json`。以下配置展示本地 SOCKS5 1080、Hysteria2
出站和双向 Brutal，不依赖任何 Python 包：

```json
{
  "log": {"loglevel": "warning"},
  "inbounds": [
    {
      "tag": "local-socks",
      "listen": "127.0.0.1",
      "port": 1080,
      "protocol": "socks",
      "settings": {"auth": "noauth", "udp": true, "ip": "127.0.0.1"}
    }
  ],
  "outbounds": [
    {
      "tag": "to-vps",
      "protocol": "hysteria",
      "settings": {
        "version": 2,
        "address": "vps.example.com",
        "port": 18443
      },
      "streamSettings": {
        "network": "hysteria",
        "security": "tls",
        "tlsSettings": {
          "serverName": "vps.example.com",
          "alpn": ["h3"]
        },
        "hysteriaSettings": {
          "version": 2,
          "auth": "<与 VPS 入站完全相同的强随机密钥>"
        },
        "finalmask": {
          "quicParams": {
            "congestion": "force-brutal",
            "brutalUp": "16mbps",
            "brutalDown": "24mbps"
          }
        }
      }
    }
  ]
}
```

- 如需强制使用 IPv6 的 AAAA 地址，可在 Windows 客户端出站的
  `streamSettings` 中增加 ` "sockopt": {"domainStrategy":"UseIPv6"} `；
  该项的准确拼写适用于 Xray 26.3.27。
- TLS 必须验证证书。受系统信任的 CA 证书可以使用 `serverName`。
  自签名证书应额外配置 `tlsSettings.pinnedPeerCertSha256`（证书 DER 的
  SHA-256，64 位十六进制），必要时配置 `verifyPeerCertByName`。
  **26.3.27 已移除 `allowInsecure`**，不要在新配置中使用。
- Windows SOCKS5 只绑定 `127.0.0.1`，不要向公网开放；
  如果旧 FRP/NAT Node 当前占用了 1080，则暂停旧进程或改用其他本地端口。
- Windows SOCKS5 入口已经支持 UDP ASSOCIATE。内核执行 UDP 会话转发；
  不需要应用层额外封装 UDP 报文。

```powershell
.\xray.exe version
.\xray.exe run -test -c C:\xray\hysteria-client.json
.\xray.exe run -c C:\xray\hysteria-client.json
```

HTTP 客户端可设置 SOCKS5 代理为 `127.0.0.1:1080`，UDP 应用应使用
支持 SOCKS5 UDP ASSOCIATE 的客户端。

## 测试和限制

Windows 后端已用 **两套真实 26.3.27 Xray 进程**、
`127.0.0.1` 的 QUIC/UDP 路径测试：SOCKS5 CONNECT → HTTP 200、
SOCKS5 UDP ASSOCIATE → UDP echo、TLS 证书固定校验和两端 Brutal 激活日志均通过。
CI 额外在 Linux 使用固定提交的 Xray 26.3.27 完成同等 loopback smoke，
并执行 x-ui Hysteria2 数据模型与 Xray 配置生成测试。

这些属于本机进程级端到端验证；**还不等于实际境内 Windows↔境外 VPS 的联通性/性能保证**。
新的 Hysteria2 入站与旧 FRP/XTCP/WARP 运行路径相互独立，切换或回退时无需删除旧配置。
