# 接入 DSH（DeepSeek Harness）

`dsh-onebot` 插件支持三种传输，WxCat 都支持。

## 方式一：forward-ws（推荐，最少配置）

DSH 作为客户端连到 WxCat 的正向 WebSocket 服务端。

`dsh-onebot` 配置：

```yaml
transport: forward-ws
url: ws://<wxcat-host>:3001
accessTokenRef: ONEBOT_ACCESS_TOKEN
```

WxCat 默认已开启 3001 端口。如果设置了 token：

```json
"websocketServers": [
  {
    "name": "ws",
    "enable": true,
    "host": "0.0.0.0",
    "port": 3001,
    "messagePostFormat": "array",
    "token": "dsh-token"
  }
]
```

DSH 的 `ONEBOT_ACCESS_TOKEN` 填同一个值。WxCat 接受 `Authorization: Bearer <token>` 与 `?access_token=<token>`。

## 方式二：reverse-ws

DSH 监听，WxCat 拨入。

`dsh-onebot` 配置：

```yaml
transport: reverse-ws
listenHost: 0.0.0.0
listenPort: 3002
listenPath: /onebot
accessTokenRef: ONEBOT_ACCESS_TOKEN
```

WxCat：

```json
"websocketClients": [
  {
    "name": "dsh",
    "enable": true,
    "url": "ws://<dsh-host>:3002/onebot",
    "messagePostFormat": "array",
    "token": "dsh-token",
    "reconnectInterval": 3000,
    "heartInterval": 30000
  }
]
```

## 方式三：http

DSH 调用 WxCat 的 HTTP API，WxCat 把事件 POST 到 DSH 的 webhook。

`dsh-onebot` 配置：

```yaml
transport: http
url: http://<wxcat-host>:3000
listenHost: 0.0.0.0
listenPort: 3002
listenPath: /onebot
webhookSecretRef: ONEBOT_WEBHOOK_SECRET
```

WxCat：

```json
"httpServers": [
  {
    "name": "http",
    "enable": true,
    "host": "0.0.0.0",
    "port": 3000,
    "messagePostFormat": "array",
    "token": "dsh-token"
  }
],
"httpClients": [
  {
    "name": "dsh",
    "enable": true,
    "url": "http://<dsh-host>:3002/onebot",
    "messagePostFormat": "array",
    "reportSelfMessage": false,
    "token": "dsh-token",
    "secret": "<与 webhookSecret 一致>"
  }
]
```

设置 `secret` 时 WxCat 会附带 `X-Signature: sha1=<hmac>`。

## 验证

1. 私聊机器人，DSH 应产生一条会话事件。
2. 群里 @机器人，应得到回复。
3. 发送图片，DSH 的视觉能力（若启用）应能处理。

## 注意

- DSH 的 OneBot 插件不暴露任意 action，只发送文本和图片。因此 `set_group_ban` 之类的调用不会出现。
- 若使用 `http` 模式，WxCat 的 `httpClients` 与 `httpServers` 必须同时启用。
- DSH 默认要求管理员白名单，首次配置记得设置 `adminUsers` 或 `ONEBOT_ALLOWED_USERS`。
