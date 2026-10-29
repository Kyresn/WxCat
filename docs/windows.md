# Windows 原生部署

在 Windows 10/11 x64 上直接运行，不用 Docker：本机微信 3.9.12.51 + WeChatFerry 注入 + `wxcat.exe` 网关。

## 前置条件

| 项目 | 要求 |
| --- | --- |
| 系统 | Windows 10/11 x64 |
| 微信 | **3.9.12.51**（32 位）。4.x 不支持 |
| 权限 | 建议管理员 PowerShell（微信若以管理员运行，注入器必须同级） |
| 端口 | 3000 HTTP、3001 正向 WS、6099 管理面板、6199 反向 WS（出站） |

> 微信 3.9 会自动升级到 4.x，导致 hook 失效。`setup.ps1` 会关闭自动更新并把 `WeChatUpdate.exe` 改名为 `.bak`。

## 快速开始（Release 包）

1. 从 [Releases](https://github.com/Kyresn/WxCat/releases) 下载 `wxcat-windows-amd64.zip`，解压到不含中文和空格的目录，例如 `D:\WxCat`。
2. 以管理员身份打开 PowerShell：

```powershell
cd D:\WxCat
powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -InstallWeChat -Firewall
```

`setup.ps1` 会：

- 下载 `sdk.dll`、`spy.dll`、`WeChatSetup-3.9.12.51.exe` 到 `wcf\`
- 生成 `config\wxcat.json`
- 可选：静默安装微信、关闭自动更新、放行 3000/3001/6099

3. 编辑 `config\wxcat.json`，至少设置 token：

```json
"network": {
  "httpServers": [{ "name": "http", "enable": true, "host": "0.0.0.0", "port": 3000, "token": "换成随机字符串", "messagePostFormat": "array" }],
  "websocketServers": [{ "name": "ws", "enable": true, "host": "0.0.0.0", "port": 3001, "token": "同一个 token", "messagePostFormat": "array", "heartInterval": 30000 }]
}
```

4. 启动：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\start.ps1
```

脚本会：

1. 复制 `sdk.dll` / `spy.dll` 到微信安装目录。
2. 启动 `WeChat.exe`（已在运行则跳过）。
3. 运行 32 位 `wxinject.exe` 注入，轮询 `127.0.0.1:10086` 直到 RPC 端口真正打开。
4. 前台运行 `wxcat.exe`，日志同时写入 `data\wxcat.log`。

5. 在弹出的微信窗口扫码登录。

验证：

```powershell
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/get_login_info -Headers @{ Authorization = "Bearer 你的token" } -ContentType "application/json" -Body "{}"
```

## 开机自启

微信需要交互式桌面，不能用 Windows 服务。用计划任务：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\install-task.ps1
```

任务在用户登录时以最高权限运行 `start.ps1`。删除：

```powershell
Unregister-ScheduledTask -TaskName WxCat -Confirm:$false
```

也可以用 NSSM 包装 `start.ps1`，但同样需要已登录的桌面会话。

## 连接 AstrBot / DSH

与 Linux 部署一致：

- AstrBot `aiocqhttp`：AstrBot 监听 6199，WxCat 的 `websocketClients[0].url` 填 `ws://<astrbot-host>:6199/ws`，token 两端一致。
- DSH `dsh-onebot`：`forward-ws` 连 `ws://<windows-host>:3001`，或 `http` 调 `http://<windows-host>:3000`。

本机测试可用 `ws://127.0.0.1:3001`。

## 目录结构

```
D:\WxCat\
├── wxcat.exe                网关
├── wxinject.exe             32 位 DLL 注入器
├── config.example.json
├── config\wxcat.json        运行配置
├── data\                    idmap.json、media\、wxcat.log
├── wcf\                     sdk.dll、spy.dll、WeChatSetup-3.9.12.51.exe
└── scripts\windows\         setup.ps1、start.ps1、install-task.ps1
```

## 手动操作

不使用脚本时：

```powershell
# 1. 复制 DLL
Copy-Item .\wcf\sdk.dll "C:\Program Files (x86)\Tencent\WeChat\" -Force
Copy-Item .\wcf\spy.dll "C:\Program Files (x86)\Tencent\WeChat\" -Force

# 2. 启动微信后注入（必须用 32 位 wxinject.exe）
.\wxinject.exe "C:\Program Files (x86)\Tencent\WeChat\sdk.dll"

# 3. 确认 10086 已监听
Test-NetConnection 127.0.0.1 -Port 10086

# 4. 启动网关
.\wxcat.exe -c .\config\wxcat.json
```

## 排错

**注入后 10086 仍不通**

- 微信版本必须是 3.9.12.51。`(Get-Item "C:\Program Files (x86)\Tencent\WeChat\WeChat.exe").VersionInfo.FileVersion`。
- `wxinject.exe` 必须是 32 位。Release 包里已固定；自己编译用 `GOOS=windows GOARCH=386`。
- 权限级别要一致：管理员微信需要管理员 PowerShell。
- 确认 `sdk.dll`、`spy.dll` 与 `WeChat.exe` 同目录。
- 如果 `sdk.dll` 加载后立即失败，安装 **Microsoft Visual C++ 2015-2022 Redistributable (x86)** 再试。

**日志出现 `dial tcp 127.0.0.1:10086: connectex: No connection could be made`**

注入未生效或微信刚启动。等几秒；`start.ps1` 会自动重试 90 秒。

**微信自动升级到 4.x**

重新安装 3.9.12.51，并确认 `WeChatUpdate.exe` 已改名、注册表 `HKCU\Software\Tencent\WeChat\NeedUpdateType=0`。

**端口被占用**

```powershell
Get-NetTCPConnection -LocalPort 3000,3001,6099 -ErrorAction SilentlyContinue
```

改 `config\wxcat.json` 里的端口，或结束占用进程。

**管理面板二维码截图不可用**

Windows 上不支持 `import` 截图，直接在微信窗口扫码即可。

## 安全

- `config\wxcat.json` 的 `network.*.token` 必须设置，尤其 `host` 为 `0.0.0.0` 时。
- 管理面板 6099 设置 `webui.token`。
- 不要将 3000/3001/6099 直接暴露到公网；用防火墙限制来源或走内网。
- 账号封禁风险与 Linux 部署相同，建议只用小号。
