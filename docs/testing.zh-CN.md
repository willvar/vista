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

## Harbor 任务评测

[`benchmarks/harbor/`](../benchmarks/harbor/) 包含 Vista 适配器、容器内 HTTP 执行器、固定任务清单、锁定的 Python 依赖和结果汇总脚本。它通过真实的会话 API 和工具运行未修改的 Vista 二进制，由 Harbor 提供任务容器、原始指令、限制和判题器。这是按需执行的真实模型评测，独立于 `make test all`。

依赖：Python 3.12+、Git、Docker 及 Compose，以及与任务镜像兼容的 Linux Vista 二进制（固定样本镜像面向 amd64）。从仓库根目录准备：

```sh
python3 -m venv .vista/benchmarks/harbor/venv
.vista/benchmarks/harbor/venv/bin/python -m pip install -r benchmarks/harbor/requirements.lock
git clone https://github.com/laude-institute/terminal-bench-2-0-sample.git .vista/benchmarks/harbor/dataset
git -C .vista/benchmarks/harbor/dataset checkout 7e917f35c281188532772312d4ad91ca9274febc
mkdir -p .vista/benchmarks/harbor/profile
cp benchmarks/harbor/models.example.json .vista/benchmarks/harbor/profile/models.json
docker pull ghcr.io/laude-institute/terminal-bench/build-cython-ext:2.0
docker pull ghcr.io/laude-institute/terminal-bench/fix-code-vulnerability:2.0
docker pull ghcr.io/laude-institute/terminal-bench/log-summary-date-ranges:2.0
```

在隔离的 `models.json` 中填写所需连接和模型，密钥留在这个被忽略的文件里。Codex OAuth 连接需要包含 `connectionId`，并将对应的 `codex-auth/<connectionId>.json` 复制到隔离配置目录；过期快照可能在执行时刷新，同一账号由其他实例使用时应单独取得凭据。适配器仅传输选中的模型、对应的 Codex 凭据及可选的 `omp-models.json` 目录文件，不复制全局提示词、记忆、技能或其他模型连接，也不修改来源配置。

```sh
make benchmark BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/harbor/profile --model YOUR_MODEL_ID --dry-run'
make benchmark BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/harbor/profile --model YOUR_MODEL_ID'
```

单独运行 `make benchmark` 显示帮助，`HARBOR_PYTHON` 可指定其他已准备好的环境。不传 `--reasoning-effort` 时沿用配置默认值，也可明确指定模型支持的档位。重复传入 `--task` 可选择任务。`--dry-run` 检查二进制、选中配置和固定任务文件，输出执行顺序，不启动 Harbor、不检查 Docker，也不调用模型。真实运行还会核验 Harbor 版本和本地任务镜像摘要；修改数据集提交、任务文件或镜像摘要后，需要明确更新清单。

重复传入 `--binary before=/path/to/baseline --binary after=bin/vista` 并设置 `--rounds 3`，可交错对比多个二进制。每个标签每轮对每道选中任务执行一次，按轮次和任务轮换先后顺序，并发为 1，Harbor 重试为 0，沿用任务原始限制和判题器。各二进制使用相同的选中模型和来源配置；二进制自带的目录或默认值差异仍属于被对比的实现。适配器在原始任务指令前加上工作目录；组合内容超过 Vista 输入限制时，改用完整指令文件引用，不代替智能体回答提问。

产物默认位于 `.vista/benchmarks/harbor/runs/<时间戳>/`，`--output` 也必须指向 `.vista/` 内尚不存在的目录。`plan.json` 记录顺序、模型、档位、二进制／配置／适配器哈希、数据集提交和镜像信息。每次试验保留原始 Harbor 结果、判题输出、Vista 事件、消息、构建信息和产物。输入发生变化时中止后续试验，不覆盖旧记录，也不自动清理镜像。

`summary.json` 将失败、未判题、缺失结果和基础设施错误都保留在计划次数中。自主完成与原始判题奖励分别记录；Vista 正常结束不代表任务通过。耗时汇总仅纳入自主完成且判题通过的试验，并显示样本数。请求用量不完整时保留为未知，未知费用也不补零。重新汇总本地产物：

```sh
.vista/benchmarks/harbor/venv/bin/python benchmarks/harbor/summarize.py .vista/benchmarks/harbor/runs/RUN_DIRECTORY
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/harbor/tests -v
```

适配器测试使用固定样本和模拟模型，不衡量真实任务成功率。三道固定任务覆盖数据处理、代码修复和依赖／构建工作，属于探索性回归样本，不能代表整体任务成功率。数据集正文及真实运行记录不纳入维护的基准源码。

## 生成的服务商基准

[服务商矩阵基准](../scripts/provider_matrix/testdata/catalog.csv)是确定性的目录／路由数据，不是真实服务商报告。生成方式及证据边界见[服务商](./providers.zh-CN.md#协议与验证边界)。
