# API — EzTerm HTTP API 参考

`ezterm` daemon 在 `127.0.0.1:<port>`（默认 `18766`）上提供小型 JSON API。CLI 是主要消费者，所有端点也可直接通过 HTTP 使用。

> [English](./API.md)

## 约定

- 基础地址：`http://127.0.0.1:<port>`
- 请求体为 JSON（`Content-Type: application/json`），上限 1 MiB。
- 响应为 JSON 对象；错误使用 `{"error": "<message>"}` 与相应状态码。
- 时间为 RFC 3339 UTC。

## 端点

### `GET /health`

CLI 用于探测 daemon 是否存活。

```json
{"status": "ok"}
```

### `GET /api/sessions`

列出所有会话（含已结束的历史），按创建时间排序。

```json
{"sessions": [ {"id": "a1b2c3d45678", "name": "dev", "status": "exited", …} ]}
```

### `GET /api/sessions/{id}`

获取单个会话；未知返回 `404`。

```json
{"id": "a1b2c3d45678", "name": "dev", "command": "", "args": null,
 "mode": "pty", "status": "running", "pid": 1234, "exit_code": 0,
 "rows": 24, "cols": 80, "ssh_config": "", "web_url": "",
 "created_at": "2026-08-02T10:00:00Z",
 "updated_at": "2026-08-02T10:00:00Z", "finished_at": null}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 12 位小写十六进制会话 ID |
| `name` | string | 默认 `session-<id>` |
| `command` / `args` | string / string[] | 要运行的进程；空 = 默认 shell |
| `mode` | `"pty"` \| `"pipe"` | PTY（回显输入）或管道 |
| `status` | `"starting"` \| `"running"` \| `"exited"` \| `"terminated"` | 生命周期状态 |
| `pid` | int | 进程 ID（远端为 0） |
| `exit_code` | int | 结束后有意义 |
| `rows` / `cols` | int | PTY 尺寸 |
| `ssh_config` | string | `""`/`"internal"` = 本机；否则为 profile 名 |
| `web_url` | string | Web 终端 URL（`/web/<id>`）；未使用 `--web` 时为空串 |
| `created_at` / `updated_at` / `finished_at` | string / null | 时间戳 |

### `POST /api/sessions`

创建并启动会话。成功返回 `201` + 会话对象；一般创建错误（如 SSH 连接失败、profile 缺失）返回 `400`，`web` 搭配 pipe 模式返回 `409`。

```json
{"command": "python3", "args": ["-i"], "mode": "pty",
 "name": "repl", "rows": 24, "cols": 80,
 "ssh_config": "internal", "web": false, "dial_timeout_seconds": 15}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `command` | string | 可执行文件；搭配 `ssh_config` 为空时用 profile 默认 shell |
| `args` | string[] | 命令参数 |
| `mode` | string | `"pty"`（默认）或 `"pipe"` |
| `name` | string | 可选会话名 |
| `rows` / `cols` | int | PTY 尺寸（默认 24×80） |
| `ssh_config` | string | `""`/`"internal"` = 本机；否则为 profile 名 |
| `web` | bool | 开启 Web 终端页面；仅 PTY 模式（pipe 返回 `409`） |
| `dial_timeout_seconds` | int | SSH 连接超时（默认 15） |

### `POST /api/sessions/{id}/input`

向会话写入输入。返回 `{"ok": true}`；会话已停止返回 `409`。

```json
{"text": "2 + 2", "press_enter": true}
```

`press_enter: true` 追加 `\n`（Unix）或 `\r\n`（Windows）。

CLI 的 `--press-key` 字节（键名、控制字节、CSI 序列）通过 `text` 字段传输且 `press_enter: false`；协议本身无变更。

### `GET /api/sessions/{id}/output`

按 reader 读取新输出。查询参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `reader_id` | `0` | reader 游标；`0` 是默认 CLI reader |
| `timeout` | `30` | 最多阻塞 N 秒（浮点）；`0` = 非阻塞；上限 300 |
| `raw` | `false` | 保留 ANSI 转义序列 |
| `max_bytes` | `0` | 限制返回字节数；`0` = 不限制 |

```json
{"data": "4\n", "eof": false}
```

`eof: true` 表示会话已结束且所有保留输出已消费。

### `GET /api/sessions/{id}/attach`

将会话的原始 PTY 输出流式推送给 attach 客户端。流从输出缓冲保留内容的起点开始重放（恢复当前画面），随后持续跟随新输出，直到会话结束（流 EOF）或客户端断开。

| 条件 | 响应 |
|---|---|
| 会话不存在 | `404` |
| 会话为 `pipe` 模式 | `409`（attach 需要 PTY 会话） |
| 成功 | `200`，`Content-Type: application/octet-stream` |

这不是 JSON：响应体是原始终端字节流（含 ANSI 转义序列），按块 flush。CLI 的 `attach` 命令消费该端点，并通过 `POST /input`（`press_enter: false`）逐键发送输入。多个客户端可同时 attach 同一会话并共享同一画面。attach 到已结束的会话会重放最终画面后关闭。

### `POST /api/sessions/{id}/readers`

分配一个位于当前输出末尾的新 reader。返回 `201`：

```json
{"reader_id": 2}
```

### `POST /api/sessions/{id}/terminate`

停止会话（先优雅后强制）。查询参数：`force=true`（跳过优雅期）、`grace=<秒>`（默认 5）。

```json
{"session": {"id": "a1b2c3d45678", "name": "dev", "status": "terminated", …}}
```

### `DELETE /api/sessions/{id}`

删除已结束的会话。返回 `204`；仍在运行返回 `409`（请先 terminate）。

### `POST /api/sessions/{id}/resize`

调整 PTY 尺寸。

```json
{"rows": 40, "cols": 120}
```

返回更新后的会话。attach 客户端在连接时以及本地终端窗口尺寸变化时，会将会话调整为其终端尺寸。

### `GET /web/{id}`

为以 `web: true` 创建的会话提供内嵌 Web 终端页面。页面在运行时通过 CDN 加载 xterm.js（无构建步骤），并连接 `/web/{id}/ws`。仅 daemon 绑定地址（默认 `127.0.0.1`）上的机器可访问，无认证。

| 条件 | 响应 |
|---|---|
| 会话不存在 | `404` |
| 会话为 `pipe` 模式 | `409`（Web 终端需要 PTY 会话） |
| 会话未以 `web: true` 创建 | `404` |
| 成功 | `200`，`text/html` |

关闭浏览器标签页不会终止会话；会话继续运行，与 `attach` 一致。多个标签页等同多个 attach 客户端，共享同一 PTY 画面。

### `GET /web/{id}/ws`

嵌入式页面使用的 WebSocket 端点。访问规则与 `GET /web/{id}` 相同。默认接受请求 Host 的 origin，仅监听 daemon 绑定地址。

协议（一条双向连接）：

- 服务器 → 客户端 **二进制帧** 携带原始 PTY 输出字节（含 ANSI），先重放已保留画面。
- 客户端 → 服务器 **二进制帧** 携带原始输入字节（按键、功能键、粘贴），原样写入会话 stdin。
- 客户端 → 服务器 **文本帧** 携带 resize JSON：

```json
{"type": "resize", "rows": 40, "cols": 120}
```

会话结束且保留输出全部发送完毕后，服务器以正常关闭码关闭连接。

### `GET /api/configs`

列出 local 与 SSH 启动配置摘要（不含机密字段）。

```json
{"configs": [
  {"name": "dev", "type": "local", "command": "bash", "mode": "pty"},
  {"name": "prod", "type": "ssh", "host": "db.example.com", "port": 22,
   "user": "deploy", "auth_method": "key", "default_shell": ""}
]}
```

### `GET /api/configs/{name}`

获取单个配置的完整非机密详情（local 配置包含 `args`；SSH 配置永不包含已存密码）。未知返回 `404`。

```json
{"name": "dev", "type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"name": "prod", "type": "ssh", "host": "db.example.com", "port": 22,
 "user": "deploy", "auth_method": "password", "key_path": "", "shell": ""}
```

### `POST /api/configs/{name}`

创建或更新配置。请求体的 `type`（`local` 或 `ssh`）决定写入哪个存储。覆盖同一类型的同名配置即更新；复用另一类型已占用的名称返回 `409`。local pipe 配置必须提供非空 `command`。

```json
{"type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"type": "ssh", "host": "db.example.com", "port": 22, "user": "deploy",
 "auth_method": "password", "password": "...", "key_path": "", "shell": "/bin/bash"}
```

SSH `password` 为**只写**：任何端点都不会返回它。更新既有 password 认证的 SSH 配置时，将 `password` 留空会保留已存值。新建 password 认证配置但无密码、或 key 认证配置但无 `key_path`，返回 `400`。配置名须匹配 `[A-Za-z0-9_-]+`。

| 条件 | 响应 |
|---|---|
| 成功（创建或更新） | `200` + 保存后的 `ConfigDetail` |
| local pipe 配置缺少 `command` | `400` |
| 跨类型重名 | `409` |
| 非法类型、缺失 SSH 凭据 | `400` |

### `DELETE /api/configs/{name}`

按名称删除配置（两种类型均可）。成功返回 `204`；未知返回 `404`。

### `GET /config`

内嵌的深色主题网页配置页面。页面列出配置，并通过上述端点创建、编辑、删除 local 与 SSH 配置；随二进制内嵌（无构建步骤，`config.js` 与 `config.css` 位于 `/config/app.js` 与 `/config/style.css`）。与 Web 终端及所有 `/api/*` 路由一样，仅 daemon 绑定地址（默认 `127.0.0.1`）可访问且无认证。关闭页面不影响正在运行的会话。

## 会话生命周期

```
POST /api/sessions          → starting → running
进程自然退出               → exited
POST .../terminate          → terminated
DELETE /api/sessions/{id}   → 从列表移除（须已结束）
```

自然退出的会话保留在列表中，直到被删除。daemon 重启后，之前运行中的会话恢复为 `exited` 历史记录；消息仍可从数据目录读取。
