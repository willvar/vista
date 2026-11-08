# 验证入口

[English](./testing.md)

测试按检查内容在 [Makefile](../Makefile) 中分组，下文说明入口、依赖和运行环境。

## Make 目标

在仓库根目录运行，需要 Go 1.26+、支持直接运行 TypeScript 的 Node.js、npm 和 Make。竞态检查另需受支持的 C 编译工具链；直接执行 Go 命令时设置 `GOWORK=off`。

```sh
make test
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
| `test`（不带其他目标） | Go／前端检查、固定浏览器核心流程，以及受影响模块和消费者 |
| `go` | Go 单测、`go vet`、`go test -race` |
| `frontend` | Node 单测、UA 版本同步脚本检查、Vue／TypeScript 检查、国际化校验及内嵌资源构建 |
| `e2e` | 桌面执行全部 Playwright 场景；手机竖屏／横屏覆盖响应式、触控和滚动 |
| `integration` | CLI／Server／Worker 原生应用测试，以及持久化、工具、队列、压缩、冲突和故障 smoke 脚本 |
| `mcp` | SDK stdio／HTTP fixture 及锁定版本的上游 Filesystem／Everything 服务 |
| `memory` | 20 个打开会话，同一常驻 Worker 追加 200 轮及内存回归检查 |
| `capacity` | stdio／HTTP MCP、shell 和子任务混合负载，Chat／Responses 长输出、慢 SSE 及八种图片适配器 |
| `all` | 顺序执行全部七项目标，首次失败即停止 |

指定目标时只执行对应检查，例如 `make test frontend memory`；共享前置步骤只执行一次。按需安装锁定 npm 依赖；浏览器检查从 `PATH` 查找 Chrome／Chromium，也可设置 `VISTA_CHROME`。集成脚本需要 `sqlite3`，缺少依赖时明确失败；安装上游 MCP 依赖可能需要网络。

Make 在 `.vista/test-state/bin` 构建隔离测试产物，使用独立配置和端口，不替换 `bin/vista` 或重启用户实例。浏览器状态、trace、截图和 HTML 报告位于 `web/.vista/test-state/`，见[前端检查](../web/README.zh-CN.md)。

可用 `E2E_ARGS` 向 Playwright 传递选项，保留默认项目，例如 `make test e2e E2E_ARGS='--max-failures=1 --workers=2'`。

`make test` 默认比较暂存、未暂存和未跟踪源码与 `HEAD` 的差异。
用 `TEST_BASE=<commit>` 纳入上次验证之后已经提交的改动。
[`e2e-coverage.ts`](../web/scripts/e2e-coverage.ts) 中的选择器为已知模块追加浏览器消费者，始终保留 `@smoke` 核心流程。
公共组件、全局样式、fixture、构建／配置改动及未归类源码会选择完整浏览器覆盖。
前端生成产物跟随对应源码的选择范围；只有产物变化时选择完整覆盖。
仅文档或单元测试改动保留核心流程，不追加无关浏览器场景。
选择范围、结果、源码标识、二进制 SHA-256 和浏览器耗时记录在 `.vista/acceptance/local-checks/` 下。
自定义文件／grep 筛选使用 `make test e2e`；本地选择器拒绝可能静默移除核心流程或消费者的过滤参数。

桌面保持完整浏览器基线。
手机项目使用明确的响应式覆盖清单，包括多语言布局、触控、拖动、滚动、虚拟历史和弹框几何；与视口无关的持久化及协议恢复只在桌面执行一次。
本地和 CI 使用同一套分类。
语言识别、偏好保存和请求语言注入保留桌面流程；另有按语言参数化的布局检查覆盖三视口及两种主题。
纯状态／协议检查同时通过 Node 和 Go 测试验证，不仅依靠浏览器重复执行。

## GitHub 构建与发布

[`validate.yml`](../.github/workflows/validate.yml) 接受手动 `workflow_dispatch` 运行及 `v*` tag 推送。
可在 Actions → Build and release 中只运行检查而不发布，或执行 `gh workflow run validate.yml -f browser=true`。
手动运行分支时构建 `dev` 压缩包；运行 tag 时使用其 `vMAJOR.MINOR.PATCH` 版本。

Go 单测／vet／竞态及前端检查在 Ubuntu 上运行。
原生矩阵分别在 Linux、Windows 和 macOS 的 x86-64 与 ARM64 环境中构建并打包已提交源码，覆盖六种系统／架构组合。
[`ci_native.py`](../scripts/ci_native.py) 拒绝交叉编译目标，校验源码／平台构建信息、压缩包内容及校验和，再用解压后的可执行文件运行现有应用、平台和 MCP host 测试。
报告列出工具链、源码提交、二进制／压缩包哈希及跳过项，必要的应用及进程检查必须通过。
脚本使用已提交的前端资源、UA 版本及许可声明，不改写这些快照。
也可在本地使用 Python 3.11+ 运行，例如 `python3 scripts/ci_native.py --target linux/amd64 --version dev`；本地报告会明确记录未提交源码状态。
Windows 通过 [`ci_windows.ps1`](../scripts/ci_windows.ps1) 经由 Explorer 或本地 WMI 桌面进程运行检查，避免 runner 外层的进程 Job 阻止应用脱离启动进程。
Windows ARM64 托管 runner 会在桌面检查前关闭首次登录隐私设置向导。
runner 缺少 Explorer 桌面入口时，该进程会启动 Explorer，并在任务栏就绪后开始验证；两种架构均要求桌面启动、托盘和原生状态窗口测试通过。
原生脚本在编译前检查进程脱离权限，不改写应用的启动标志。

Linux 浏览器矩阵安装与锁定 Playwright 包匹配的 Chromium，三个视口项目分别使用独立 runner，各运行两个工作进程，并应用上述覆盖分类。
手动运行时可关闭浏览器测试；推送发布 tag 时始终执行。
原生报告、压缩包和浏览器结果摘要保留三天，浏览器 trace／截图仅在失败时上传。
runner 结果只证明实际运行的检查，模拟模型端点不证明真实服务商兼容性。

推送带注释的 `vMAJOR.MINOR.PATCH` tag 会启动自动发布。
tag 说明作为 Release 正文，使用签名 tag 并保持英文发布说明简洁。
全部检查通过后，[`ci_release.py`](../scripts/ci_release.py) 核对各原生报告的源码、版本、结果和压缩包校验和，再汇总实际测试的压缩包及一份 `SHA256SUMS`。
只有发布任务拥有仓库写权限；压缩包上传完成后创建正式 GitHub Release，不重新构建，也不替换已发布版本。

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
| [image_guard_smoke.mjs](../scripts/image_guard_smoke.mjs) | 50 MiB 原件、Worker 内存中断、原件保留与无重放的文本恢复 |
| [evaluate_mixed_capacity.py](../scripts/evaluate_mixed_capacity.py) | 按明确 cgroup、负载和内存增长条件评估混合负载报告 |

直接运行脚本需要绝对路径的 `VISTA_BIN` 和隔离的 `VISTA_TEST_TMPDIR`；混合／MCP 负载还需要 `VISTA_MCP_FIXTURE`，具体参数见脚本。20 会话负载脚本在隔离配置中显式设置 8 GiB 准入预算和 20 个同时执行，不依赖产品默认值。评估器要求报告对应其指定的 8 GiB cgroup 和负载，不是通用报告校验器。普通 `make test memory`／`capacity` 不创建受限 cgroup，也不证明长期容量保证。

记录二进制 SHA-256／源码提交、平台工具链、参数、失败、Worker 身份、空闲 heap／RSS 及 cgroup 峰值／OOM。比较预热后多个空闲检查点；实测内存准入与 Go 软限制不是系统强制内存上限。浏览器内存单独测量，缓存边界测试本身不能证明浏览器长稳表现。

## 真实模型评测

使用 `make benchmark tb` 运行三个固定的 Terminal-Bench 样本，使用 `make benchmark gaia` 运行 GAIA 验证集任务。单独运行 `make benchmark` 列出可选套件。两者均使用 Harbor 和真实的 Vista 会话 API，独立于 `make test all`，可能产生模型及搜索服务费用。

环境准备、模型凭据、运行命令和结果解读见[评测文档](./benchmarks.zh-CN.md)。
已记录的成绩与分析分别维护在 [Terminal-Bench](./benchmarks/tb.zh-CN.md) 和 [GAIA](./benchmarks/gaia.zh-CN.md) 文档中。
维护的适配器位于 [`benchmarks/harbor/`](../benchmarks/harbor/) 和 [`benchmarks/gaia/`](../benchmarks/gaia/)。

## 生成的服务商基准

[服务商矩阵基准](../scripts/provider_matrix/testdata/catalog.csv)是确定性的目录／路由数据，不是真实服务商报告。生成方式及证据边界见[服务商](./providers.zh-CN.md#协议与验证边界)。

通过前台 `shell` 工具运行测试时，将测试程序的诊断超时设为小于 shell 超时。
例如，用 `timeoutSeconds: 180` 执行 `GOWORK=off go test -timeout=120s ./internal/agent`，让 Go 在 shell 清理进程前打印超时堆栈。
测试套件可能超过前台超时时，使用受管后台工作并读取保存的输出，不增加或移除 shell 的最长时限。
