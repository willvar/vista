# Vista

[English](./README.md)

![Vista 中文桌面界面与荣耀 WIN、一加 15、iPhone Pro Max 的游戏手机对比](./docs/assets/vista-hero.zh-CN.png)

Vista 是适用于 Linux、Windows 和 macOS 的通用型智能体。在浏览器界面描述目标，智能体可使用文件、shell 命令、网络查询、MCP 工具及技能完成工作。每个已打开的会话拥有独立的 Worker 进程，会话和历史保存在 Vista 的运行环境中。

## 开始使用

从 [GitHub Releases](https://github.com/willvar/vista/releases/latest) 选择与系统和架构匹配的一种包。
**`vista-server`** 提供独立服务和浏览器工作台；**`vista-desktop`** 提供原生窗口并自带同一服务。桌面包不需要另装 Server。

运行 `vista-server` 会在后台启动服务并打开默认浏览器，再次打开会复用已运行的实例。浏览器默认打开 `http://127.0.0.1:5186/`，服务则监听所有 IPv4 网卡（`0.0.0.0`），其他设备使用 `http://<主机IP>:5186/` 访问。可选环境变量 `VISTA_HOST` 可设为某个 IPv4 地址以收窄监听范围；HTTPS 由反向代理提供，Vista 自身不处理。

使用[桌面端](./docs/desktop.zh-CN.md)时，直接打开 `vista-desktop`（Windows 上是 `vista-desktop.exe`，macOS 上打开 `Vista.app`）。托盘可用时关窗只隐藏，服务继续运行；托盘可以唤回窗口、查看状态、重启和停止服务。

发送第一条消息前，先在设置中连接模型服务商。内置连接封装认证和协议配置；自定义连接可独立选择对话 API 格式与认证方式。可用模型由所连接的账号或服务决定。

| 命令 | 作用 |
| --- | --- |
| `vista-server` / `vista-server start` | 启动服务或打开现有实例 |
| `vista-server status` | 查看服务、会话、Worker 和进程状态 |
| `vista-server stop` | 停止服务及其受管工作，保留历史 |
| `vista-server restart` | 停止并重新启动服务，保留历史 |

关闭浏览器不会停止工作。关闭会话会停止其 Worker 和受管后台工作，保留历史；删除会话还会移除所属数据。重启 Vista 后，未完成操作记录为中断，不自动重放其副作用。

「关于 Vista」提供服务重启和端口设置。重启保留会话和当前端口，随后根据已保存的上下文继续未完成的智能体任务；托管后台进程会停止。详情见[服务控制](./docs/product.zh-CN.md#关于-vista-与服务控制)。

## 从源码构建

构建需要 Go 1.26+、支持直接运行 TypeScript 的 Node.js、npm 和 Make。在仓库根目录执行：

```sh
make build
./bin/vista-server start
```

`make build` 会构建并内嵌前端，版本号默认为 `dev`；发布版本使用 `make build VERSION=v0.1.0`。开发约束见 [AGENTS.md](./AGENTS.md)，构建检查与测试命令见[验证](./docs/testing.zh-CN.md)。

如需在桌面窗口中使用同一套界面，参见 [Wails 桌面构建](./docs/desktop.zh-CN.md)。

## 配置与状态目录

设置中可管理服务连接、模型联网、MCP、全局提示词、技能和资源占用。

配置与会话保存在以下目录，运行数据位于其中的 `state/` 子目录：

| 平台 | 配置根目录 |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/vista`，未设置时为 `~/.config/vista` |
| Windows | `%APPDATA%/vista` |
| macOS | `~/Library/Application Support/vista` |

目录中包含设置、明文凭据、全局 `AGENTS.md` 提示词及技能，请妥善保管。文件格式与升级行为见[配置编辑](./docs/product.zh-CN.md#配置编辑)，技术约定见[配置存储](./docs/architecture.zh-CN.md#统一配置存储)。

## 会话与资源

会话没有固定工作目录，请在任务中提供相关路径。不同会话共享真实文件系统：编辑或撤回消息仅回退会话上下文，文件改动及其他工具副作用保留。

关闭会话会保留附件。删除最后一个使用某附件的会话后，可在设置中手动清理其原件。见[资源](./docs/resources.zh-CN.md)。

模型报告上下文超限后，Vista 可自动压缩长会话；空闲时也可手动压缩。见[上下文压缩](./docs/context-compaction.zh-CN.md)。

## 文档

- [产品范围与受众](./docs/product.zh-CN.md)
- [架构与运行时约定](./docs/architecture.zh-CN.md)
- [服务商路由与模型资料](./docs/providers.zh-CN.md)
- 高级配置：[网络与代理](./docs/network.zh-CN.md)、[提示词缓存](./docs/prompt-caching.zh-CN.md)
- [Worker 与后台进程生命周期](./docs/process-lifecycle.zh-CN.md)
- [测试目标与容量测量](./docs/testing.zh-CN.md)
- [评测：运行方法与结果](./docs/benchmarks.zh-CN.md)
- [前端、国际化与浏览器检查](./web/README.zh-CN.md)
- [上下文压缩](./docs/context-compaction.zh-CN.md)、[资源](./docs/resources.zh-CN.md)及[会话回退](./docs/message-revert.zh-CN.md)

## 许可证

Vista 的原创代码和文档采用 [MIT 许可证](./LICENSE)。第三方组件保留各自的许可证和版权声明；完整的[许可及署名声明](./licenses/NOTICE)随发布压缩包提供，也可通过运行中服务的 `/licenses` 地址查看。
