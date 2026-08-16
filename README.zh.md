<!-- README.zh.md is the Chinese counterpart of README.md -->
# EzTerm

<p align="center">
  <strong>面向 AI 代理的交互式终端会话工具</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8.svg" alt="Go 1.25+">
  <img src="https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey" alt="macOS / Linux / Windows">
  <img src="https://img.shields.io/badge/Skill-agentskills.io-blue.svg" alt="Skill">
  <img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="MIT License">
</p>

<p align="center">
  <a href="./README.md"><img src="https://img.shields.io/badge/EN-English-blue.svg" alt="English"></a>
</p>

<p align="center">
  <a href="./README.md">English</a> | <strong>中文</strong>
</p>

---

`ezterm` 是一个 **CLI + Skill** 项目：一个轻量级命令行工具（CLI + daemon）配一套现成可用的 Agent Skill。二者结合，让 AI 代理（以及人类）能够**多轮运行、驱动和监控交互式终端会话**——REPL、shell、安装器、服务器、远程 SSH 等场景，支持跨多轮的发送输入与读取输出。

CLI 负责实际的会话管理；Skill 封装该 CLI，使支持 SKILL.md 的工具（pi、Claude Code 等）开箱即用。详见 [Agent Skill](#agent-skill) 一节。

## 致谢 termcp

本项目代码参考并衍生自 [termcp](https://github.com/open-mcp-ai/termcp)，特此致谢。
如需 MCP 版本或更全面的功能（如 SFTP、端口转发等），请使用该开源项目：https://github.com/open-mcp-ai/termcp

## 快速开始

```bash
# 构建单一二进制（CLI 与 daemon 合一）。
go build -o ezterm .

# 首次命令会自动在后台拉起 daemon（默认端口 18766，数据目录 ~/.ezterm）。
./ezterm config local --name repl --command python3
./ezterm start --name repl
./ezterm config local --name dev --mode pty
./ezterm start --name dev --web
./ezterm send <id> --text '2 + 2' --press-enter
./ezterm read <id> --timeout 30

./ezterm terminate <id>
./ezterm delete <id>
```

## 特性

- **交互式会话** — 以 PTY 或管道模式启动进程，跨多轮发送输入、阻塞或非阻塞读取输出。
- **自动拉起 daemon** — 后台服务在首次使用时启动，同时提供 `ezterm daemon` 子命令。
- **保存的启动配置** — 通过 `config local/ssh` 定义命名的本地或 SSH 会话配置，再由 `start --name` 启动；SSH 支持密码或私钥认证。
- **稳定的 `--json` 输出** — 面向 Skill/工具的可解析输出。
- **多游标输出缓冲** — 每个 reader 独立游标、保留历史、可设上限的阻塞读取。
- **持久化** — 数据目录保存 `sessions.json` 与会话消息日志；重启后历史会话恢复为已结束记录。
- **跨平台** — Unix 使用 `creack/pty`，Windows 使用 ConPTY；管道模式全平台可用。
- **共享终端 attach** — `attach <id>` 以 raw mode 进入会话的实时 PTY 画面（类似 `tmux attach`）：按键与窗口尺寸自动转发，输出实时流回，`Ctrl+]` 脱离而不终止会话。
- **精确按键输入** — `send --press-key` 发送单个标准终端按键或组合键（如 `ctrl+c`、`enter`、`ctrl+shift+up`、`f5`），不追加换行；PTY 与 pipe 会话均可使用。
- **本机 Web 终端** — `start --web` 为显式开启的 PTY 会话提供 xterm.js 浏览器画面，通过 WebSocket 实时传输输出、输入、粘贴和尺寸；daemon 默认只监听本机。
- **本机配置网页** — `ezterm config web [--open]`（或 `http://127.0.0.1:18766/config`）在浏览器中打开深色主题页面，用于创建、编辑、删除已保存的本地/SSH 配置，配色与 Web 终端一致。SSH 密码为只写：页面永不回显已存密码。

## 命令行

```
ezterm [全局参数] <命令> [参数]
```

| 命令 | 说明 |
|---|---|
| `start` | 按保存的配置启动会话：`--name <配置名>`，可选 `--web`、`--rows`、`--cols`、`--timeout` |
| `send <id>` | 发送输入：`--text`、`--press-enter`，或用 `--press-key` 发送单个按键/组合键（如 `ctrl+c`、`enter`、`f5`） |
| `read <id>` | 读取新输出：`--reader`、`--timeout <秒>`、`--raw`、`--max-bytes` |
| `attach <id>` | 以 raw mode 进入运行中的 PTY 会话；`Ctrl+]` 脱离 |
| `terminate <id>` | 停止会话（先优雅后强制） |
| `delete <id>` | 删除已结束的会话 |
| `list` | 列出会话 |
| `config local\|ssh` | 创建 / 更新启动配置：`--name` 及类型专属参数 |
| `config list` | 列出配置（可选 `--type local\|ssh`） |
| `config delete` | 按 `--name` 删除配置 |
| `config web` | 确保 daemon 运行并打印配置页 URL（`--open` 打开浏览器） |
| `health` | 探测 daemon |
| `daemon` | 前台运行 daemon |
| `version` | 打印版本 |

全局参数（命令前后均可）：`--port`（18766）、`--data-dir`（`~/.ezterm`）、`--json`、`--log-level`。

**退出码：** `0` 成功 · `1` 会话不存在 · `2` 其他错误。

完整示例、启动配置、配置网页与 Skill 工作流见 [`SKILL.md`](./SKILL.md)。

配置页面及对应 API 与 daemon 绑定地址一致。默认为仅本机（`127.0.0.1`）；若用 `--host` 暴露 daemon，也会暴露配置 CRUD，请仅在可信网络使用。

### 示例

```bash
# 创建本地配置并启动（--web 可选；rows/cols 默认 24/80）。
ezterm config local --name dev --command bash --mode pty
ezterm start --name dev --web

# 从保存的配置启动一次性管道命令。
ezterm config local --name df --command df --args -h --mode pipe
ezterm start --name df

# 在自己的终端接管正在运行的 shell（Ctrl+] 脱离）。
ezterm attach <id>

# 发送单个终端按键，不追加换行。
ezterm send <id> --press-key ctrl+c
# 其他示例：ctrl+shift+up、left、enter、f5

# 通过保存的配置连接 SSH。
ezterm config ssh --name prod --host db.example.com --user deploy --auth key --key-path ~/.ssh/id_ed25519
ezterm start --name prod

# 管理配置。
ezterm config list
ezterm config delete --name prod

# 在浏览器中打开配置网页。
ezterm config web --open
# → configuration page: http://127.0.0.1:18766/config

# 机器可读输出。
ezterm --json list
ezterm --json read <id> --timeout 0
```

## Agent Skill

项目附带一个符合 [agentskills.io](https://agentskills.io) 规范的 Skill：
[`SKILL.md`](./SKILL.md)（适用于 pi、Claude Code 等支持 SKILL.md 的工具）。将仓库根目录作为 skill 路径传给工具即可让代理通过 CLI 启动/发送/读取/终止会话。

## HTTP API

daemon 提供小型 JSON API（`/health`、`/api/sessions`、`/api/sessions/{id}/output`、`/api/configs` 等），以及按需开启的 Web 终端页面和 WebSocket：`/web/{id}`、`/web/{id}/ws`，另有配置管理页面 `/config`。详见 [`API.md`](./API.md)（中文版 [`API.zh.md`](./API.zh.md)）。

## 项目结构与规范

- [`FileTree.md`](./FileTree.md)（中文 [`FileTree.zh.md`](./FileTree.zh.md)）— 目录结构说明。
- [`Standards.md`](./Standards.md)（中文 [`Standards.zh.md`](./Standards.zh.md)）— 开发规范。
- [`API.md`](./API.md) — HTTP API 参考。

## License

MIT — 见 [`LICENSE`](./LICENSE)。
