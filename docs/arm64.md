# arm64 部署

## 结论先说

- **网关镜像 `wxcat` 原生支持 linux/amd64 与 linux/arm64**。
- **一体化镜像 `wxcat-wcf`（Wine + 微信 + 注入）只支持 linux/amd64**。
- 微信本身无法在 arm64 上运行，所以 arm64 只负责网关层。两种拓扑：

| 拓扑 | 说明 | 状态 |
| --- | --- | --- |
| **A. x86 跑完整 WxCat，arm64 应用直连** | AstrBot/DSH 直接连 x86 的 3000/3001/6199，不需要 arm64 容器 | 推荐，最稳 |
| B. arm64 跑网关，x86 只跑微信后端 | 网关通过 11086/11087/11090 连远程 WCF | 实验性，见下方限制 |

原因：

1. 微信 3.9.12.51 是 32 位 Windows 程序，依赖 Wine 的 i386 运行库。
2. `dpkg --add-architecture i386` 在 arm64 的 Ubuntu 上不受支持，无法安装 32 位 x86 库。
3. box86/box64 路线需要 i386 Wine 前缀，且 DLL 注入（`CreateRemoteThread` + `LoadLibraryA`）跨架构模拟不稳定，不能作为可承诺的方案。
4. 用 QEMU 跑 amd64 一体化镜像理论可行，但 Wine + GUI 微信的性能不可接受，仅作为最后手段。
5. WeChatFerry 的 RPC 基于 NNG pair1，是否支持多个并发客户端未经实测。拓扑 B 会让网关和本地 `wxcat` 同时连接，可能冲突；生产环境优先选 A。

## 拓扑 A（推荐）

x86_64 主机：

```bash
git clone https://github.com/Kyresn/WxCat.git
cd WxCat
cp .env.example .env
# WXCAT_FULL_IMAGE=ghcr.io/kyresn/wxcat-wcf:latest
docker compose pull
docker compose up -d
```

扫码登录 `http://<x86-host>:6080/vnc.html`，然后让 arm64 上的 AstrBot/DSH 连接：

- AstrBot 反向 WS：WxCat 配 `ws://<astrbot-host>:6199/ws`
- DSH forward-ws：`ws://<x86-host>:3001`
- HTTP API：`http://<x86-host>:3000`

这种拓扑不需要 arm64 镜像，arm64 只跑你自己的应用。

## 拓扑 B（实验性）：arm64 网关 + 远程 WCF

只有当你确实需要把网关放在 arm64 主机上时才用。先确认 WCF 能接受并发客户端（拓扑 B 下远程网关与本地 `wxcat` 会同时连接；若冲突，把 x86 主机的本地 `wxcat` 停掉，只保留 `wcf-agent` 与 socat 桥）。

## 拓扑 B 架构

```
┌─ arm64 主机 ────────────────────┐        ┌─ x86_64 主机 ───────────────────┐
│ wxcat 网关（多架构镜像）         │  TCP   │ wxcat-wcf 一体化镜像             │
│  ├ OneBot HTTP :3000            │◄──────►│  ├ 微信 + Wine                  │
│  ├ OneBot WS   :3001            │ 11086  │  ├ WeChatFerry RPC 10086/10087  │
│  ├ WebUI       :6099            │ 11087  │  ├ socat 桥 11086/11087          │
│  └ 媒体缓存                      │ 11090  │  └ wcf-agent 11090（解密/上传）  │
└─────────────────────────────────┘        └──────────────────────────────────┘
```

一体化镜像里的 `socat` 把容器内只监听 `127.0.0.1` 的 WCF 端口转发到 `0.0.0.0:11086/11087`。只有你把这两个端口发布出去，外部才能访问。

## 拓扑 B 步骤

### 1. x86_64 主机：启动微信后端

```bash
git clone https://github.com/Kyresn/WxCat.git
cd WxCat
cp .env.example .env
# 编辑 .env：WXCAT_FULL_IMAGE=ghcr.io/kyresn/wxcat-wcf:latest
#            WXCAT_WCF_MEDIA_TOKEN=随机字符串
docker compose -f docker-compose.backend.yml pull
docker compose -f docker-compose.backend.yml up -d
```

`docker-compose.backend.yml` 在完整部署的基础上额外发布 11086/11087/11090，不需要手动改端口。

如果远程网关连接 WCF 失败（pair1 可能只接受一个客户端），停掉 x86 主机上的本地网关，只保留 agent 与 socat：

```bash
docker compose -f docker-compose.backend.yml exec wxcat supervisorctl stop wxcat
```

扫码登录：`http://<x86-host>:6080/vnc.html`。

验证端口：

```bash
nc -zv <x86-host> 11086
nc -zv <x86-host> 11087
curl -s http://<x86-host>:11090/healthz
```

### 2. arm64 主机：启动网关

```bash
git clone https://github.com/Kyresn/WxCat.git
cd WxCat
cp .env.example .env
```

编辑 `.env`：

```dotenv
WXCAT_IMAGE=ghcr.io/kyresn/wxcat:latest
WXCAT_WCF_CMD_ADDR=tcp://<x86-host>:11086
WXCAT_WCF_MSG_ADDR=tcp://<x86-host>:11087
WXCAT_WCF_MEDIA_URL=http://<x86-host>:11090
WXCAT_WCF_MEDIA_TOKEN=与 x86 主机的 WXCAT_WCF_MEDIA_TOKEN 一致
WXCAT_TOKEN=换成随机字符串
WXCAT_WEBUI_TOKEN=换成随机字符串
WXCAT_MEDIA_BASE_URL=http://<arm64-host>:3000
```

启动：

```bash
docker compose -f docker-compose.arm64.yml up -d
docker compose -f docker-compose.arm64.yml logs -f
```

预期日志：

```
http api listening addr=0.0.0.0:3000
forward websocket listening addr=0.0.0.0:3001
webui listening addr=0.0.0.0:6099
onebot identity ready wxid=wxid_xxx self_id=...
```

打开 `http://<arm64-host>:6099` 确认在线。

> `WXCAT_WCF_MEDIA_URL` 不设置时，网关只能收发文本；图片、语音、文件无法解密或上传。设置后才会启用媒体代理。

### 3. 验证媒体

在私聊里发一张图片，网关日志应出现 `cache media failed` 之外的正常记录；AstrBot/DSH 应收到 `image` 段。发送图片时，x86 主机的 `wcf-agent` 日志会出现 `staged upload`。

## 安全警告

WeChatFerry 的 RPC **没有任何认证**，媒体代理默认也没有。任何能连到 11086/11087/11090 的人都可以用你的微信收发消息、读写媒体。

- 只在内网或 VPN 内发布这些端口。
- 不要直接暴露到公网。
- 防火墙限制来源 IP 为 arm64 网关的地址。
- 给 11090 设置 `WXCAT_WCF_MEDIA_TOKEN`（两端一致），至少让媒体代理需要 token。
- 如果必须跨公网，用 WireGuard/Tailscale 之类的隧道，不要端口转发。

## 备选：网关也跑在 x86 上

不需要 arm64 时，直接用一体化镜像最简单。网关与微信在同一个容器里，无需 11086/11087。

## 备选：MimicWX 后端

如果你的 arm64 设备能运行微信 Linux 原生客户端（需自行确认是否有 arm64 版本），可以用 MimicWX 作为后端：

```dotenv
WXCAT_BACKEND=mimicwx
WXCAT_MIMICWX_BASE_URL=http://<mimicwx-host>:8899
WXCAT_MIMICWX_TOKEN=...
WXCAT_MIMICWX_SELF_WXID=wxid_xxx
```

MimicWX 的发送按联系人显示名寻址，且没有群成员列表，功能比 WCF 少。

## 备选：QEMU 模拟

如果实在没有 x86 机器：

```bash
docker run --privileged --rm tonistiigi/binfmt --install amd64
```

然后强行使用一体化镜像：

```bash
docker compose up -d   # compose 里已写 platform: linux/amd64
```

这会在 arm64 上通过 QEMU 模拟 x86_64 运行 Wine + 微信。能启动，但极慢（首次安装可能超过 30 分钟），不推荐，也不提供支持。

## 常见问题

**网关日志 `wcf connect failed`**

- 11086/11087 没有发布，或防火墙拦截。
- x86 主机上的微信尚未登录 / 注入失败。
- 地址写错：必须是 `tcp://host:11086`，不是 `http://`。

**网关连上了但收不到消息**

- 微信侧注入失败，检查 x86 主机的 `docker compose logs wxcat | grep inject`。
- 群聊默认 `groupMode: "at"`，不 @ 不上报。

**x86 主机重启后 arm64 网关连不上**

网关的反向连接会自动重试（指数退避，最长 30 秒）。等一会儿，或 `docker compose -f docker-compose.arm64.yml restart`。

**文本正常，图片/文件失败**

- `WXCAT_WCF_MEDIA_URL` 没设置，或 11090 没发布。
- `WXCAT_WCF_MEDIA_TOKEN` 两端不一致（401）。
- x86 主机上 `wxcat-agent` 日志报 `connect to wechatferry`，说明 WCF 还没就绪。
