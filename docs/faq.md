# 常见问题

## 容器起不来

**`docker compose up` 卡在构建**

构建阶段要从 GitHub 下载微信安装包与 WCF 的 DLL。国内网络建议配置代理，或预先下载后放进 `docker/` 目录并修改 Dockerfile 的 `COPY`。

**首次启动很久**

镜像已内置微信，首次启动只是把内置 Wine 前缀复制到 `./wine` 卷，通常几十秒。如果超过 2 分钟，检查 `./wine` 所在磁盘速度与剩余空间（约需 2 GB）。

**`/app/config` 权限错误**

容器以 root 启动并在 entrypoint 里 `chown` 给 `wechat`。如果你把宿主目录挂载成只读或特殊权限，会失败。用普通目录即可。

## 微信登录

**noVNC 打开是黑屏**

Xvfb 还没起来或微信未启动。等 10 秒；仍黑屏就看 `docker compose logs wxcat | grep -E 'Xvfb|wechat'`。

**扫码后一直"等待登录"**

注入失败。执行：

```bash
docker compose exec wxcat /opt/wcf/inject.sh
```

观察输出。常见原因：微信版本不匹配、`spy.dll` 不在安装目录、Wine 缺少运行库。

**登录后几分钟掉线**

微信对 Wine 环境有风控。建议：不要频繁重启容器、不要用主号、保持 `./wine` 持久化。

## OneBot

**AstrBot 连不上**

- 反向 WS 地址必须以 `/ws` 结尾。
- token 两端一致；`WXCAT_TOKEN` 环境变量会覆盖空 token。
- 容器之间用服务名或宿主机 IP，不能用 `127.0.0.1`。

**收不到群消息**

默认 `groupMode: "at"`，只有被 @ 才上报。改成 `"all"` 可收全部群消息（不推荐，容易刷屏）。

**发图失败**

- 检查 `data/media` 是否可写。
- 大图片走 base64 时可能超过框架限制，设置 `mediaBaseURL` 让框架用 URL 拉取。
- 远程网关（arm64）还要检查 `WXCAT_WCF_MEDIA_URL` 是否指向微信主机的 11090，以及 `WXCAT_WCF_MEDIA_TOKEN` 是否一致。

**消息 ID 对不上**

不要删除 `data/idmap.json`。删除后所有数字 ID 重新生成，AstrBot / DSH 的历史会话会错乱。

**`retcode 1404`**

该 action 在微信侧没有对应能力（撤回、禁言、踢人、点赞、表情回应等）。这是预期行为。

## 性能

- 内存 4–5 GB 是常态（Wine + 微信 + 虚拟桌面）。
- 图片解密会占用临时磁盘，位于 `data/media` 与容器 `/tmp`。
- 群消息量大时建议保持 `groupMode: "at"`。

## arm64

一体化镜像不能在 arm64 上运行。arm64 请用网关镜像 + 远程微信后端，见 [arm64.md](arm64.md)。

## 安全

- noVNC（6080）等于远程桌面，**不要直接暴露到公网**。至少改 `VNC_PASSWORD`，最好只监听内网。
- 管理面板（6099）设置 `webui.token`。
- OneBot 端口（3000/3001）设置 token，尤其跨网络时。
- WeChatFerry 桥端口（11086/11087）**没有任何认证**，只在内网/VPN 内发布，并限制来源 IP。
- 容器需要 `SYS_ADMIN`？不需要。WCF 注入只用 `CreateRemoteThread`，不需要额外 capability。若你换了 MimicWX 后端，它需要 `SYS_ADMIN` / `SYS_PTRACE`。
