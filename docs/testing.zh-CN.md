# 验证入口

[English](./testing.md)

测试按检查内容在 [Makefile](../Makefile) 中分组，下文说明入口、依赖和运行环境。

## Make 目标

在仓库根目录运行，需要 Go 1.26+、支持直接运行 TypeScript 的 Node.js、npm 和 Make。竞态检查另需受支持的 C 编译工具链；直接执行 Go 命令时设置 `GOWORK=off`。

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

| 目标 | 当前检查 |
| --- | --- |
| `go` | Go 单测、`go vet`、`go test -race` |
| `frontend` | Node 单测、UA 版本同步脚本检查、Vue／TypeScript 检查、国际化校验及内嵌资源构建 |
| `e2e` | Playwright 桌面、手机竖屏、手机横屏项目 |
| `integration` | CLI／Server／Worker 原生应用测试，以及持久化、工具、队列、压缩、冲突和故障 smoke 脚本 |
| `mcp` | SDK stdio／HTTP fixture 及锁定版本的上游 Filesystem／Everything 服务 |
| `memory` | 20 个打开会话，同一常驻 Worker 追加 200 轮及内存回归检查 |
| `capacity` | stdio／HTTP MCP、shell 和子任务混合负载，Chat／Responses 长输出、慢 SSE 及八种图片适配器 |
| `all` | 顺序执行全部七项目标，首次失败即停止 |

`make test` 默认执行 `go`，可用 `make test frontend memory` 组合目标，共享前置步骤只执行一次。按需安装锁定 npm 依赖；浏览器检查从 `PATH` 查找 Chrome／Chromium，也可设置 `VISTA_CHROME`。集成脚本需要 `sqlite3`，缺少依赖时明确失败；安装上游 MCP 依赖可能需要网络。

Make 在 `.vista/test-state/bin` 构建隔离测试产物，使用独立配置和端口，不替换 `bin/vista` 或重启用户实例。浏览器状态、trace、截图和 HTML 报告位于 `web/.vista/test-state/`，见[前端检查](../web/README.zh-CN.md)。

## 目标平台应用检查

为目标操作系统构建应用，并在该平台运行应用测试：

```sh
VISTA_TEST_BINARY=/absolute/path/to/vista GOWORK=off go test ./cmd/vista -run '^TestNative(Application|ResetPort|WindowsDesktopLauncher|WindowsTray.*)$' -count=1
```

Windows 在 PowerShell 设置对应环境变量，路径使用 `.exe`。也可运行交叉编译的 `go test -c` 测试程序，无需在目标机器安装 Go。测试使用临时配置和模拟模型，覆盖实际应用 Worker、shell／后台工作、取消、启停、重置及崩溃恢复。Windows 桌面启动测试使用独立的 Explorer 同名父进程及浏览器替身，验证启动、再次打开和原生启动错误对话框，不打开用户的浏览器。托盘检查覆盖点击通知、右键菜单、原生状态解析与长内容、图标恢复、重启和停止；图标注册检查需要运行资源管理器的交互式 Windows 会话，无桌面的运行会跳过该项。平台专用测试另行覆盖进程归属和清理。

交叉编译只证明可编译，模拟端点只证明请求／流行为，不代表真实账号授权或服务商兼容性。

## 容量工具

| 脚本 | 用途 |
| --- | --- |
| [capacity_smoke.mjs](../scripts/capacity_smoke.mjs) | 打开会话与重复轮次的内存回归 |
| [mixed_capacity_smoke.mjs](../scripts/mixed_capacity_smoke.mjs) | shell／MCP／子任务混合，stdio 或 HTTP |
| [slow_sse_capacity_smoke.mjs](../scripts/slow_sse_capacity_smoke.mjs) | 慢事件读取和长回复 |
| [long_output_smoke.mjs](../scripts/long_output_smoke.mjs) | 模型长正文的持久化与读取 |
| [image_capacity_smoke.mjs](../scripts/image_capacity_smoke.mjs) | 并发和重复图片输入，检查历史图片不被自动重发 |
| [evaluate_mixed_capacity.py](../scripts/evaluate_mixed_capacity.py) | 按明确 cgroup、负载和内存增长条件评估混合负载报告 |

直接运行脚本需要绝对路径的 `VISTA_BIN` 和隔离的 `VISTA_TEST_TMPDIR`；混合／MCP 负载还需要 `VISTA_MCP_FIXTURE`，具体参数见脚本。评估器要求报告对应其指定的 8 GiB cgroup 和负载，不是通用报告校验器。普通 `make test memory`／`capacity` 不创建受限 cgroup，也不证明长期容量保证。

记录二进制 SHA-256／源码提交、平台工具链、参数、失败、Worker 身份、空闲 heap／RSS 及 cgroup 峰值／OOM。比较预热后多个空闲检查点；软预留与 Go 软限制不是系统强制内存上限。浏览器内存单独测量，缓存边界测试本身不能证明浏览器长稳表现。

## 真实模型评测

使用 `make benchmark tb` 运行三个固定的 Terminal-Bench 样本，使用 `make benchmark gaia` 运行 GAIA 验证集任务。单独运行 `make benchmark` 列出可选套件。两者均使用 Harbor 和真实的 Vista 会话 API，独立于 `make test all`，可能产生模型及搜索服务费用。

环境准备、模型凭据、运行命令和结果解读见[评测文档](./benchmarks.zh-CN.md)。
已记录的成绩与分析分别维护在 [Terminal-Bench](./benchmarks/tb.zh-CN.md) 和 [GAIA](./benchmarks/gaia.zh-CN.md) 文档中。
维护的适配器位于 [`benchmarks/harbor/`](../benchmarks/harbor/) 和 [`benchmarks/gaia/`](../benchmarks/gaia/)。

## 生成的服务商基准

[服务商矩阵基准](../scripts/provider_matrix/testdata/catalog.csv)是确定性的目录／路由数据，不是真实服务商报告。生成方式及证据边界见[服务商](./providers.zh-CN.md#协议与验证边界)。
