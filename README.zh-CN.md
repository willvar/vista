# Vista

[English](./README.md)

![Vista 中文桌面界面与荣耀 WIN、一加 15、iPhone Pro Max 的游戏手机对比](./docs/assets/vista-hero.zh-CN.png)

Vista 是适用于 Linux、Windows 和 macOS 的通用型智能体。在浏览器界面描述目标，智能体可使用文件、shell 命令、网络查询、MCP 工具及技能完成工作。每个已打开的会话拥有独立的 Worker 进程，会话和历史保存在 Vista 的运行环境中。

## 开始使用

从 [GitHub Releases](https://github.com/willvar/vista/releases/latest) 下载适合当前系统的压缩包并解压。

运行 `vista`；Windows 也可以直接双击 `vista.exe`。它会在后台启动服务并打开默认浏览器，再次打开会复用已运行的实例。默认地址为 `http://127.0.0.1:5186/`。

Windows 服务运行时会显示托盘图标，单击或双击可打开 Vista；右键提供 Restart（重启）、Status（状态）和 Stop（停止）。Status 通过原生窗口展示服务、会话、进程、内存及构建信息。Stop 停止服务并移除图标。

发送第一条消息前，先在设置中连接模型服务商。内置连接封装认证和协议配置；自定义连接可独立选择对话 API 格式与认证方式。可用模型由所连接的账号或服务决定。

| 命令 | 作用 |
| --- | --- |
| `vista` / `vista start` | 启动服务或打开现有实例 |
| `vista status` | 查看服务、会话、Worker 和进程状态 |
| `vista stop` | 停止服务及其受管工作，保留历史 |
| `vista restart` | 停止并重新启动服务，保留历史 |

关闭浏览器不会停止工作。关闭会话会停止其 Worker 和受管后台工作，保留历史；删除会话还会移除所属数据。重启 Vista 后，未完成操作记录为中断，不自动重放其副作用。

要固定使用其他端口，在 `<configuration root>/server.json` 设置 `{"port":6186}`。`VISTA_PORT` 覆盖该文件，启动或重启的 `--port` 再覆盖两者。端口范围为 1～65535，Vista 不会自动换端口；配置在下次启动时生效。

## 从源码构建

构建需要 Go 1.26+、支持直接运行 TypeScript 的 Node.js、npm 和 Make。在仓库根目录执行：

```sh
make build
./bin/vista start
```

`make build` 会构建并内嵌前端。开发版本号默认为 `dev`；发布时使用 `make build VERSION=v0.1.0`，构建前自动刷新内置的[客户端 UA 版本](./docs/providers.zh-CN.md#http-客户端身份)。「关于 Vista」展示版本、可执行文件 SHA-256、源码提交和宿主操作系统，构建不注入时间戳。

执行 `make web` 后，可交叉编译：

```sh
GOWORK=off CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/vista-windows-amd64.exe ./cmd/vista
GOWORK=off CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/vista-darwin-arm64 ./cmd/vista
GOWORK=off CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/vista-darwin-amd64 ./cmd/vista
```

Shell 工具在 Windows 使用 Windows PowerShell，在 Linux/macOS 使用 `/bin/sh`。运行 Vista 无需另外安装 shell 或数据库。

## 配置与状态目录

所有平台通过 Go 的 `os.UserConfigDir()` 确定 Vista 配置根目录，运行数据位于其 `state/` 子目录。

| 平台 | 配置根目录 |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/vista`，未设置时为 `~/.config/vista` |
| Windows | `%APPDATA%/vista` |
| macOS | `~/Library/Application Support/vista` |

配置根目录保存模型和 MCP 连接、用户可编辑的全局 `AGENTS.md`、`skills/` 以及账号凭据。SQLite、实例记录、锁、日志、输出产物和共享资源原件位于 `state/`。

设置中可管理服务商连接、网络查询服务、MCP、全局提示词、记忆、技能和资源占用。将包含 `SKILL.md` 的技能文件夹放到页面显示的技能目录并刷新，即可识别。项目局部规则可写入对应文件夹中的 `AGENTS.md`，适用于该文件夹及其子文件夹。

## 会话与资源

会话没有固定工作目录，请在任务中提供相关路径。不同会话共享真实文件系统：编辑或撤回消息仅回退会话上下文，文件改动及其他工具副作用保留。

附件按内容寻址存为共享原件，由会话持有引用，SQLite 不存二进制内容。模型支持图片时，新图片进入下一次请求；后续请求只保留引用，直到模型主动重新读取原件。关闭会话保留资源；最后一个持有原件的会话删除后，可在设置中手动清理。

自动上下文压缩仅由上游明确的上下文超限证据触发，完成后重试失败的模型请求，不重放已完成工具。空闲时可手动压缩，完成后不自动发消息或续跑；模型目录中的窗口限制不触发压缩。

## 开发与验证

开发约束和提交规范见 [AGENTS.md](./AGENTS.md)。直接执行 Go 命令时设置 `GOWORK=off`。在仓库根目录选择检查目标：

```sh
make test go
make test frontend
make test e2e
make test integration
make test mcp
make test memory
make test capacity
make test all
```

| 目标 | 检查范围 |
| --- | --- |
| `go` | Go 测试、vet 和竞态检测 |
| `frontend` | 前端单测、类型检查和生产资源构建 |
| `e2e` | 桌面、手机竖屏和横屏的 Playwright 检查 |
| `integration` | CLI/Server/Worker 工作流、工具续轮、压缩、持久化、文件写入冲突、SQLite 故障和进程崩溃 |
| `mcp` | stdio/HTTP SDK fixture，以及锁定版本的上游 Filesystem/Everything 服务 |
| `memory` | 20 个打开会话，以及同一 Worker 追加 200 轮的内存回归 |
| `capacity` | shell/MCP/子智能体混合负载、Chat/Responses 长输出、慢 SSE 和八种适配器的图片负载 |
| `all` | 顺序执行全部七项目标，首次失败即停止 |

`make test` 默认执行 `go`，可用 `make test frontend memory` 组合目标；共享前置步骤只执行一次。检查按需安装锁定依赖。浏览器测试从 `PATH` 查找 Chrome/Chromium，也可设置 `VISTA_CHROME`；集成检查需要 `sqlite3`，缺少依赖时明确失败。

测试在 `.vista/test-state/bin` 构建隔离产物，并使用独立配置、端口和浏览器 profile，不替换 `bin/vista` 或重启用户实例。依赖和测试环境见[验证](./docs/testing.zh-CN.md)。

## 文档

- [产品范围与受众](./docs/product.zh-CN.md)
- [架构与运行时约定](./docs/architecture.zh-CN.md)
- [服务商路由与模型资料](./docs/providers.zh-CN.md)
- [Worker 与后台进程生命周期](./docs/process-lifecycle.zh-CN.md)
- [测试目标与容量测量](./docs/testing.zh-CN.md)
- [评测：运行方法与结果](./docs/benchmarks.zh-CN.md)
- [前端、国际化与浏览器检查](./web/README.zh-CN.md)
- [上下文压缩](./docs/context-compaction.zh-CN.md)、[资源](./docs/resources.zh-CN.md)及[会话回退](./docs/message-revert.zh-CN.md)

## 许可证

Vista 的原创代码和文档采用 [MIT 许可证](./LICENSE)。第三方组件保留各自的许可证和版权声明；完整的[许可及署名声明](./licenses/NOTICE)随发布压缩包提供，也可通过运行中服务的 `/licenses` 地址查看。
