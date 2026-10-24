# GAIA 评测结果

[English](./gaia.md) · [运行评测](../benchmarks.zh-CN.md)

GAIA 验证集覆盖资料检索、推理、文件处理和工具使用，分为三个难度级别。
准确率为通过题数除以计划题数，未通过和缺失的任务均计入总数。
成绩反映所记录的模型、服务和测试条件。

## 历次成绩

2026-10-23 的结果包含已确认的搜索参考答案暴露。
它是原始诊断结果，并非经过答案污染控制的成绩。

| 日期 | Vista | 连接 | 模型 | 推理档位 | 覆盖范围 | 通过 | 准确率 | 答案暴露 |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| [2026-10-23](#2026-10-23) | [208de5a](https://github.com/willvar/vista/commit/208de5a0fd494aa0c2d02091108cfddf4b10f2e5) | Codex | gpt-6.1-sol | low | 验证集 165/165 题 | 137/165 | 83.03% | 至少 6 道已通过任务 |

## 各轮分析

### 2026-10-23

#### 各难度结果

| 难度 | 通过／计划 | 准确率 |
| --- | ---: | ---: |
| 1 | 48/53 | 90.57% |
| 2 | 71/86 | 82.56% |
| 3 | 18/26 | 69.23% |
| 全部 | 137/165 | 83.03% |

本轮覆盖全部验证题、所有难度和 38 个附件。
验证集答案公开，本轮并非答案隐藏的测试集。
28 道未通过任务中，26 道正常自主结束但未通过判题。
两张原始图片在模型请求前因输入上限被拒绝。
智能体超时和模型 API 错误未造成这些结果。
没有剔除失败或重新判分。

#### 失败归因

每道未通过任务归入一个主要诊断类别，次要因素可能重叠。

| 主要观察 | 题数 |
| --- | ---: |
| 原始图片超过输入上限 | 2 |
| 下载图片或提取视频帧缺少直接视觉输入路径 | 3 |
| 输出格式、名称变体或单位不符合判题要求 | 8 |
| 题目／参考答案冲突，或建模假设有歧义 | 6 |
| 检索数据／来源版本与标注记录不同 | 5 |
| 来源选择、核实或历史名称理解问题 | 4 |
| 合计 | 28 |

该版本中值得处理的 Vista 问题包括：

- **工具获得的图片：**下载的图片和提取的视频帧缺少转入会话视觉资源的路径。
- **图片输入：**1 MiB 边界阻止了超限原图输入。
- **网页读取：**在内容校验前决定是否回退，可能造成部分 PDF 失败，或将空内容／仅有 JavaScript 提示的页面当作读取成功。
- **文件读取：**`read_file` 按行分页之前仍对整个文件做 1 MiB 检查。

这些问题会阻止信息获取或增加步骤，但上述数量不代表某项修复能挽回多少题。
原始判题结果保持不变。

这些评测结果和失败归因将用于指导 Vista 的后续优化与调整，优先改善影响实际任务完成的问题，并通过后续评测检验改进效果。

#### 答案暴露

至少六道已通过任务的搜索结果包含参考答案文本。
有 16 道任务中的 45 条搜索结果可疑，但不代表确认了 16 道泄漏。
暴露对具体答案的影响未知，扣除六道也无法得到干净成绩。
轨迹中未发现读取本地参考答案文件的工具调用。

#### 测试条件

| 项目 | 值 |
| --- | --- |
| 数据集 | 完整 GAIA 验证集，全部三个难度级别 |
| 题数 | 165 |
| 附件数 | 38 |
| 数据集版本 | [682dd723ee1e1697e00360edccf2366dc8418dd9](https://huggingface.co/datasets/gaia-benchmark/GAIA/tree/682dd723ee1e1697e00360edccf2366dc8418dd9) |
| 模型连接 | Codex |
| 模型 | `gpt-6.1-sol` |
| 推理档位 | `low` |
| 框架 | Harbor 0.24.0 |
| 环境 | Linux/amd64 容器，隔离配置 |
| 每题尝试次数 | 1 |
| 并发 | 1 |
| Harbor 重试次数 | 0 |
| 智能体时限 | 600 秒 |
| 图片 | 原图保留、不缩放；输入上限 1 MiB |
| 观察到成功调用的搜索服务 | Exa、Tavily、Parallel |
| 观察到成功调用的网页读取器 | Vista 内置读取器、Jina Reader |
| 官方判题器版本 | [9f133d71362e77b3539f1514f31b9c101a545fec](https://huggingface.co/spaces/gaia-benchmark/leaderboard/blob/9f133d71362e77b3539f1514f31b9c101a545fec/scorer.py) |
| 判题器调整 | 仅移除未使用的 NumPy 导入 |
| 完成时间 | 2026-10-23T23:12:08Z |

#### 构建标识

| 项目 | 值 |
| --- | --- |
| Vista 源码 | [208de5a0fd494aa0c2d02091108cfddf4b10f2e5](https://github.com/willvar/vista/commit/208de5a0fd494aa0c2d02091108cfddf4b10f2e5) |
| 源码工作区 | 构建时干净 |
| 二进制 SHA-256 | `3baa306246ed7bc0c784b2c666b20a3fd4b43ab8c8867f8c3b3c649e36e4c746` |

## 其他已公开的 GAIA 成绩

### 2026-10-24

下表汇总公开的验证集成绩，各项使用的模型、服务、时限和答案污染控制不同。

| 系统 | 模型 | 报告成绩 | 来源 |
| --- | --- | ---: | --- |
| Codex CLI，经 ACP | GPT-5.5 | 84.8% | [OpenHands 评测，2026 年 5 月](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-codex/GPT-5.5/scores.json) |
| Claude Code，经 ACP | Opus 4.7 | 74.5% | [OpenHands 评测，2026 年 4 月](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-claude/claude-opus-4-7/scores.json) |
| Gemini CLI，经 ACP | Gemini 3.1 Pro | 88.5% | [OpenHands 评测，2026 年 4 月](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-gemini/Gemini-3.1-Pro/scores.json) |
| OpenHands | GPT-5.5 | 86.1% | [项目结果，2026 年 4 月](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/results/GPT-5.5/scores.json) |
| Tabloy | 未披露具体模型 | 77.6% | [厂商报告，2026 年 10 月](https://docs.tabloy.ai/benchmarks/gaia)，128/165、一次尝试、无重试、官方判题器 |
| HAL Generalist | Claude Sonnet 4.5 | 74.55% | [HAL 复现](https://hal.cs.princeton.edu/gaia)，两次运行 |

[ACP 运行表](https://github.com/OpenHands/benchmarks/issues/576)记录 Codex 为 140/165、Claude Code 为 123/165、Gemini CLI 为 146/165。
Gemini 实际覆盖 159/165 题，报告成绩仍以 165 为分母。
这些是第三方评测，并非客户端厂商发布的成绩。
