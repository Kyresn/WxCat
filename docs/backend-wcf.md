# 后端：WCF（Wine + WeChatFerry）

## 组成

| 组件 | 版本 | 作用 |
| --- | --- | --- |
| 微信 | 3.9.12.51 | 客户端 |
| WeChatFerry | v39.5.2 | 注入 hook，提供 RPC |
| Wine | WineHQ stable | 在 Linux 上运行微信 |
| Xvfb + x11vnc + noVNC | Ubuntu 22.04 包 | 虚拟桌面与扫码 |
| wxcat | 本仓库 | OneBot 11 网关 |

WeChatFerry 上游仓库已归档，因此版本全部锁定。升级微信会导致 hook 失效。

## 数据流

```
wxcat ──pair1/NNG──► sdk.dll (10086 命令 / 10087 消息) ──► WeChat.exe
```

- 命令口：`tcp://127.0.0.1:10086`
- 消息口：`tcp://127.0.0.1:10087`
- 默认只在容器内监听。需要远程网关时，使用下面的 11086/11087 桥。

### 远程后端（arm64 或分离部署）

一体化镜像内置两个 `socat` 桥：

- `0.0.0.0:11086` → `127.0.0.1:10086`
- `0.0.0.0:11087` → `127.0.0.1:10087`

以及一个媒体代理 `wcf-agent`（`0.0.0.0:11090`）：远端网关无法在本地解密微信图片，也无法把本地文件塞进 Wine 的 `Z:` 盘，所以媒体操作必须回到微信主机执行。

在 `docker-compose.yml` 里发布这三个端口后，另一台机器上的网关可以这样配置：

```dotenv
WXCAT_WCF_CMD_ADDR=tcp://x86-host:11086
WXCAT_WCF_MSG_ADDR=tcp://x86-host:11087
WXCAT_WCF_MEDIA_URL=http://x86-host:11090
WXCAT_WCF_MEDIA_TOKEN=与微信主机一致
```

**11086/11087 没有认证。** 只在内网或 VPN 内发布，并用防火墙限制来源。11090 建议设置 token。完整步骤见 [arm64.md](arm64.md)。

## 注入流程

1. supervisor 启动 `Xvfb :1`、微信。
2. `inject.sh` 轮询 `WeChat.exe` 进程，窗口就绪后调用 `wxinject.exe`。
3. `wxinject.exe` 是 Go 写的 Windows 程序，用 `CreateRemoteThread` + `LoadLibraryA` 把 `sdk.dll` 加载进微信进程。
4. `sdk.dll` 加载同目录的 `spy.dll`，启动 RPC 服务。
5. `wxcat` 连接 10086，轮询 `IsLogin()`，登录后开启消息接收。

`sdk.dll` 与 `spy.dll` 会被复制到微信安装目录（`entrypoint.sh` 和 `inject.sh` 都会做）。

## 首次部署验证清单

Docker 守护进程需要先启动。按顺序检查：

```bash
docker compose up -d --build
docker compose logs -f wxcat | grep -E 'entrypoint|wechat|inject|wxcat'
```

1. **安装**：日志出现 `WeChat found at ...`，`./wine` 下存在 `drive_c/Program Files*/Tencent/WeChat/WeChat.exe`。
2. **虚拟桌面**：`http://<host>:6080/vnc.html` 能看到微信登录窗口。
3. **注入**：日志出现 `[inject] injection succeeded`。
4. **登录**：扫码后 `wxcat` 日志出现 `wechat logged in wxid=... nickname=...`。
5. **收消息**：给机器人发一条私聊，日志出现 `onebot identity ready` 之前不应报错。
6. **发消息**：管理面板测试发送成功。

如果第 3 步失败：

- 看 `wine /opt/wcf/wxinject.exe` 的输出。`LoadLibraryA returned NULL` 通常是 DLL 路径错误或 `spy.dll` 不在同目录。
- 微信版本必须与 WCF 版本匹配（3.9.12.51 / v39.5.2）。
- Wine 缺少运行库时，可在容器内执行 `xvfb-run -a winetricks -q vcrun2019 riched20` 后重启。

## 已知限制

- 无撤回 API：`delete_msg` 返回 1404。撤回事件通过 `10002` 系统消息解析后上报。
- 群成员列表依赖 `MicroMsg.db` 的表结构，WxCat 会尝试多种查询，失败时返回空列表。
- 群 @ 检测依赖消息文本里出现 `@机器人昵称`，或 `atKeywords` 命中。WCF 不提供入站 @ 列表。
- 图片解密依赖 `DecryptImage` 与 Wine 的 `Z:` 盘路径映射；路径转换在 `internal/backend/wcf` 中处理。
- 媒体文件会缓存到 `data/media`，不会自动清理。
- 内存占用约 4–5 GB。

## 切换后端

如果 WCF 路线在你的环境里不可用，可以改用 Linux 原生路线：

```json
"backend": { "type": "mimicwx", "mimicwx": { "baseUrl": "http://<mimicwx-host>:8899", "token": "..." } }
```

MimicWX-Linux 需要自己部署（它提供 Docker 镜像）。WxCat 只作为 OneBot 网关连接它，不复制其代码。

MimicWX 的 `/status` 不返回登录账号，因此需要手动填写 `selfWxid`：

```json
"mimicwx": { "baseUrl": "http://127.0.0.1:8899", "token": "", "selfWxid": "wxid_xxx" }
```

差异：

- 发送按**联系人显示名**寻址，同名联系人会失败。
- 没有群成员列表。
- 不支持发文件。
- 收消息走 `/messages/new` 轮询，延迟约 2 秒。
