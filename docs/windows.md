# Windows 原生部署

在 Windows 10/11 x64 上直接运行，不用 Docker。WxCat 会把**微信 3.9.12.51（32 位）放进自己的私有目录** `runtime\wechat`，不安装到 `Program Files`，因此不会覆盖或冲突你已有的微信 4.x。

## 前置条件

| 项目 | 要求 |
| --- | --- |
| 系统 | Windows 10/11 x64 |
| 微信 | 不需要系统安装；WxCat 使用私有副本 3.9.12.51 |
| 权限 | 建议管理员 PowerShell（微信若以管理员运行，注入器必须同级） |
| 端口 | 3000 HTTP、3001 正向 WS、6099 管理面板、6199 反向 WS（出站） |
| 必需 | 7-Zip（用于把微信解包到私有目录，不安装到系统） |

> 私有副本不会自动升级。`setup.ps1` 会禁用其中的 `WeChatUpdate.exe`。系统里已装的微信 4.x 保持原样。

> 发行包不包含腾讯的微信安装程序（再分发受腾讯许可限制）。`setup.ps1` 会自动下载并放进私有目录；离线环境用 `-OfflineSetup` 指向本地安装包。

## 快速开始

1. 从 [Releases](https://github.com/Kyresn/WxCat/releases) 下载 `wxcat-windows-amd64.zip`，解压到不含中文和空格的目录，例如 `D:\WxCat`。
2. 以管理员身份打开 PowerShell：

```powershell
cd D:\WxCat
powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -Firewall
```

`setup.ps1` 会：

- 下载 `sdk.dll`、`spy.dll`、`WeChatSetup-3.9.12.51.exe` 到 `wcf\`
- 用 7-Zip 把微信解包到 `runtime\wechat`（便携，不写注册表、不建快捷方式）
- 如果没有 7-Zip，脚本会直接报错退出，**不会**安装微信到 `Program Files`
- 把 `sdk.dll`、`spy.dll` 复制到私有微信目录
- 生成 `config\wxcat.json`
- 可选：放行 3000/3001/6099

离线环境：把 `WeChatSetup-3.9.12.51.exe` 放到任意位置，然后：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\setup.ps1 -OfflineSetup D:\WeChatSetup-3.9.12.51.exe
```

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

1. 使用 `runtime\wechat\WeChat.exe`（私有副本）；没有私有副本时直接报错，不会使用系统安装的微信（除非显式传 `-WeChatExe`）。
2. 复制 `sdk.dll` / `spy.dll` 到该目录。
3. 启动微信（已在运行则跳过）。
4. 运行 32 位 `wxinject.exe` 注入，轮询 `127.0.0.1:10086` 直到 RPC 端口真正打开。
5. 前台运行 `wxcat.exe`，日志同时写入 `data\wxcat.log`。

5. 在私有微信窗口扫码登录。

验证：

```powershell
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/get_login_info -Headers @{ Authorization = "Bearer 你的token" } -ContentType "application/json" -Body "{}"
```

## 备选：WeFlow 后端（不注入，兼容微信 4.x）

如果不希望注入微信进程，或本机已装微信 4.x：

1. 安装并登录 [WeFlow](https://github.com/hicccc77/WeFlow) 或 [AstrWeChat](https://github.com/CMKH1337/Astrwechat)，在设置里启用 HTTP API 与主动推送，记下端口和 Access Token。两者 API 相同；用 AstrWeChat 时把 `backend.type` 写成 `astrwechat`。
2. 把 `config\wxcat.json` 的 `backend` 改为：

```json
"backend": {
  "type": "weflow",
  "weflow": {
    "baseUrl": "http://127.0.0.1:5031",
    "token": "WeFlow 的 Access Token",
    "selfWxid": "机器人账号 wxid",
    "sendMode": "uia",
    "uiaScript": "scripts/windows/uia-send.ps1"
  }
}
```

3. 直接运行 `wxcat.exe`，不需要 `setup.ps1` 的微信解包，也不需要 `wxinject.exe`。

或者用启动脚本自动探测/启动 AstrWeChat 或 WeFlow，再启动网关：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\start-astrwechat.ps1
```

脚本会先检查 `http://127.0.0.1:5031/health`，未响应时尝试从常见安装目录启动 AstrWeChat/WeFlow，等待 API 就绪后运行 `wxcat.exe`。

发送通过 `scripts/windows/uia-send.ps1` 操作微信窗口，会抢焦点。完整说明见 [weflow.md](weflow.md)。

## 与已安装微信共存

- 私有副本在 `runtime\wechat`，不写 `Program Files`，不改系统微信的文件。
- 便携解包模式不写安装信息；私有安装模式只写自己的目录和标准卸载项。
- 两个版本不要**同时**运行：微信有单实例检测，同时启动可能互相顶掉。需要系统微信时先退出 WxCat 的微信。
- 数据目录：私有副本默认仍使用 `文档\WeChat Files`。如果系统微信 4.x 也在用同名目录，可在登录后到微信设置里改存储路径。

## 开机自启

微信需要交互式桌面，不能用 Windows 服务。用计划任务：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\install-task.ps1
```

任务在用户登录时以最高权限运行 `start.ps1`。删除：

```powershell
Unregister-ScheduledTask -TaskName WxCat -Confirm:$false
```

## 连接 AstrBot / DSH

与 Linux 部署一致：

- AstrBot `aiocqhttp`：AstrBot 监听 6199，WxCat 的 `websocketClients[0].url` 填 `ws://<astrbot-host>:6199/ws`，token 两端一致。
- DSH `dsh-onebot`：`forward-ws` 连 `ws://<windows-host>:3001`，或 `http` 调 `http://<windows-host>:3000`。

## 目录结构

```
D:\WxCat\
├── wxcat.exe                网关
├── wxinject.exe             32 位 DLL 注入器
├── config.example.json
├── config\wxcat.json        运行配置
├── data\                    idmap.json、media\、wxcat.log
├── runtime\wechat\          私有微信 3.9.12.51（由 setup 生成）
├── wcf\                     sdk.dll、spy.dll、WeChatSetup-3.9.12.51.exe
└── scripts\windows\         setup.ps1、start.ps1、install-task.ps1
```

## 手动操作

```powershell
$wx = "D:\WxCat\runtime\wechat"
Copy-Item .\wcf\sdk.dll "$wx\" -Force
Copy-Item .\wcf\spy.dll "$wx\" -Force
Start-Process "$wx\WeChat.exe"
.\wxinject.exe "$wx\sdk.dll"
Test-NetConnection 127.0.0.1 -Port 10086
.\wxcat.exe -c .\config\wxcat.json
```

## 排错

**注入后 10086 仍不通**

- 日志出现 `CreateRemoteThread failed: Access is denied`：PowerShell 不是管理员。用管理员身份重开。
- `wxinject.exe` 必须是 32 位。Release 包里已固定；自己编译用 `GOOS=windows GOARCH=386`。
- 权限级别要一致：管理员微信需要管理员 PowerShell。
- 确认 `sdk.dll`、`spy.dll` 与私有 `WeChat.exe` 同目录。
- 如果 `sdk.dll` 加载后立即失败，安装 **Microsoft Visual C++ 2015-2022 Redistributable (x86)** 再试。

**setup 解包失败**

- 必须安装 7-Zip：脚本只使用解包模式，永不写 `Program Files`。
- 安装包损坏时脚本会报 `The WeChat installer is corrupt`；删除 `wcf\WeChatSetup-3.9.12.51.exe` 后重跑。
- 解包后校验 `WeChat.exe`、`WeChatWin.dll`、`WeChatResource.dll`，缺一不可。

**微信自动升级**

私有副本的 `WeChatUpdate.exe` 已被改名。若发现版本变成 4.x，删除 `runtime\wechat` 重新运行 `setup.ps1`。

**端口被占用**

```powershell
Get-NetTCPConnection -LocalPort 3000,3001,6099 -ErrorAction SilentlyContinue
```

**管理面板二维码截图不可用**

Windows 上不支持 `import` 截图，直接在微信窗口扫码即可。

## 安全

- `config\wxcat.json` 的 `network.*.token` 必须设置，尤其 `host` 为 `0.0.0.0` 时。
- 管理面板 6099 设置 `webui.token`。
- 不要将 3000/3001/6099 直接暴露到公网；用防火墙限制来源或走内网。
- 账号封禁风险与 Linux 部署相同，建议只用小号。
