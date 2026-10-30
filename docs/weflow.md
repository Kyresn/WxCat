# 后端：WeFlow

用 [WeFlow](https://github.com/hicccc77/WeFlow) 或 [AstrWeChat](https://github.com/CMKH1337/Astrwechat) 读取本机微信数据库、通过 SSE 接收新消息，由 WxCat 转成 OneBot 11。**不注入微信进程**，不需要微信 3.9.12.51，可与已安装的微信 4.x 共存。

AstrWeChat 的 HTTP/SSE API 与 WeFlow 完全一致（默认同为 `127.0.0.1:5031`）。把 `backend.type` 写成 `weflow` 或 `astrwechat`，`baseUrl` 指向 AstrWeChat 即可。

## 架构

```
微信桌面客户端
   │ 本地数据库 / 媒体文件
   ▼
WeFlow（HTTP API :5031 + SSE 推送）
   │ GET /api/v1/contacts, /messages, /sessions, /group-members
   │ GET /api/v1/push/messages (SSE)
   ▼
WxCat（backend.type = weflow）
   ├ 事件 → OneBot 11（HTTP / WS / 反向 WS / webhook）
   └ 发送 → sendMode
        ├ uia  : scripts/windows/uia-send.ps1（Windows UI Automation）
        ├ http : 外部发送服务（JSON）
        └ none : 只收不发
```

## 前置条件

| 项目 | 说明 |
| --- | --- |
| WeFlow | 已安装、已登录微信、数据库已连接 |
| WeFlow API | 设置 → API 服务 → 启用；记下端口与 Access Token |
| WeFlow 主动推送 | 设置里启用，WxCat 依赖 `/api/v1/push/messages` |
| 微信 | 与 WeFlow 支持范围一致（通常为当前桌面版） |
| 发送 | `uia` 需要 Windows 桌面会话；`http` 需要外部发送服务 |

## 配置

```json
"backend": {
  "type": "weflow",
  "weflow": {
    "baseUrl": "http://127.0.0.1:5031",
    "token": "WeFlow 的 Access Token",
    "selfWxid": "wxid_机器人账号",
    "sendMode": "uia",
    "senderUrl": "",
    "senderToken": "",
    "uiaScript": "scripts/windows/uia-send.ps1",
    "pollIntervalMs": 0
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `baseUrl` | WeFlow API 地址 |
| `token` | WeFlow Access Token |
| `selfWxid` | 建议填写。WeFlow 不暴露登录账号；留空时 WxCat 会从已发送消息的 `senderUsername` 推断 |
| `sendMode` | `uia` / `http` / `none` |
| `senderUrl` | `http` 模式的发送服务地址 |
| `uiaScript` | `uia` 模式的 PowerShell 脚本路径 |
| `pollIntervalMs` | 可选兜底轮询间隔；0 表示只靠 SSE。SSE 断线会自动重连 |

环境变量：`WXCAT_WEFLOW_BASE_URL`、`WXCAT_WEFLOW_TOKEN`、`WXCAT_WEFLOW_SELF_WXID`、`WXCAT_WEFLOW_SEND_MODE`、`WXCAT_WEFLOW_SENDER_URL`、`WXCAT_WEFLOW_SENDER_TOKEN`。

## 发送模式

### uia（Windows，推荐用于本机）

WxCat 调用 `scripts/windows/uia-send.ps1`：

1. 找到 `Weixin` / `WeChat` 主窗口并置前。
2. `Ctrl+F` 搜索目标显示名，回车打开会话。
3. 文本写入剪贴板，`Ctrl+V`，回车。
4. 图片/文件写入剪贴板，`Ctrl+V`，回车。

限制：

- 会**抢焦点**，发送时不要操作鼠标键盘。
- 按**显示名**搜索；同名联系人或群会发错，建议给机器人使用的会话设置唯一备注。
- 不会创建微信原生 `@` 提醒；群里只是文本 `@昵称`。
- 微信窗口必须可见且已登录。

### http（跨平台 / 容器）

WxCat POST 到 `senderUrl`：

```json
{"to": "wxid_xxx 或 123@chatroom", "text": "hello", "ats": ["wxid_member"]}
{"to": "wxid_xxx", "image": "<base64>", "name": "a.png"}
{"to": "wxid_xxx", "file": "<base64>", "name": "a.pdf"}
```

外部服务负责实际发送（可以是 Akasha-WeChat / AstrWeChat 的发送端，或自建 UIA 服务）。返回 2xx 即视为成功。

### none

只接收，不发送。`send_*` 返回 `retcode 1404`。

## Docker / Linux

WeFlow 提供 Linux x64 版本，WxCat 网关可以跑在容器里：

- `backend.type=weflow`，`baseUrl` 指向 WeFlow 的地址。
- `sendMode` 用 `http`（转发到 Windows 发送服务）或 `none`。
- `uia` 在 Linux 容器里不可用，会返回 `ErrUnsupported`。

## 与 WCF 后端对比

| 项目 | WeFlow | WCF（Wine 注入） |
| --- | --- | --- |
| 微信版本 | 跟随 WeFlow 支持范围，通常可用当前版 | 锁定 3.9.12.51 |
| 注入 | 无 | DLL 注入 |
| 与已装微信共存 | 可以 | 需要独立 Wine 前缀/容器 |
| 发送 | Windows UIA 或外部服务，较慢 | 原生 API，较快 |
| 群成员 | 支持 | 依赖数据库表结构 |
| 撤回 | 仅上报事件 | 仅上报事件 |
| 账号风险 | 读本地数据库，风险较低 | 注入，风险较高 |

## 排错

**`selfWxid` 未设置且推断失败**

`get_login_info` 返回 103。手动填写机器人账号 wxid；可从 WeFlow 联系人/会话数据中查。

**SSE 连不上**

- WeFlow 设置里是否启用 API 与主动推送。
- Token 是否一致。
- 端口是否被防火墙拦截。

**收不到群消息**

WxCat 默认 `groupMode: "at"`，需要文本里出现 `@机器人昵称` 或命中 `atKeywords`。把 `atKeywords` 设置为机器人昵称列表，或临时改成 `"all"`。

**uia 发送失败**

- 微信必须已登录且窗口可见。
- 目标名必须能在 `Ctrl+F` 搜索到。
- PowerShell 需要 `-Sta`（WxCat 已加）。
- 发送图片/文件时微信可能弹出确认框，需要人工处理。
