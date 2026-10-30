# WxCat

[![ci](https://github.com/Kyresn/WxCat/actions/workflows/ci.yml/badge.svg)](https://github.com/Kyresn/WxCat/actions/workflows/ci.yml)
[![docker](https://github.com/Kyresn/WxCat/actions/workflows/docker.yml/badge.svg)](https://github.com/Kyresn/WxCat/actions/workflows/docker.yml)

微信版 NapCat：在 Linux 上用 Docker 跑起个人微信，对外提供标准 **OneBot 11** 接口，让 AstrBot、DSH（DeepSeek Harness）等框架像接 NapCat 一样接微信。

> ## ⚠️ 使用前必读
>
> - 本项目通过非官方方式自动化个人微信（Wine + WeChatFerry 注入），**违反微信用户协议**。
> - 账号可能被限制登录、限制功能或**永久封禁**。请只使用小号，不要用于工作、支付或重要社交账号。
> - 项目不提供任何形式的账号安全保证。继续使用即表示你自行承担全部风险。
> - 群聊默认只在被 @ 时上报消息，避免机器人打扰群成员。
> - 镜像与发行包内含/会下载腾讯微信客户端；微信的再分发受腾讯许可限制，请仅在个人环境使用。

---

## 特性

- **OneBot 11 四种传输**：HTTP API、正向 WebSocket、反向 WebSocket、HTTP webhook（POST 上报）。
- **NapCat 风格配置**：`httpServers` / `httpClients` / `websocketServers` / `websocketClients`，迁移成本低。
- **AstrBot 直连**：反向 WebSocket 带 `X-Self-ID` / `X-Client-Role` / `Authorization`，对应 `aiocqhttp` 适配器。
- **DSH 直连**：`dsh-onebot` 的 forward-ws、reverse-ws、http 三种模式都能用。
- **稳定数字 ID**：微信的 `wxid_xxx` / `123@chatroom` 会映射成重启不变、小于 2^53 的整数，兼容 JS 客户端。
- **可插拔后端**：默认 WeChatFerry（Wine）；也支持 WeFlow（读本地数据库 + SSE，不注入，兼容已装微信 4.x）、MimicWX-Linux 与其他 REST 桥。
- **一体化镜像**：Xvfb + noVNC 虚拟桌面 + 微信 + 注入 + 网关，`docker compose up -d` 即可。
- **多架构网关**：`linux/amd64` + `linux/arm64`，arm64 上跑网关、微信后端跑在 x86 机器上。
- **Windows 原生**：Release 提供 `wxcat.exe` + 32 位注入器 + PowerShell 脚本，微信 3.9.12.51 放在私有目录，不影响系统已装版本。
- **管理面板**：登录状态、二维码截图、网络配置、测试发送、日志。

## 镜像与发行包

| 产物 | 平台 | 内容 |
| --- | --- | --- |
| `ghcr.io/kyresn/wxcat` | `linux/amd64`, `linux/arm64` | 网关：OneBot 11 + WebUI，不含 Wine/微信 |
| `ghcr.io/kyresn/wxcat-wcf` | `linux/amd64` | 一体化：Xvfb + noVNC + 微信 3.9.12.51 + WCF + 网关 |
| `wxcat-windows-amd64.zip` | Windows 10/11 x64 | `wxcat.exe`、32 位 `wxinject.exe`、PowerShell 脚本 |

Windows 用法见 [docs/windows.md](docs/windows.md)。

arm64 无法运行一体化镜像（微信是 32 位 Windows 程序，arm64 装不了 i386 Wine 运行库）。推荐拓扑：x86_64 跑完整 WxCat，arm64 上的 AstrBot/DSH 直接连过去。只有必须把网关放在 arm64 时，才用网关镜像 + 远程后端（实验性）。详见 [docs/arm64.md](docs/arm64.md)。

## 架构

一体化部署（x86_64）：

```
┌─ Docker 容器（linux/amd64）─────────────────────────────────────────────┐
│  Xvfb :1 ──► x11vnc ──► noVNC :6080       扫码登录                      │
│      └──► wine WeChat.exe 3.9.12.51                                    │
│               ▲ 注入 sdk.dll / spy.dll（CreateRemoteThread + LoadLibrary）│
│               │                                                        │
│           WeChatFerry RPC 10086/10087                                  │
│               ▲                                                        │
│         wxcat（Go 单二进制）                                            │
│           ├ backend/wcf | backend/rest                                │
│           ├ 事件流水线 + ID 映射 + 媒体缓存                             │
│           └ OneBot 11：HTTP :3000 / WS :3001 / 反向 WS / webhook        │
│         WebUI :6099                                                    │
└────────────────────────────────────────────────────────────────────────┘
        │                    │                      │
        ▼                    ▼                      ▼
   AstrBot :6199/ws      DSH forward-ws        HTTP / webhook
   （反向 WS）           或 reverse-ws / http
```

分离部署（arm64 网关 + x86 微信后端）：

```
arm64: wxcat 网关  ──11086/11087 (WCF RPC) + 11090 (媒体代理)──►  x86: wxcat-wcf
```

网关镜像里没有任何微信代码，`backend.wcf.cmdAddr` / `msgAddr` 可以指向任意可达的 WCF 服务。

## 快速开始

要求：Linux x86_64 主机（或 Docker Desktop），Docker 24+，内存 ≥ 6 GB，磁盘 ≥ 8 GB。

### 方式一：拉取 ghcr 镜像

```bash
git clone https://github.com/Kyresn/WxCat.git wxcat && cd wxcat
cp .env.example .env
# 编辑 .env：WXCAT_FULL_IMAGE=ghcr.io/kyresn/wxcat-wcf:latest
docker compose pull
docker compose up -d
docker compose logs -f wxcat
```

### 方式二：本地构建

```bash
git clone https://github.com/Kyresn/WxCat.git wxcat && cd wxcat
docker compose up -d --build
docker compose logs -f wxcat
```

镜像已内置微信 3.9.12.51（32 位）与可用的 Wine 前缀。首次启动只把内置前缀复制到 `./wine` 卷，通常几十秒，不会下载或安装微信；随后启动 Xvfb、noVNC、微信并注入 hook。

> arm64 主机不要用上面的命令，见 [docs/arm64.md](docs/arm64.md)。

### 方式三：只跑网关（微信后端在另一台机器）

```bash
cp .env.example .env
# 编辑 WXCAT_IMAGE、WXCAT_WCF_CMD_ADDR、WXCAT_WCF_MSG_ADDR、WXCAT_WCF_MEDIA_URL
docker compose -f docker-compose.gateway.yml up -d
```

### 方式四：Windows 原生（不用 Docker）

从 [Releases](https://github.com/Kyresn/WxCat/releases) 下载 `wxcat-windows-amd64.zip`，解压后以管理员 PowerShell 运行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -Firewall
powershell -ExecutionPolicy Bypass -File scripts\windows\start.ps1
```

`setup.ps1` 会把微信 3.9.12.51（32 位）放进私有目录 `runtime\wechat`，不安装到 `Program Files`，不影响已安装的微信 4.x。完整说明见 [docs/windows.md](docs/windows.md)。

### 方式五：WeFlow（不注入，兼容微信 4.x）

已装微信 4.x 或不想注入时，用 WeFlow 读本地数据库 + SSE 收消息：

```json
"backend": {
  "type": "weflow",
  "weflow": { "baseUrl": "http://127.0.0.1:5031", "token": "WeFlow Token", "sendMode": "uia" }
}
```

发送由 `scripts/windows/uia-send.ps1` 通过 Windows UI Automation 完成。详见 [docs/weflow.md](docs/weflow.md)。

登录：

1. 打开 `http://<host>:6080/vnc.html`（VNC 密码默认 `wxcat`，可用 `VNC_PASSWORD` 修改）。
2. 用手机微信扫码。扫码后不要关闭页面，等待几秒。
3. 打开 `http://<host>:6099` 查看状态；`在线` 为绿色表示 hook 与网关都正常。
4. 也可以在管理面板里直接看二维码截图（`/api/qr`）。

验证 HTTP API：

```bash
TOKEN=你的token
curl -s http://127.0.0.1:3000/get_login_info -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:3000/get_group_list -H "Authorization: Bearer $TOKEN"
```

## 连接 AstrBot

AstrBot 的 `aiocqhttp` 适配器只支持**反向 WebSocket**（它监听，WxCat 拨过去）。

1. AstrBot 后台 → 消息平台 → 新增 `aiocqhttp`：
   - `ws_reverse_host`: `0.0.0.0`
   - `ws_reverse_port`: `6199`
   - `ws_reverse_token`: 自定义一个字符串
2. 修改 `config/wxcat.json`：

```json
"websocketClients": [
  {
    "name": "astrbot",
    "enable": true,
    "url": "ws://<astrbot-host>:6199/ws",
    "messagePostFormat": "array",
    "reportSelfMessage": false,
    "token": "同一个 token",
    "reconnectInterval": 3000,
    "heartInterval": 30000,
    "debug": false
  }
]
```

3. `docker compose restart wxcat`。
4. AstrBot 日志出现 `aiocqhttp(OneBot v11) 适配器已连接。` 即成功。

详细说明与排错见 [docs/astrbot.md](docs/astrbot.md)。

## 连接 DSH

`dsh-onebot` 支持三种传输，任选一种：

```yaml
# 方式一：forward-ws（WxCat 做服务端）
transport: forward-ws
url: ws://<wxcat-host>:3001
accessTokenRef: ONEBOT_ACCESS_TOKEN

# 方式二：reverse-ws（DSH 监听，WxCat 拨入）
transport: reverse-ws
listenHost: 0.0.0.0
listenPort: 3002
listenPath: /onebot
# WxCat: websocketClients[0].url = ws://<dsh-host>:3002/onebot

# 方式三：http（DSH 调 WxCat 的 HTTP API，WxCat 把事件 POST 给 DSH）
transport: http
url: http://<wxcat-host>:3000
```

详见 [docs/dsh.md](docs/dsh.md)。

## OneBot 能力

| 类别 | 已实现 |
| --- | --- |
| 消息 | `send_msg`、`send_private_msg`、`send_group_msg`、`get_msg` |
| 信息 | `get_login_info`、`get_friend_list`、`get_group_list`、`get_group_info`、`get_group_member_list`、`get_group_member_info`、`get_stranger_info` |
| 状态 | `get_status`、`get_version_info`、`can_send_image`、`can_send_record`、`get_cookies`、`get_csrf_token` |
| 媒体 | `get_image`、`download_file`、图片消息自动缓存到 `/media/<id>` |
| 事件 | `message`（private / group）、`notice`（group_recall、friend_recall）、`meta_event`（lifecycle、heartbeat） |
| 降级 | `send_*_forward_msg` 逐条转成文本；`delete_msg` 等微信侧无能力的动作返回 `retcode 1404` |

消息段支持：`text`、`image`、`at`、`face`、`reply`（发送时忽略）、`record`、`video`、`file`。

## 配置

完整示例见 [config.example.json](config.example.json)。

关键字段：

| 字段 | 说明 |
| --- | --- |
| `backend.type` | `wcf`（默认）、`weflow`、`mimicwx`、`webhook` |
| `idMode` | `numeric`（默认，映射成稳定整数）或 `raw`（直接下发 wxid 字符串） |
| `groupMode` | `at`（默认，群聊只上报被 @ 的消息）或 `all` |
| `atKeywords` | 额外的 @ 触发词，例如 `["小助手"]` |
| `reportSelfMessage` | 是否上报机器人自己发出的消息 |
| `enableLocalFile2Url` | 图片/文件是否通过 `/media/` URL 下发；关闭后不下发媒体 |
| `mediaBaseURL` | 对外可访问的媒体前缀，例如 `http://192.168.1.10:3000`；为空时图片以 data URL 下发 |
| `network.*.token` | OneBot 鉴权 token，`Authorization: Bearer <token>` 或 `?access_token=` |
| `network.*.messagePostFormat` | `array` 或 `string`（CQ 码） |
| `webui.token` | 管理面板 token |

环境变量：`WXCAT_CONFIG`、`WXCAT_DATA`、`WXCAT_BACKEND`、`WXCAT_TOKEN`（给所有未单独设置 token 的 OneBot 端点）、`WXCAT_WEBUI_TOKEN`、`WXCAT_ID_MODE`、`WXCAT_MEDIA_BASE_URL`、`WXCAT_WCF_CMD_ADDR`、`WXCAT_WCF_MSG_ADDR`、`WXCAT_WCF_MEDIA_URL`、`WXCAT_WCF_MEDIA_TOKEN`、`WXCAT_MIMICWX_BASE_URL`、`WXCAT_MIMICWX_TOKEN`、`WXCAT_MIMICWX_SELF_WXID`、`WXCAT_WEFLOW_BASE_URL`、`WXCAT_WEFLOW_TOKEN`、`WXCAT_WEFLOW_SELF_WXID`、`WXCAT_WEFLOW_SEND_MODE`、`VNC_PASSWORD`。

## 后端选择

| 后端 | 说明 | 适用 |
| --- | --- | --- |
| `wcf`（默认） | Wine + 微信 3.9.12.51 + WeChatFerry。按 wxid 收发，图片/文件/群列表完整。 | 需要完整 OneBot 能力，x86 服务器 |
| `mimicwx` | 对接 MimicWX-Linux（微信 Linux 原生客户端 + SQLCipher + UI 自动化）。无注入、封号风险低，但发送按显示名寻址、速度慢。 | 更看重账号安全，能接受功能缩水 |
| `weflow` | 对接 [WeFlow](https://github.com/hicccc77/WeFlow)：读本地数据库 + SSE 收消息，不注入微信；发送走 Windows UIA 或外部 HTTP 发送服务。 | 已装微信 4.x、不想注入、能接受 UIA 发送 |
| `webhook` | 通用 REST 桥适配器，路径可配置。 | 已有自己的微信 hook |

WCF 后端已归档（上游 `lich0821/WeChatFerry` 停止维护），因此微信版本锁定 3.9.12.51。详见 [docs/backend-wcf.md](docs/backend-wcf.md)。

## 数据与持久化

| 卷 | 内容 |
| --- | --- |
| `./config` | `wxcat.json` 配置 |
| `./data` | `idmap.json`（ID 映射）、`media/`（解密后的图片缓存） |
| `./wine` | Wine 前缀（首次启动从镜像内置前缀复制，含微信 3.9.12.51） |
| `./wechat-files` | 微信聊天文件与数据库 |

删除 `./data/idmap.json` 会导致 OneBot 数字 ID 变化，AstrBot 的会话 ID 会失效，请谨慎。

## 常见问题

见 [docs/faq.md](docs/faq.md)。最典型的三个：

- **一直显示离线**：微信还没登录，或注入失败。看 `docker compose logs wxcat | grep inject`。
- **AstrBot 连不上**：确认 `url` 以 `/ws` 结尾、token 两端一致、容器能访问 AstrBot 的 6199 端口。
- **图片收不到**：设置 `mediaBaseURL` 为框架能访问的地址，或接受 data URL。

## 镜像构建与 CI

GitHub Actions：

- [`.github/workflows/ci.yml`](.github/workflows/ci.yml)：`gofmt`、`go vet`、`go test`（Linux 跑 race）、交叉编译 arm64 网关与 32 位 Windows 注入器。
- [`.github/workflows/docker.yml`](.github/workflows/docker.yml)：推送 `main` 或 `v*` 标签时构建并推送 ghcr 镜像。
- [`.github/workflows/release.yml`](.github/workflows/release.yml)：推送 `v*` 标签时构建 Windows/Linux 发行包并创建 GitHub Release。
  - `wxcat`：`linux/amd64,linux/arm64`
  - `wxcat-wcf`：`linux/amd64`

本地构建：

```bash
# 网关（当前架构）
docker build -f docker/Dockerfile -t wxcat:local .

# 网关（多架构，需要 buildx + qemu）
docker buildx build -f docker/Dockerfile --platform linux/amd64,linux/arm64 -t wxcat:local .

# 一体化（amd64）
docker build -f docker/Dockerfile.wcf -t wxcat-wcf:local .
```

ghcr 包首次推送后默认是私有的。到 GitHub → Packages → 对应镜像 → Package settings 改成 Public。

## 开发

```bash
go test ./...
go build ./cmd/wxcat
go build ./cmd/wcf-agent
GOOS=windows GOARCH=386 go build -o wxinject.exe ./cmd/wxinject
```

也可以直接用 Makefile：

```bash
make test
make build VERSION=0.1.0
make build-windows VERSION=0.1.0   # wxcat.exe + 32 位 wxinject.exe
make image VERSION=0.1.0           # 多架构网关
make image-full VERSION=0.1.0      # amd64 一体化
```

不使用 Docker 时，`wxcat` 可以直接跑：把 `backend.type` 指向一个已经运行的 WCF / MimicWX 服务即可。OneBot 层与微信后端完全解耦。

Windows 本机快速验证（微信 3.9.12.51 + WCF 已注入，RPC 监听 10086/10087）：

```powershell
go run ./cmd/wxcat -c config\wxcat.json
```

也可以在 Windows 上手动注入（等价于容器里的 `inject.sh`，**必须是 32 位注入器**）：

```powershell
$env:GOOS="windows"; $env:GOARCH="386"
go build -o wxinject.exe ./cmd/wxinject
.\wxinject.exe "C:\Program Files (x86)\Tencent\WeChat\sdk.dll"
```

目录：

```
cmd/wxcat           网关入口
cmd/wcf-agent       媒体代理（远程网关时在微信主机上解密/暂存文件）
cmd/wxinject        Windows 32 位 DLL 注入器（容器内用 wine 执行）
internal/onebot     OneBot 11 协议层与四种传输
internal/backend    Backend 接口、wcf / weflow / rest 适配器
internal/pipeline   去重、自发过滤、群 @ 过滤
internal/idmap      wxid ↔ 数字 ID 持久映射
internal/media      解密媒体缓存与 /media 服务
internal/webui      管理面板（内嵌静态页）
third_party/wcf     WeChatFerry protobuf 与精简客户端（MIT）
docker/             Dockerfile（多架构网关）、Dockerfile.wcf（amd64 一体化）、supervisord、注入脚本
scripts/windows/    setup.ps1、start.ps1、install-task.ps1、uia-send.ps1
.github/workflows   CI、ghcr 镜像构建、Release 发行包
```

## 限制

- 一体化镜像仅 `linux/amd64`（Wine 需要 i386 运行库）；网关镜像支持 `linux/amd64` 与 `linux/arm64`。
- Windows 原生部署只支持 Windows 10/11 x64 + 微信 3.9.12.51（32 位）。
- arm64 推荐让 x86 主机跑完整服务、arm64 应用直连；arm64 网关 + 远程 WCF 属于实验性拓扑，见 [docs/arm64.md](docs/arm64.md)。
- 微信版本锁定 3.9.12.51；升级微信会使 hook 失效。
- 运行时内存约 4–5 GB（Wine + 微信 + 虚拟桌面）。
- 无撤回 API：`delete_msg` 返回 1404，但撤回事件仍会上报。
- 群成员列表依赖数据库表结构，可能为空；不同微信版本字段不同。
- 不支持合并转发、群管理、表情回应、拍一拍。
- 一个容器一个微信号。

## 许可与致谢

本项目 MIT。`third_party/wcf` 来自 [lich0821/WeChatFerry](https://github.com/lich0821/WeChatFerry)（MIT）。

思路参考：

- [NapCat](https://github.com/NapNeko/NapCatQQ)：OneBot 11 配置结构与部署体验。
- [AstrBot](https://github.com/AstrBotDevs/AstrBot)：`aiocqhttp` 适配器行为。
- [MimicWX-Linux](https://github.com/wx-11/MimicWX-Linux)：Linux 原生微信自动化路线。
- [sourcepro5/WeChatAgent](https://github.com/sourcepro5/WeChatAgent)、[alingalingling/Astrbot-wechat-bot-](https://github.com/alingalingling/Astrbot-wechat-bot-)、[JackZhaoooooo/astrbot_agent_wechat_bridge](https://github.com/JackZhaoooooo/astrbot_agent_wechat_bridge)：产品形态参考。
