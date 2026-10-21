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
 "rows": 24, "cols": 80, "ssh_config": "",
 "web_url": "http://127.0.0.1:18766/web/a1b2c3d45678",
 "created_at": "2026-08-02T10:00:00Z",
 "updated_at": "2026-08-02T10:00:00Z", "finished_at": null}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 12 位小写十六进制会话 ID |
| `name` | string | 默认 `session-<id>` |
| `command` / `args` | string / string[] | 要运行的进程；空 = 默认 shell |
| `mode` | `"pty"` \| `"pipe"` | PTY（回显输入）或管道 |
| `status` | `"starting"` \| `"running"` \| `"awaiting_password"` \| `"exited"` \| `"terminated"` | 生命周期状态 |
| `pid` | int | 进程 ID（远端与等待密码时为 0） |
| `exit_code` | int | 结束后有意义 |
| `rows` / `cols` | int | PTY 尺寸 |
| `ssh_config` | string | `""`/`"internal"` = 本机；否则为 profile 名 |
| `web_url` | string | Web 终端 URL（`/web/<id>`）；页面被关闭或会话为 `pipe` 模式时为空串 |
| `pending_prompt` | object，省略 | 当前密码提示，见下 |
| `created_at` / `updated_at` / `finished_at` | string / null | 时间戳 |

`pending_prompt` 仅在会话等待密码时出现，本身**不含密码**：

```json
{"kind": "ssh_password", "label": "Password for deploy@db.example.com:22"}
{"kind": "terminal_password", "label": "[sudo] password for deploy:"}
```

- `ssh_password` —— SSH 配置使用密码认证但没有已存密码；提交密码后才拨号。
- `terminal_password` —— 会话输出当前正显示密码提示（`sudo`、`su`、`passwd`、嵌套 `ssh` 等）。

### `POST /api/sessions`

创建并启动会话。成功返回 `201` + 会话对象；一般创建错误（如 SSH 连接失败、profile 缺失）返回 `400`；显式 `web: true` 搭配 pipe 模式返回 `409`；SSH 配置需要密码提示但 Web 终端被关闭时同样返回 `409`。

Web 终端**默认开启**：省略 `web` 即为 PTY 会话提供 `/web/{id}`。pipe 会话省略 `web` 时会被静默忽略（无法提供终端语义），显式 `web: false` 则在任何模式下关闭该页面。

当 `ssh_config` 指向一个 password 认证但**未存密码**的 profile 时，会话以 `awaiting_password` 状态创建：仍返回 `201`，带 `web_url` 与 `pending_prompt`，但在通过 WebSocket 提交密码之前不会拨号。该配置搭配 `web: false`（或 pipe 模式）返回 `409`，否则将无处输入密码。

```json
{"command": "python3", "args": ["-i"], "mode": "pty",
 "name": "repl", "rows": 24, "cols": 80,
 "ssh_config": "internal", "web": true, "dial_timeout_seconds": 15}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `command` | string | 可执行文件；搭配 `ssh_config` 为空时用 profile 默认 shell |
| `args` | string[] | 命令参数 |
| `mode` | string | `"pty"`（默认）或 `"pipe"` |
| `name` | string | 可选会话名 |
| `rows` / `cols` | int | PTY 尺寸（默认 24×80） |
| `ssh_config` | string | `""`/`"internal"` = 本机；否则为 profile 名 |
| `web` | bool | Web 终端页面；**默认 `true`**。仅 PTY：省略 + pipe 自动忽略，显式 `true` + pipe 返回 `409`，`false` 关闭页面（若需要密码提示则返回 `409`） |
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

停止会话（先优雅后强制）。查询参数：`force=true`（跳过优雅期）、`grace=<秒>`（默认 5）。`awaiting_password` 会话尚无进程：terminate 会立即结束等待，并在输出中说明未提供密码。

```json
{"session": {"id": "a1b2c3d45678", "name": "dev", "status": "terminated", …}}
```

### `DELETE /api/sessions/{id}`

删除已结束的会话。返回 `204`；仍在运行或处于 `awaiting_password` 返回 `409`（请先 terminate）。

### `GET /api/sessions/{id}/files`

列出会话所在主机的目录：本地会话是 daemon 所在机器，SSH 会话是远端主机。文件传输仅在会话处于 `running` 时可用，本地与 SSH 会话都支持。

查询参数：`path`（可选）。省略或为空时列出会话根目录（本地用户 home，或 SSH 登录目录）。`~` 与 `~/...` 同样解析到该根目录，相对路径会拼接到根目录；绝对路径原样使用。`~user` 返回 `400`。

```json
{
  "path": "/home/deploy",
  "parent": "/home",
  "root": "/home/deploy",
  "entries": [
    {"name": "logs", "path": "/home/deploy/logs", "dir": true, "symlink": false,
     "size": 4096, "mode": "0755", "modified": "2026-08-02T10:00:00Z"},
    {"name": "latest", "path": "/home/deploy/latest", "dir": false, "symlink": true,
     "size": 11, "mode": "0777", "modified": "2026-08-02T10:00:00Z"}
  ]
}
```

| 字段 | 说明 |
|---|---|
| `path` | 本次列出的路径（已展开 `~`，未解析符号链接） |
| `parent` | 父目录；文件系统根为空 |
| `root` | 会话传输根目录（本地 home / SSH 登录目录） |
| `entries` | 目录优先，其次符号链接，最后普通文件；组内按名称排序 |

`path` 指向普通文件或符号链接时只返回一个条目。默认包含隐藏文件。本地会话会隐藏并拒绝访问 daemon 数据目录，且跟随符号链接（报告目标类型与大小）；SSH 会话的符号链接标记为 `symlink: true`、`dir: false`，永不跟随。

### `POST /api/sessions/{id}/files`

新建单层目录（父目录必须存在）。成功返回 `201` 与新条目；目标已存在返回 `409`，父目录不存在返回 `404`。

```json
{"path": "logs/2026"}
```

### `DELETE /api/sessions/{id}/files`

删除文件、符号链接（只删链接本身）或**空**目录。成功返回 `204` 无 body。非空目录 `409`；文件系统根或会话根 `400`；daemon 数据目录 `403`。

### `GET /api/sessions/{id}/files/content`

下载文件。响应体是原始字节（非 JSON），边读边发，内存占用与文件大小无关；没有大小上限，也不支持断点续传。`HEAD` 返回同样的头而不发送 body。

```
Content-Type: application/octet-stream
Content-Length: <size>
Content-Disposition: attachment; filename="app.log"
X-Content-Type-Options: nosniff
Cache-Control: no-store
```

下载一律作为附件，绝不内联渲染，避免上传的 HTML 在 daemon 同源执行。目录、SSH 符号链接、特殊文件（FIFO、设备、socket）返回 `400`。

### `POST /api/sessions/{id}/files/content`

上传文件。请求体就是文件原始内容，流式写入目标；1 MiB 的 JSON body 上限不适用于该端点。

| 查询参数 | 默认 | 说明 |
|---|---|---|
| `path` | 必填 | 目标文件路径 |
| `overwrite` | `true` | 设为 `false` 时目标已存在返回 `409` |

```
Content-Type: application/octet-stream
```

其他 Content-Type 返回 `415`。该要求与下方的 Origin 校验共同阻止跨站表单写入文件。

上传先写目标目录中的临时文件，再用原子重命名发布，因此失败或中断不会留下半截目标文件；覆盖时保留原文件权限位。本地会话跟随符号链接（替换目标），SSH 会话拒绝链接。成功返回 `200` 与结果条目：

```json
{"file": {"name": "app.log", "path": "/home/deploy/app.log", "dir": false,
          "symlink": false, "size": 1234, "mode": "0644",
          "modified": "2026-08-02T10:03:00Z"}}
```

若传输中途 daemon 被杀死，可能残留 `.ezterm-upload-*.part`；可用 `DELETE .../files?path=...` 清理。

#### 文件传输的访问边界与状态码

文件端点只接受 loopback 来源；浏览器请求若带 `Origin` 头，必须与请求 Host 一致（跨站页面得到 `403`）。校验在查找会话之前执行，因此非本机调用方无法探测会话是否存在。

| 条件 | 响应 |
|---|---|
| 非 loopback 来源，或跨站 `Origin` | `403` |
| 会话不存在 | `404` |
| 会话不是 `running`（含 `awaiting_password`） | `409` |
| 路径不存在（含悬空符号链接） | `404` |
| 权限不足，或本地路径落在 daemon 数据目录内 | `403` |
| 上传 body 不是 `application/octet-stream` | `415` |
| 非法路径、`~user`、下载目录、SSH 符号链接、特殊文件、删除根目录 | `400` |
| 上传目标是目录、`overwrite=false` 且目标已存在、删除非空目录、mkdir 目标已存在 | `409` |
| SSH/SFTP 传输失败（连接关闭、无 sftp 子系统） | `502` |
| 本地未知 I/O 失败 | `500` |

SSH 会话的文件传输复用该会话自己的 SSH 连接，因此会话结束即不可用，也不需要额外凭据。远端必须启用 `sftp` 子系统（OpenSSH 默认启用）。

### `POST /api/sessions/{id}/resize`

调整 PTY 尺寸。

```json
{"rows": 40, "cols": 120}
```

返回更新后的会话。attach 客户端在连接时以及本地终端窗口尺寸变化时，会将会话调整为其终端尺寸。

### `GET /web/{id}`

为已开启 Web 页面的会话提供内嵌 Web 终端页面（PTY 会话默认开启，`web: false` 关闭）。页面在运行时通过 CDN 加载 xterm.js（无构建步骤），并连接 `/web/{id}/ws`。仅 daemon 绑定地址（默认 `127.0.0.1`）上的机器可访问，无认证。

| 条件 | 响应 |
|---|---|
| 会话不存在 | `404` |
| 会话为 `pipe` 模式 | `409`（Web 终端需要 PTY 会话） |
| 会话以 `web: false` 创建 | `404` |
| 成功 | `200`，`text/html` |

关闭浏览器标签页不会终止会话；会话继续运行，与 `attach` 一致。多个标签页等同多个 attach 客户端，共享同一 PTY 画面。

页面用内嵌模态弹窗提示断开：进程结束时提示「Session disconnected」（即下文的 `session-end` 帧）；WebSocket 断开但会话仍在运行时提示「Connection lost」，并自动重连，重连成功后恢复终端。

会话等待密码时（任一 `pending_prompt`），页面改为显示密码输入框。密码由用户亲手输入、经 WebSocket 提交，**不写 messages、不写日志、不回显到终端**。

### `GET /web/{id}/ws`

嵌入式页面使用的 WebSocket 端点。访问规则与 `GET /web/{id}` 相同。默认接受请求 Host 的 origin，仅监听 daemon 绑定地址。

协议（一条双向连接）：

- 服务器 → 客户端 **二进制帧** 携带原始 PTY 输出字节（含 ANSI），先重放已保留画面。
- 客户端 → 服务器 **二进制帧** 携带原始输入字节（按键、功能键、粘贴），原样写入会话 stdin。
- 客户端 → 服务器 **文本帧** 携带 JSON 控制消息：`resize` 与 `secret`。
- 服务器 → 客户端 **文本帧** 携带 JSON 状态消息：`prompt`、`prompt-cleared`、`secret-result`、`session-end`。

客户端 → 服务器：

```json
{"type": "resize", "rows": 40, "cols": 120}
{"type": "secret", "text": "<密码>"}
```

- `resize` 更新 PTY 尺寸。`awaiting_password` 会话会记录尺寸并返回成功，因此页面一打开不会因尚未拨号而报错。
- `secret` 提交密码。仅当连接来自 loopback 地址时才处理；其他来源会收到 `secret-result{ok:false}` 且内容被忽略。该文本不写日志、不写成 input 消息、不回显。

服务器 → 客户端：

```json
{"type": "prompt", "kind": "ssh_password", "label": "Password for u@h:22"}
{"type": "prompt-cleared"}
{"type": "secret-result", "ok": true}
{"type": "secret-result", "ok": false, "error": "ssh: authentication failed: …"}
{"type": "session-end"}
```

- `prompt` 通告当前密码提示。连接建立时若已有待处理提示，会立即重放，因此重连或后打开的页面仍能弹出对话框。
- `prompt-cleared` 表示提示消失：已被回答，或程序换行继续输出。
- `secret-result` 报告 `secret` 帧的结果。`ok: false` 时页面显示错误并保留对话框；`ssh_password` 失败时会话已结束，随后会收到 `session-end`。
- `session-end` 在进程退出、保留输出全部发送完毕后发送一次，随后服务器以正常关闭码关闭连接。传输层断开（网络中断、daemon 重启、页面关闭）**不会**发送该帧，因此客户端可区分「会话结束」与「连接断开」。

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
 "user": "deploy", "auth_method": "password", "key_path": "", "shell": "",
 "has_password": true, "password_prompt": false}
```

`has_password` 只表示是否已存密码（永不返回值本身）。`password_prompt` 表示该 profile 使用密码认证但没有已存密码：每次启动都在 Web 终端询问。

### `POST /api/configs/{name}`

创建或更新配置。请求体的 `type`（`local` 或 `ssh`）决定写入哪个存储。覆盖同一类型的同名配置即更新；复用另一类型已占用的名称返回 `409`。local pipe 配置必须提供非空 `command`。

```json
{"type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"type": "ssh", "host": "db.example.com", "port": 22, "user": "deploy",
 "auth_method": "password", "password": "...", "key_path": "", "shell": "/bin/bash"}
```

SSH `password` 为**只写**：任何端点都不会返回它。更新既有 password 认证的 SSH 配置时，将 `password` 留空会保留已存值。发送 `password_prompt: true` 会清空已存密码并切到「每次启动在 Web 终端询问」；新建 password 认证配置既无密码也无已存值时同样合法，语义即为每次询问。key 认证配置但无 `key_path`，返回 `400`。配置名须匹配 `[A-Za-z0-9_-]+`。

| 条件 | 响应 |
|---|---|
| 成功（创建或更新） | `200` + 保存后的 `ConfigDetail` |
| local pipe 配置缺少 `command` | `400` |
| 跨类型重名 | `409` |
| 非法类型、缺失 key path | `400` |

### `DELETE /api/configs/{name}`

按名称删除配置（两种类型均可）。成功返回 `204`；未知返回 `404`。

### `GET /config`

内嵌的深色主题网页配置页面。页面列出配置，并通过上述端点创建、编辑、删除 local 与 SSH 配置；随二进制内嵌（无构建步骤，`config.js` 与 `config.css` 位于 `/config/app.js` 与 `/config/style.css`）。与 Web 终端及所有 `/api/*` 路由一样，仅 daemon 绑定地址（默认 `127.0.0.1`）可访问且无认证。关闭页面不影响正在运行的会话。

## 会话生命周期

```
POST /api/sessions          → starting → running
POST /api/sessions（空密码 SSH） → awaiting_password
  ├─ 提交密码且拨号成功        → running
  ├─ 提交密码但拨号/认证失败    → exited（exit code 1）
  ├─ 300 秒内未提交            → terminated（输出含超时说明）
  └─ POST .../terminate        → terminated
进程自然退出               → exited
POST .../terminate          → terminated
DELETE /api/sessions/{id}   → 从列表移除（须已结束）
```

自然退出的会话保留在列表中，直到被删除。daemon 重启后，之前运行中（以及 `awaiting_password`）的会话恢复为 `exited` 历史记录；消息仍可从数据目录读取。恢复的会话不会再保留密码提示。
