<!-- Standards.zh.md is the Chinese counterpart of Standards.md -->
# Standards — EzTerm 开发规范

本文档定义 `ezterm` 项目的代码风格、工程约定与接口规范。

> [English](./Standards.md)

---

## 1. Go 编码规范

### 格式

- **强制 `gofmt`。** 所有 Go 源文件必须通过 `gofmt`；提交前运行 `gofmt -l .` 并修复报告的文件。
- `go vet ./...` 必须通过。
- import 分组遵循标准 `gofmt` 顺序：标准库 → 外部模块 → `ezterm/...`。

### 命名

- 遵循 [Go 命名规范](https://go.dev/doc/effective_go#names)：导出标识符带注释；未导出辅助函数小写。
- 包名简短、小写、单词（`session`、`buffer`、`sshclient`）。
- 缩写大写（`SSH`、`PTY`、`ID`、`URL`）。
- 测试文件使用 `_test.go` 后缀；测试辅助函数调用 `t.Helper()`。

### 错误处理

- 带上下文包装错误：`fmt.Errorf("create session: %w", err)`；绝不静默吞掉错误——至少记录日志。
- 使用 `errors.Is` / `errors.As` 判断哨兵错误（`buffer.ErrClosed`、`storage.ErrCorrupt`、`sshconfig.ErrNotFound`）。
- 在包边界返回哨兵错误供调用方分支。

### 并发

- 会话状态写入串行化：stdin 经 `stdinMu`；状态与退出码经 `sync.Once` 只写一次（见 `exitOnce`）。
- 终止幂等：`terminateOnce`（sync.Once）保护 `Terminate`。
- 新增共享状态必须由独立 mutex 或 `sync.Once` 保护，并在注释中说明锁的归属。
- 优先用 `context.Context` 做取消，而非临时 goroutine 标志。

### 平台相关代码

- PTY 实现位于 `ptySession` 接口之后（`pty_unix.go` / `pty_windows.go`），带 `//go:build` 标签。
- 终止逻辑按平台拆分（`terminate_unix.go` / `terminate_windows.go`）；公共 `proc` 接口保持平台无关。
- Windows 特性（ConPTY、`CREATE_NO_WINDOW`）隔离在带标签的文件中。

---

## 2. 测试规范

- 纯逻辑默认使用**表驱动测试**。
- 新功能必须覆盖成功路径、错误路径与（相关时）并发。
- 会话测试使用辅助子进程（`--` 参数模式），同一套机制可经 HTTP API 触发且跨平台。
- `go test ./...` 必须通过，internal 包须通过 `-race`。
- `testdata/` 仅放夹具；绝不写运行时状态。

---

## 3. 提交约定

使用 [Conventional Commits](https://www.conventionalcommits.org/)：

```
<type>(<scope>): <subject>
```

- **类型：** `feat`、`fix`、`docs`、`refactor`、`chore`、`test`、`ci`。
- **范围**（可选）：包或领域，如 `session`、`cli`、`daemon`、`sshconfig`、`storage`。
- **主题：** 祈使句、小写；面向内部的说明可用中文，但需以英文类型前缀。
- 每个提交是单一逻辑变更。

示例：

```
feat(session): add PTY lifecycle with platform ptySession adapters
fix(cli): accept flags after positional session IDs
docs: add HTTP API reference
```

---

## 4. 文档约定

- **默认英文。** 中文版使用 `.zh.md` 后缀（`README.zh.md`、`FileTree.zh.md`、`Standards.zh.md`、`API.zh.md`）。
- 每个双语文件在顶部链接对应版本。
- 根级文档：`README.md`、`FileTree.md`、`Standards.md`、`API.md`。
- 保持单一事实来源：优先链接而非重复。

---

## 5. 接口规范

- 完整 HTTP API 在 [`API.md`](./API.md) 中说明；daemon 每个端点都必须记录。
- `internal/api/types.go` 是 JSON wire 类型的单一事实来源。
- CLI 是主要消费者；退出码稳定：`0` 成功、`1` 会话不存在、`2` 其他错误。`--json` 输出稳定且无人类装饰。
- `send --press-key` 每次调用接受一个大小写不敏感的按键表达式。CLI 将 VT/xterm 字节通过 `InputRequest.Text` 发送，并固定 `PressEnter=false`；它与 `--text`、`--press-enter` 互斥，非法表达式返回退出码 `2`。
- 向后兼容：删除或重命名端点/CLI flag 属于破坏性变更，需配合版本号。
- **安全：** 绝不提交凭据、密钥或私密材料。SSH profile 可包含用户 `~/.ezterm` 数据目录中的密码/密钥，但绝不能进入仓库或 profile 骨架。

## 6. 依赖

- WebSocket 支持使用 `github.com/coder/websocket`（支持 context 取消，无传递依赖）。
- Web 终端页面由 `internal/daemon/web/` 下的 `go:embed` 资源提供；无前端构建步骤。xterm.js 在运行时通过 CDN 加载，不纳入仓库。
