# 接入 AstrBot

AstrBot 的 `aiocqhttp` 适配器只支持**反向 WebSocket**：AstrBot 启动一个 WebSocket 服务端，OneBot 实现（这里是 WxCat）主动拨过去。

## 1. AstrBot 侧

后台 → 消息平台 → 新增 `aiocqhttp`：

| 配置项 | 值 |
| --- | --- |
| `ws_reverse_host` | `0.0.0.0` |
| `ws_reverse_port` | `6199` |
| `ws_reverse_token` | 自定义，例如 `wxcat-astrbot` |

AstrBot 接受的路由是 `/ws`、`/ws/event`、`/ws/api`。WxCat 使用 `/ws`（Universal）。

## 2. WxCat 侧

编辑 `config/wxcat.json`：

```json
"websocketClients": [
  {
    "name": "astrbot",
    "enable": true,
    "url": "ws://astrbot:6199/ws",
    "messagePostFormat": "array",
    "reportSelfMessage": false,
    "token": "wxcat-astrbot",
    "reconnectInterval": 3000,
    "heartInterval": 30000,
    "debug": false
  }
]
```

要点：

- `url` 必须以 `/ws` 结尾。
- `token` 与 AstrBot 的 `ws_reverse_token` 完全一致。WxCat 发送的是 `Authorization: Token <token>`，AstrBot 同时接受 `Token` 与 `Bearer`。
- 容器网络里主机名要用 AstrBot 容器的服务名；跨主机时用可达 IP。
- 保存后 `docker compose restart wxcat`。

## 3. 验证

AstrBot 日志：

```
aiocqhttp(OneBot v11) 适配器已连接。
```

WxCat 日志：

```
onebot sink connected sink=reverse:astrbot
onebot identity ready wxid=wxid_xxx self_id=1234567890
```

然后：

1. 用另一个微信号私聊机器人，应收到回复。
2. 在群里 `@机器人 你好`，应收到回复；不 @ 时默认不响应（`groupMode: "at"`）。
3. 发送图片，AstrBot 日志应出现图片消息；如果视觉模型启用，应能描述图片。

## 4. 常见问题

**AstrBot 日志报 `401` 或 `403`**

token 不一致。检查两端；WxCat 的 `WXCAT_TOKEN` 环境变量会覆盖空 token，容易造成"看起来配了但没生效"。

**连接成功但收不到消息**

- `groupMode` 默认 `at`，群聊不 @ 不会上报。
- 检查 WxCat 日志里是否有 `backend state changed online=true`。
- 在管理面板 `/api/status` 看 `online` 与 `connections`。

**收到消息但回复失败**

- 看 WxCat 日志里的 `send failed` 与状态码。
- 微信 3.9 对频繁发送有限制，短时间内大量回复可能被静默丢弃。

**图片是 data URL**

把 `mediaBaseURL` 设置为 AstrBot 能访问的地址（例如 `http://192.168.1.10:3000`），并确保 3000 端口可达。否则 WxCat 用 base64 data URL，部分框架会拒绝过长消息。

**ID 变了导致会话错乱**

不要删除 `data/idmap.json`。如果必须重置，AstrBot 侧的会话也要一起清理。
