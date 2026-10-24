# 评测

[English](./benchmarks.md)

评测让 Vista 执行有独立判定标准的任务，帮助开发者发现工具使用、任务完成方面的退化，也帮助用户了解经过测试的能力与边界。
成绩反映的是特定二进制、模型、服务和设置的组合，不代表每个实际任务都能成功。

| 入口 | 任务 | 用途 | 结果 |
| --- | --- | --- | --- |
| `make benchmark tb` | 三个固定的 Terminal-Bench 2.0 样本任务 | 比较 Vista 版本在数据处理、代码修复和依赖／构建任务上的表现 | [历史成绩与分析](./benchmarks/tb.zh-CN.md) |
| `make benchmark gaia` | GAIA 验证集：三个难度级别，共 165 题，其中 38 题带附件 | 检查资料检索、推理、文件处理和工具使用，也可选择子集 | [历史成绩与分析](./benchmarks/gaia.zh-CN.md) |

**两套评测都使用 Harbor 执行框架。**
真实模型评测独立于[自动化验证](./testing.zh-CN.md)，可能产生模型和搜索服务费用。
单独运行 `make benchmark` 只列出套件，不启动评测。

## 运行评测

从仓库根目录执行以下命令。
需要 Python 3.12+、Git、Docker 和 Compose，以及与任务容器兼容的 Linux Vista 二进制。
Terminal-Bench 样本镜像为 Linux/amd64；GAIA 的容器与二进制也须使用相同架构。

### 环境与模型

同一 Python 环境可以运行两套评测：

```sh
python3 -m venv .vista/benchmarks/harbor/venv
.vista/benchmarks/harbor/venv/bin/python -m pip install -r benchmarks/gaia/requirements.lock
mkdir -p .vista/benchmarks/profile
cp benchmarks/harbor/models.example.json .vista/benchmarks/profile/models.json
make build
```

只运行 Terminal-Bench 时，可改为安装 `benchmarks/harbor/requirements.lock`。
在 Make 命令中设置 `HARBOR_PYTHON` 可使用其他已准备好的环境。

编辑被 Git 忽略的 `models.json`，填写 API 格式、地址、密钥和模型 ID；也可将现有 Vista 的 `models.json` 复制到这个隔离目录。
Codex 连接还需复制对应的 `codex-auth/<connectionId>.json`。
目标模型不是配置默认项时，传入 `--model-key`。
不传 `--reasoning-effort` 时沿用 Vista 的默认值，也可明确指定模型支持的档位。

适配器只传入选中的模型、对应 Codex 凭据和可选的 `omp-models.json` 目录。
GAIA 还会复制可选的 `search.json` 网络查询配置。
全局提示词、记忆、用户技能、MCP 连接和其他模型不会传入；适配器也不会代替用户回答智能体的提问。

源配置目录不会被写入。
GAIA 会为每次运行建立配置快照，并在连续试验之间保留快照内刷新的 Codex 凭据。
如果另一个实例使用相同账号，应采用独立获取的凭据：即使源文件没有改动，OAuth 令牌轮换也可能影响其他副本。

### 通过 Harbor 运行 Terminal-Bench 样本

准备固定的数据集与运行镜像：

```sh
git clone https://github.com/laude-institute/terminal-bench-2-0-sample.git .vista/benchmarks/harbor/dataset
git -C .vista/benchmarks/harbor/dataset checkout 7e917f35c281188532772312d4ad91ca9274febc
docker pull ghcr.io/laude-institute/terminal-bench/build-cython-ext:2.0
docker pull ghcr.io/laude-institute/terminal-bench/fix-code-vulnerability:2.0
docker pull ghcr.io/laude-institute/terminal-bench/log-summary-date-ranges:2.0
make benchmark tb BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/profile --model YOUR_MODEL_ID --dry-run'
```

移除 `--dry-run` 后执行。
重复传入 `--task` 可选择样本任务。
比较版本时，使用 `--binary before=/path/to/baseline --binary after=bin/vista --rounds 3`。
每个二进制每轮执行所有选中任务，顺序轮换；并发为 1，每次试验只尝试一次，Harbor 重试为零，保留原始任务限制和判题器。
执行器根据 [`tasks.json`](../benchmarks/harbor/tasks.json) 检查数据集提交、任务文件哈希及运行镜像摘要。

### GAIA 验证集

先取得 [GAIA 数据集](https://huggingface.co/datasets/gaia-benchmark/GAIA)的访问权限：使用 Hugging Face 账号接受访问条件，再登录该账号。
令牌保存在 Hugging Face 凭据存储中，不写入受版本管理的文件或命令示例。

```sh
.vista/benchmarks/harbor/venv/bin/hf auth login
make benchmark gaia BENCHMARK_ARGS='--prepare'
make benchmark gaia BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/profile --model YOUR_MODEL_ID --reasoning-effort low --dry-run'
```

`--prepare` 下载固定版本的验证集资料、原始附件和官方判题器，不调用模型。
版本和资料／判题器哈希见 [`sources.json`](../benchmarks/gaia/sources.json)。
准备过程检查全部 165 题、各难度数量和 38 个附件，再记录生成的任务文件哈希。
数据保存在 `.vista/benchmarks/gaia/`。
文件已全部缓存时，可在 `--prepare` 时加入 `--offline`。
准备好的数据集不会被覆盖；需要其他目标目录时使用 `--dataset`。

移除 `--dry-run` 后运行全部 165 道验证题。
`--level 1` 或重复的 `--task TASK_ID` 可选择子集，报告成绩时必须注明子集。
并发为 1，只尝试一次，Harbor 重试为零，智能体限时 600 秒。
最终答案写入 `/app/answer.txt`；智能体执行结束后，Harbor 才将参考答案和官方判题器交给验证步骤。
判题器仅移除了未使用的 NumPy 导入。

附件原样放在 `/app/files/`。
支持的原始图片还会通过 Vista 消息 API 传入，保留产品的每张 1 MiB 上限，不缩图；超限输入仍计为未通过试验。
其他文件供工具读取。
适配器不会额外提供图片／视频工具，也不会绕过 Vista 现有的能力边界。

**网络访问未受限制，搜索可能命中转载的题目、答案和评测轨迹。
因此，这套执行器不能产出经过答案污染控制的 GAIA 成绩。**

### 产物与适配器检查

两套评测均支持 `--help` 和 `--dry-run`。
预检验证准备好的数据、二进制和选中配置，不启动 Docker、不调用模型。
Terminal-Bench 结果默认保存在 `.vista/benchmarks/harbor/runs/<时间戳>/`，GAIA 结果保存在 `.vista/benchmarks/gaia/runs/<时间戳>/`；`--output` 必须指定 `.vista/` 内尚不存在的目录。

`plan.json` 记录任务选择、二进制／配置／适配器哈希、数据集版本、模型、档位和运行命令。
每次运行保留原始 Harbor 结果、判题输出、Vista 事件、消息、构建信息和产物。
GAIA 固定运行副本中的二进制、适配器源码及非凭据配置，固定输入变化时停止后续试验。
在 GAIA 运行目录创建 `stop-after-current` 文件可在当前试验结束后停止，已有结果保持完整。

`summary.json` 将未通过、未判定、缺失和出错的试验保留在计划总数中。
Vista 正常完成且判题器判定成功，才算通过。
耗时汇总只统计正常完成并通过判题的试验，同时给出样本数；缺失的用量、费用仍记为未知。
GAIA 汇总还包含难度统计，并明确标注子集和答案污染限制。

```sh
.vista/benchmarks/harbor/venv/bin/python benchmarks/harbor/summarize.py .vista/benchmarks/gaia/runs/RUN_DIRECTORY
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/harbor/tests -v
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/gaia/tests -v
```

适配器检查使用合成样本和模拟模型，用来验证评测路径，不衡量真实服务商的任务成功率。
源码入口是 [`benchmarks/harbor/`](../benchmarks/harbor/) 和 [`benchmarks/gaia/`](../benchmarks/gaia/)。
仓库不包含数据集正文、凭据和真实运行轨迹。
