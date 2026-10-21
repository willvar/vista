# 架构与运行时约定

[English](./architecture.md)

本文描述当前源码。产品取舍见[产品行为](./product.zh-CN.md)，开发约束见 [AGENTS.md](../AGENTS.md)。

## 模块

| 目录 | 职责 |
| --- | --- |
| `cmd/vista/`、`internal/instance/` | 生命周期命令、后台服务、实例识别与关闭 |
| `internal/server/` | HTTP/SSE、配置、准入、Worker 监督及持久化协调 |
| `internal/worker/`、`internal/workerproto/` | 会话进程、管道协议和持久化确认 |
| `internal/agent/` | 模型循环、工具调度、子任务收集及压缩 |
| `internal/provider/` | 元数据、协议请求、流解析、认证及超限识别 |
| `internal/modelcatalog/` | 连接、发现、选模及运行配置 |
| `internal/store/` | SQLite 事务、消息、事件、任务及输出引用 |
| `internal/resources/` | SHA-256 原件文件及跨进程发布／读取／清理锁 |
| `internal/tools/`、`internal/mcpclient/` | 文件、进程、网络、技能和 MCP 操作 |
| `internal/agentconfig/` | 全局提示词和委派设置 |
| `internal/appdirs/`、`internal/platform/` | 平台目录、进程归属、浏览器启动及指标 |
| `internal/buildinfo/` | 版本、二进制哈希及 Go VCS 信息 |
| `web/src/` | Vue 界面、会话状态、HTTP/SSE 客户端及虚拟滚动 |

前端构建到 `internal/server/assets/`，由 Go 内嵌。Server、Worker 和 CLI 共用一个可执行文件，服务商协议在 Go 内实现，无独立 JavaScript 服务商进程。

## 进程与存储归属

打开的会话通常拥有常驻 Worker，受准入与故障恢复约束。主线程顺序执行，工具、子任务及不同会话可并行。会话没有固定工作目录，文件和 shell 工具接收明确路径。

Server 负责 SQLite 写入和会话内事件序号。Worker 通过本地管道发送结果，工具启动、子任务启动／终态及检查点等边界等待持久化确认。浏览器接收已持久化事件，按序号去重 SSE 重放。模型正文分块保存，工具和进程产物按需读取。见[进程生命周期](./process-lifecycle.zh-CN.md)。

[目录解析](../internal/appdirs/dirs.go)使用 `os.UserConfigDir()/vista`。`models.json`、`mcp.json`、`search.json`、`user-agent.json`、全局 `AGENTS.md`、`skills/`、`omp-models.json`、`model-catalog-status.json` 等配置与凭据位于该根目录。SQLite、实例记录、锁、日志、输出及资源原件位于 `state/`。模型目录文件不在 `state/` 内。

`GET /api/status` 提供版本、缓存的可执行文件 SHA-256、Go 源码提交及 dirty 状态、服务所在系统及版本。`internal/buildinfo.Version` 默认为 `dev`，Makefile 可指定发布版本。不注入构建时间，二进制哈希在启动时计算，不回写可执行文件。

## 执行与子任务

执行中的普通发送在下一决策边界插话，明确排队的消息等待执行结束。取消／失败暂停未投递输入；停止后新发消息独立开始，不释放旧暂停队列。选模变化从后续执行生效。

[委派注册表](../internal/agent/delegation.go)通过稳定任务 ID 关联父任务及原执行：

| 策略 | 最终提交行为 |
| --- | --- |
| `required` | 等待终态，收集持久化报告；仍有未收集报告时让模型综合。 |
| `best_effort` | 不阻塞提交；父任务收尾时取消未完成工作。 |
| `detached` | 归会话所有，可超过父执行及其取消的生命周期；报告留在原过程，不新增正式答复。 |

明确取消子任务也会取消其后代。同一父智能体的后续执行可查询或取消其 detached 任务。关闭会话或停止 Vista 会结束受管工作。

模型提出不含工具的候选答案后，运行循环在结束前检查尚未收集的 required 子任务。收集结果作为结构化运行时证据（`RuntimeData`），共享 16 KiB 内联预览，保留补读引用；语义综合仍由模型完成。同一执行可在插话边界保留多条正式答复，不是整个生命周期只允许一条消息。

共享的 `runScope.modelTurns` 用于分配轮次编号，不是模型调用预算。运行时校验委派深度，但没有统一的整轮 deadline、累计调用预算或有上限的 join 代次计数。

检查清单是按执行归属的替换事件，不是会话全局状态；新执行不继承旧清单。工具按自身参数规则校验，参数错误作为该次工具结果返回。有效 `question` 仅主智能体可调用，且必须是该模型轮次唯一工具；问题保留到回答或被替代。服务商已执行的托管工具只保存，不再作为本地工具调度。

## 上下文接续

主模型消息及工具结果存入不可变历史文件，通过确认后的 `model_history` 引用提交。后续执行按事件顺序与正式输入合并，排除重复的正式 assistant 投影。中断后缺少的工具结果恢复为明确的未知状态，不执行工具。

自动压缩要求上游明确的上下文超限证据；手动压缩在空闲时共用摘要／检查点存储。模型目录窗口和保留词元估算不触发压缩。检查点通过事件与输入截止位置替代旧历史，保留工具配对、资源引用和协议回放字段；摘要失败不替换当前检查点。见[上下文压缩](./context-compaction.zh-CN.md)。

全局提示词、偏好、当前清单、语言及可用工具信息按执行重新组装，项目规则和技能正文按需读取。切换模型／协议时去除不兼容的服务商回放数据，保留可移植正文、工具配对及资源引用。

## 服务商与工具

OMP 和 models.dev 提供模型资料，Vista 负责路由与认证，自定义连接明确指定 API 格式。两个目录来源整体暂存、安装，失败保留旧目录。已有执行使用自己的配置快照。见[服务商路由](./providers.zh-CN.md)。

`websearch` 按保存顺序尝试外部服务；`webfetch` 使用独立的 Vista 内置／Jina／DeepSeek 顺序。首次搜索顺序只随机一次，网页读取默认内置优先；DeepSeek 搜索和读取密钥分别配置。首次输出默认每条搜索结果 1,000 个 Unicode 字符、每页 8,000 个，0 取消首次截短。网络结果没有第二层固定工具结果截断，显式补读仍按字节分页。配置保存在 `search.json`，从下一次执行生效。

文件搜索在进入被忽略目录前应用祖先和嵌套 `.gitignore` 及例外规则；明确读取不受此限制。`read_file` 使用从 1 开始的 `startLine`、`lineCount`（默认 200），返回原行号及 `nextLine`，哈希基于原文件。过大的单行使用产物保存；`read_tool_output` 仍按字节读取。

MCP 设置保存为 `mcp.json`，稳定 ID 与显示名称分离。每次执行独立拥有 MCP 连接，本地服务不是全局永久进程。设置中的连接检查使用独立临时握手；OAuth 仅在授权、握手成功且连接配置未变时发布凭据。界面和状态入口见[前端](../web/README.zh-CN.md)。

## 资源、回退与限制

原始附件位于 `state/resources/<哈希前缀>/<SHA-256>`，SQLite 只存元数据和归属，不存图片字节。新附件及显式 `read_resource` 结果只激活下一次请求的视觉输入，规范历史和检查点保留引用。见[资源约定](./resources.zh-CN.md)。

撤回或编辑提交先停止该会话工作，再从所选用户消息处截断历史，不撤销文件或其他工具副作用，也不复用事件序号。见[消息回退](./message-revert.zh-CN.md)。

[准入实现](../internal/server/admission.go)以 64 个常驻 Worker、20 个运行会话作为准入限制，并使用软内存预留；运行会话计数包含后台活动。Worker heap/RSS 监控和 Go 软限制不是操作系统强制的进程内存上限，外部 shell/MCP 占用另行观察。组件缓冲限制和单次操作超时不等于通用任务容量保证，见[验证](./testing.zh-CN.md)。

服务监听回环地址，校验 Host/Origin，工具以本机用户权限执行，无逐次调用审批。HTTP／流错误保留脱敏后的服务商诊断，在消息区展示；实际步骤错误保留在步骤内。平台层实现 Linux、Windows 和 macOS 进程管理。
