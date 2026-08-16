<!-- FileTree.zh.md is the Chinese counterpart of FileTree.md -->
# EzTerm — 项目目录结构

> [English](./FileTree.md)

本文档描述 `ezterm` 的仓库结构与关键文件的作用。

---

## 顶层结构

```
.
├── main.go                       # 入口：统一二进制（CLI + daemon 分发）
├── SKILL.md                      # agentskills.io 规范的技能
├── go.mod                        # Go module（ezterm，go 1.25+）
├── go.sum                        # 依赖校验
├── LICENSE                       # MIT 许可证
├── README.md                     # 项目总览、快速开始、CLI 用法
├── README.zh.md                  # README 中文版
├── FileTree.md / FileTree.zh.md  # 本文件（仓库结构）
├── Standards.md / Standards.zh.md # 开发规范
├── API.md / API.zh.md            # HTTP API 参考
├── .gitignore                    # 忽略二进制、数据目录、私钥
├── internal/                     # 私有实现包（不可被外部导入）
├── scripts/
│   ├── e2e.sh                    # 端到端验收（Bash/Unix + Git Bash）
│   └── e2e.ps1                   # 端到端验收（PowerShell/Windows）
└── testdata/                     # 测试夹具
```

## internal/ — 实现包

```
internal/
├── ansi/
│   ├── strip.go                  # 去除 ANSI 转义序列（CSI/OSC/字符集）
│   └── compact.go                # 压缩终端噪音：CRLF→LF、\r 覆盖、空行去重
├── api/
│   └── types.go                  # daemon 与 CLI 共享的 JSON wire 类型
├── buffer/
│   └── buffer.go                 # 追加式输出日志 + 多读者独立游标 + 前缀裁剪
├── cli/
│   ├── cli.go                    # 全局 flag 解析与子命令分发
│   ├── commands.go               # start/send/read/attach/terminate/delete/list 实现
│   ├── keys.go                   # --press-key 解析器与 VT/xterm 按键编码
│   ├── keys_test.go              # 按键解析器表驱动测试
│   ├── client.go                 # daemon HTTP 客户端与退出码映射
│   ├── attach.go                 # 交互式 attach 主循环（raw mode 泵、Ctrl+] 脱离）
│   ├── attach_term.go            # 终端状态保存/恢复 + 尺寸（golang.org/x/term）
│   ├── attach_unix.go            # SIGWINCH 驱动的终端尺寸监听（Unix）
│   ├── attach_windows.go         # 控制台尺寸轮询监听（Windows）
│   ├── spawn.go                  # auto-spawn daemon（探测 /health → 后台拉起）
│   ├── spawn_{windows,unix}.go   # 平台相关后台进程分离
│   ├── env.go                    # ~/.ezterm 与 ~ 展开
│   ├── config_cmd.go             # config local/ssh/list/delete 本地管理
│   ├── config_web.go             # config web：确保 daemon 并打印/打开配置页
│   ├── open_linux.go             # Linux 默认浏览器启动
│   ├── open_unix.go              # macOS/BSD 默认浏览器启动
│   └── open_windows.go           # Windows 默认浏览器启动
├── config/
│   └── config.go                 # 配置默认值、校验、~ 展开
├── configstore/
│   └── store.go                  # 统一 local/ssh 配置存储（configs/local.json + ssh.json）
├── daemon/
│   ├── daemon.go                 # HTTP 服务器、flag、优雅关闭
│   ├── handlers.go               # REST/JSON handler、配置 CRUD、attach 流
│   ├── web.go                    # 内嵌 Web 终端/配置页面 + WebSocket 桥接
│   └── web/                      # xterm.js 终端 + 深色配置页资源（go:embed）
├── message/
│   └── message.go                # 每会话消息索引 + 内容文件持久化
├── session/
│   ├── session.go                # 会话模型、状态机、输入/输出、终止
│   ├── manager.go                # 会话注册表 + 持久化 + 通知
│   ├── proc_local.go             # 本地进程抽象（pty/pipe 双模式）
│   ├── proc_remote.go            # 远端 SSH 进程适配
│   ├── pty_unix.go               # Unix PTY（creack/pty）
│   ├── pty_windows.go            # Windows ConPTY（charmbracelet/x/conpty）
│   └── terminate_{unix,windows}.go # 平台相关优雅终止
├── sshclient/
│   └── client.go                 # SSH 建连、PTY 请求、流桥接
├── sshconfig/
│   └── config.go                 # profile 模型与校验（存储由 configstore 负责）
└── storage/
    └── store.go                  # 原子 JSON 持久化（temp + fsync + rename）
```

模块内按职责分包，无循环依赖：`cli` → `daemon`/`configstore`/`sshconfig`；
`daemon` → `session`/`configstore`/`message`/`storage`；
`session` → `buffer`/`ansi`/`message`/`storage`/`sshclient`/`configstore`。

attach 流程：CLI `attach` 命令打开 `GET /api/sessions/{id}/attach`（daemon 流式推送原始 PTY 字节流，先重放保留画面），按键通过 `POST /api/sessions/{id}/input`（`press_enter=false`）转发，窗口尺寸变化通过 `POST /api/sessions/{id}/resize` 同步；`Ctrl+]` 本地脱离而不终止会话。

Web 终端（`start --web`）复用同一套 attach 原语（输出用 `AttachReader`/`ReadOutput`，输入用 `SendInput`/`Resize`），经 `/web/{id}/ws` 的 WebSocket 连接，因此浏览器标签页与 `attach` 客户端共享同一 PTY 画面。仅对使用 `--web` 启动且为 PTY 模式的会话开启。

`/config` 配置页面与终端页面一起内嵌，使用 `/api/configs` CRUD 端点，支持匹配深色主题的 local/SSH 配置管理。SSH 密码可用于写入，但不会出现在任何响应中。`config web [--open]` 会确保 daemon 运行并打印（或打开）页面 URL。页面与 API 继承 daemon 的绑定地址且无认证，因此默认仅绑定本机非常重要。

---

## 数据目录（默认 `~/.ezterm`）

```
~/.ezterm/
├── sessions.json                 # 会话列表（每次变更原子写入）
├── messages/<session_id>/
│   ├── index.json                # 消息索引
│   └── messages/<msg_id>.json    # 单条消息（input/output/system）
├── configs/
│   ├── local.json                # 本地启动配置映射（0600 权限）
│   └── ssh.json                  # SSH 启动配置映射（0600 权限）
└── ezterm.log                   # auto-spawn daemon 的日志
```
