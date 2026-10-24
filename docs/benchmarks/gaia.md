# GAIA results

[简体中文](./gaia.zh-CN.md) · [Run the benchmarks](../benchmarks.md)

GAIA validation covers research, reasoning, file handling, and tool use across three difficulty levels.
Accuracy is the number of passed tasks divided by the planned task count, including unsuccessful and missing tasks.
Scores describe the recorded model, services, and test conditions.

## Result history

The 2026-10-23 result includes confirmed reference-answer exposure through search.
It is a raw diagnostic result, not a contamination-controlled score.

| Date | Vista | Connection | Model | Effort | Coverage | Passed | Accuracy | Answer exposure |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| [2026-10-23](#2026-10-23) | [208de5a](https://github.com/willvar/vista/commit/208de5a0fd494aa0c2d02091108cfddf4b10f2e5) | Codex | gpt-6.1-sol | low | 165/165 validation tasks | 137/165 | 83.03% | At least 6 passed tasks |

## Run analysis

### 2026-10-23

#### Results by level

| Level | Passed / planned | Accuracy |
| --- | ---: | ---: |
| 1 | 48/53 | 90.57% |
| 2 | 71/86 | 82.56% |
| 3 | 18/26 | 69.23% |
| All | 137/165 | 83.03% |

The run covered all validation tasks, difficulty levels, and 38 attachments.
The validation answers are public; this was not the hidden test set.
Of 28 unsuccessful tasks, 26 finished autonomously but failed scoring.
Two original images exceeded the input limit before a model request.
Agent timeouts and model API errors did not account for these outcomes.
No failures were excluded or regraded.

#### Failure analysis

Each unsuccessful task received one primary diagnostic category; contributors can overlap.

| Primary observation | Tasks |
| --- | ---: |
| Original image exceeded the input limit | 2 |
| Downloaded image or extracted frame lacked a direct visual-input path | 3 |
| Output format, alternate name, or unit did not meet scoring requirements | 8 |
| Question/reference conflict or ambiguous modeling assumptions | 6 |
| Retrieved data/source version differed from the annotation record | 5 |
| Source selection, verification, or historical-name interpretation | 4 |
| Total | 28 |

The actionable Vista findings at this revision were:

- **Tool-acquired images:** downloaded images and extracted frames lacked a path into conversation-owned visual resources.
- **Image input:** the 1 MiB boundary blocked oversized original images.
- **Page reading:** fallback was decided before content validation, allowing some PDF failures or empty/JavaScript-only successes.
- **File reading:** the whole-file 1 MiB check preceded `read_file` line pagination.

These can block information access or add work, but the counts do not predict how many answers any particular fix would recover.
Original scorer outcomes remain unchanged.

These results and failure analyses will guide future improvements to Vista, prioritize issues that affect real task completion, and inform follow-up evaluations of the changes.

#### Answer exposure

At least six passed tasks received reference-answer text in search results.
There were 45 suspect search results across 16 tasks, but these are not 16 confirmed leaks.
The effect on individual answers is unknown; subtracting six passes would not establish a clean score.
No local reference-answer file reads were found in the trajectories.

#### Test conditions

| Item | Value |
| --- | --- |
| Dataset | Full GAIA validation set, all three levels |
| Tasks | 165 |
| Attachments | 38 |
| Dataset revision | [682dd723ee1e1697e00360edccf2366dc8418dd9](https://huggingface.co/datasets/gaia-benchmark/GAIA/tree/682dd723ee1e1697e00360edccf2366dc8418dd9) |
| Model connection | Codex |
| Model | `gpt-6.1-sol` |
| Reasoning effort | `low` |
| Harness | Harbor 0.24.0 |
| Environment | Linux/amd64 containers, isolated configuration |
| Attempts per task | 1 |
| Concurrency | 1 |
| Harbor retries | 0 |
| Agent limit | 600 seconds |
| Images | Originals retained without resizing; 1 MiB input limit |
| Successful search services observed | Exa, Tavily, Parallel |
| Successful page readers observed | Vista reader, Jina Reader |
| Official scorer revision | [9f133d71362e77b3539f1514f31b9c101a545fec](https://huggingface.co/spaces/gaia-benchmark/leaderboard/blob/9f133d71362e77b3539f1514f31b9c101a545fec/scorer.py) |
| Scorer adjustment | Removed only the unused NumPy import |
| Completed | 2026-10-23T23:12:08Z |

#### Build identity

| Item | Value |
| --- | --- |
| Vista source | [208de5a0fd494aa0c2d02091108cfddf4b10f2e5](https://github.com/willvar/vista/commit/208de5a0fd494aa0c2d02091108cfddf4b10f2e5) |
| Source working tree | Clean when built |
| Executable SHA-256 | `3baa306246ed7bc0c784b2c666b20a3fd4b43ab8c8867f8c3b3c649e36e4c746` |

## Other published GAIA results

### 2026-10-24

The table summarizes published validation scores obtained with different models, services, time limits, and contamination controls.

| System | Model | Reported score | Source |
| --- | --- | ---: | --- |
| Codex CLI through ACP | GPT-5.5 | 84.8% | [OpenHands evaluation, May 2026](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-codex/GPT-5.5/scores.json) |
| Claude Code through ACP | Opus 4.7 | 74.5% | [OpenHands evaluation, April 2026](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-claude/claude-opus-4-7/scores.json) |
| Gemini CLI through ACP | Gemini 3.1 Pro | 88.5% | [OpenHands evaluation, April 2026](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/alternative_agents/acp-gemini/Gemini-3.1-Pro/scores.json) |
| OpenHands | GPT-5.5 | 86.1% | [Project results, April 2026](https://github.com/OpenHands/openhands-index-results/blob/3015ac612e7196f428e6e8a3948965d32d9a3331/results/GPT-5.5/scores.json) |
| Tabloy | Specific model not disclosed | 77.6% | [Vendor report, October 2026](https://docs.tabloy.ai/benchmarks/gaia); 128/165, one attempt, no retries, official scorer |
| HAL Generalist | Claude Sonnet 4.5 | 74.55% | [HAL reproduction](https://hal.cs.princeton.edu/gaia), two runs |

The [ACP run table](https://github.com/OpenHands/benchmarks/issues/576) reports 140/165 passes for Codex, 123/165 for Claude Code, and 146/165 for Gemini CLI.
Gemini's run covered 159/165 tasks; the reported score still uses 165 as its denominator.
These are third-party evaluations, not scores published by the clients' vendors.
