# Benchmarks

[简体中文](./benchmarks.zh-CN.md)

Benchmarks exercise Vista on tasks with an independently checked outcome.
They help developers spot regressions in tool use and task completion, and help users understand the tested capabilities and their limits.
A score describes the tested combination of binary, model, services, and settings; it is not a guarantee for an individual task.

| Entry point | Tasks | Use | Results |
| --- | --- | --- | --- |
| `make benchmark tb` | Three pinned Terminal-Bench 2.0 sample tasks | Compare Vista builds on data processing, code repair, and dependency/build work | [History and analysis](./benchmarks/tb.md) |
| `make benchmark gaia` | GAIA validation: 165 questions across three difficulty levels, including 38 attachments | Examine research, reasoning, file handling, and tool use; optionally select a subset | [History and analysis](./benchmarks/gaia.md) |

Harbor is the execution framework for **both** suites.
These live-model runs are separate from [automated verification](./testing.md) and can incur model and search-service charges.
`make benchmark` alone lists the suites; it starts no evaluation.

## Running an evaluation

Run the commands from the repository root.
Requirements are Python 3.12+, Git, Docker with Compose, and a Linux Vista binary compatible with the task containers.
The Terminal-Bench sample images are Linux/amd64.
Choose the same architecture for the GAIA containers and binary.

### Environment and model

One Python environment can run both suites:

```sh
python3 -m venv .vista/benchmarks/harbor/venv
.vista/benchmarks/harbor/venv/bin/python -m pip install -r benchmarks/gaia/requirements.lock
mkdir -p .vista/benchmarks/profile
cp benchmarks/harbor/models.example.json .vista/benchmarks/profile/models.json
make build
```

For Terminal-Bench alone, install `benchmarks/harbor/requirements.lock` instead.
Set `HARBOR_PYTHON` on Make commands to use another prepared environment.

Edit the ignored `models.json` with the desired API format, endpoint, key, and model ID.
Alternatively, copy an existing Vista `models.json` into that isolated profile.
For a Codex connection, also copy its matching `codex-auth/<connectionId>.json`.
Supply `--model-key` if the intended model is not the profile's default.
Omit `--reasoning-effort` to use Vista's default or provide a supported value explicitly.

The adapters transfer only the selected model, its matching Codex credential, and an optional `omp-models.json` catalog.
GAIA also copies optional `search.json` network-query settings.
Global instructions, memory, user skills, MCP connections, and other models are not transferred.
Pending questions are not answered on the agent's behalf.

The source profile is never written.
GAIA takes a run-local profile snapshot and retains refreshed Codex credentials there between sequential trials.
Use independently obtained credentials when another instance uses the same account: rotating OAuth credentials can affect other copies even when the source file is untouched.

### Terminal-Bench samples through Harbor

Prepare the fixed dataset and runtime images:

```sh
git clone https://github.com/laude-institute/terminal-bench-2-0-sample.git .vista/benchmarks/harbor/dataset
git -C .vista/benchmarks/harbor/dataset checkout 7e917f35c281188532772312d4ad91ca9274febc
docker pull ghcr.io/laude-institute/terminal-bench/build-cython-ext:2.0
docker pull ghcr.io/laude-institute/terminal-bench/fix-code-vulnerability:2.0
docker pull ghcr.io/laude-institute/terminal-bench/log-summary-date-ranges:2.0
make benchmark tb BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/profile --model YOUR_MODEL_ID --dry-run'
```

Remove `--dry-run` to execute.
Repeat `--task` to select sample tasks.
To compare builds, use `--binary before=/path/to/baseline --binary after=bin/vista --rounds 3`.
Each binary runs each selected task per round, with rotating order, concurrency 1, one attempt, zero Harbor retries, and the original task limits and verifiers.
The runner checks the dataset commit, task-file hashes, and runtime-image digests against [`tasks.json`](../benchmarks/harbor/tasks.json).

### GAIA validation

First obtain access to the [GAIA dataset](https://huggingface.co/datasets/gaia-benchmark/GAIA): accept its access conditions using a Hugging Face account, then authenticate that account.
Tokens belong in the Hugging Face credential store, not in tracked files or command examples.

```sh
.vista/benchmarks/harbor/venv/bin/hf auth login
make benchmark gaia BENCHMARK_ARGS='--prepare'
make benchmark gaia BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/profile --model YOUR_MODEL_ID --reasoning-effort low --dry-run'
```

`--prepare` downloads pinned validation metadata, original attachments, and the official scorer; it does not call a model.
Revisions and metadata/scorer hashes are in [`sources.json`](../benchmarks/gaia/sources.json).
Preparation validates all 165 tasks, level counts, and 38 attachments, then records generated task-file hashes.
Data stays under `.vista/benchmarks/gaia/`.
Use `--offline` with `--prepare` when all required files are already cached.
A prepared dataset is not overwritten; use `--dataset` for a different destination.

Remove `--dry-run` to run all 165 validation tasks.
`--level 1` or repeated `--task TASK_ID` selects a subset; report it as a subset.
Runs use concurrency 1, one attempt, zero Harbor retries, and a 600-second agent limit.
The final answer goes in `/app/answer.txt`; Harbor supplies reference answers and the official scorer to the verifier after agent execution.
The only scorer adjustment removes an unused NumPy import.

Attachments remain original files in `/app/files/`.
Supported original images also enter Vista through its message API, preserving the product's 1 MiB limit without resizing; oversized inputs remain unsuccessful trials.
Other files are available to tools.
The adapter does not add image/video tools or bypass Vista's existing capability boundaries.

**Network access is unrestricted.
Search can retrieve reposted questions, answers, and evaluation traces.
This runner does not produce a contamination-controlled GAIA score.**

### Outputs and adapter checks

Both suites support `--help` and `--dry-run`.
Dry runs validate the prepared data, binary, and selected configuration without starting Docker or calling models.
Results default to `.vista/benchmarks/harbor/runs/<timestamp>/` for Terminal-Bench or `.vista/benchmarks/gaia/runs/<timestamp>/` for GAIA; `--output` must name a new directory under `.vista/`.

`plan.json` records task selection, binary/configuration/adapter hashes, dataset revision, model, effort, and execution commands.
Runs keep original Harbor results, verifier output, Vista events, messages, build identity, and artifacts.
GAIA freezes its run-local binary, adapter sources, and non-credential configuration; changed frozen inputs stop later trials.
Creating `stop-after-current` in a GAIA run directory stops before the next trial.
Existing results remain intact.

`summary.json` retains failed, unjudged, missing, and error trials in the planned denominator.
A normal Vista completion only passes when the verifier also awards success.
Timing summaries cover successful autonomous completions and include their sample counts.
Missing usage or cost stays unknown.
GAIA summaries include difficulty-level counts and explicitly label subsets and contamination limits.

```sh
.vista/benchmarks/harbor/venv/bin/python benchmarks/harbor/summarize.py .vista/benchmarks/gaia/runs/RUN_DIRECTORY
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/harbor/tests -v
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/gaia/tests -v
```

Adapter checks use synthetic fixtures and a simulated model; they verify the evaluation path, not task success with a live provider.
Source entry points are [`benchmarks/harbor/`](../benchmarks/harbor/) and [`benchmarks/gaia/`](../benchmarks/gaia/).
Dataset contents, credentials, and live trajectories are not included in the repository.
