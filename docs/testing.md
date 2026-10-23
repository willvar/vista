# Verification entry points

[简体中文](./testing.zh-CN.md)

The [Makefile](../Makefile) groups checks by behavior. The sections below describe their entry points, dependencies, and environments.

## Make targets

Run from the repository root with Go 1.26+, Node.js with native TypeScript support, npm, and Make. The race target also needs a supported C compiler/toolchain. Direct Go commands use `GOWORK=off`.

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

| Goal | Current checks |
| --- | --- |
| `go` | Go unit tests, `go vet`, and `go test -race` |
| `frontend` | Node unit tests, UA version updater checks, Vue/TypeScript checking, i18n validation, and embedded asset build |
| `e2e` | Playwright desktop, mobile portrait, and mobile landscape projects |
| `integration` | Native CLI/Server/Worker application test and smoke scripts for persistence, tools, queues, compaction, conflicts, and faults |
| `mcp` | SDK stdio/HTTP fixtures and pinned upstream Filesystem/Everything servers |
| `memory` | 20 open conversations plus 200 additional turns in one persistent Worker, with memory regression checks |
| `capacity` | Mixed stdio/HTTP MCP, shell and child work; Chat/Responses long output; slow SSE; eight image adapters |
| `all` | All seven goals in sequence, stopping at failure |

`make test` defaults to `go`. Combine goals with `make test frontend memory`; shared prerequisites run once. Locked npm dependencies are installed when needed. Browser checks find Chrome/Chromium on `PATH`; set `VISTA_CHROME` to override. Integration scripts require `sqlite3`. Missing prerequisites fail explicitly. MCP upstream dependencies may require network access to install.

Make builds test executables under `.vista/test-state/bin` and uses separate configurations and ports. It does not replace `bin/vista` or restart the user's instance. Browser state, traces, screenshots, and HTML reports live under `web/.vista/test-state/`; see [frontend checks](../web/README.md).

## Target-platform application checks

Build the application for the target OS, then run its application test there:

```sh
VISTA_TEST_BINARY=/absolute/path/to/vista GOWORK=off go test ./cmd/vista -run '^TestNative(Application|ResetPort|WindowsDesktopLauncher|WindowsTray.*)$' -count=1
```

On Windows, set the equivalent environment variables in PowerShell and use the `.exe` path. A cross-compiled `go test -c` executable can run without installing Go on that machine. The tests use temporary configuration and a simulated model, exercising real application Workers, shell/background work, cancellation, startup/shutdown, reset, and crash recovery. The Windows desktop-launch test uses a private Explorer-named parent and browser command to check startup, reopening, and the native startup-error dialog without opening the user's browser. Tray checks cover click notifications, the context menu, parsed/long native status, icon restoration, Restart, and Stop. Icon registration checks require an interactive Windows session with Explorer; a headless run skips that test. Platform-specific tests also cover process ownership and cleanup.

Cross-compilation confirms compilation only. Mock endpoints establish request/stream behavior, not real account authorization or live-provider compatibility.

## Capacity tools

| Script | Purpose |
| --- | --- |
| [capacity_smoke.mjs](../scripts/capacity_smoke.mjs) | Open-conversation and repeated-turn memory regression |
| [mixed_capacity_smoke.mjs](../scripts/mixed_capacity_smoke.mjs) | Mixed shell/MCP/child tasks, stdio or HTTP |
| [slow_sse_capacity_smoke.mjs](../scripts/slow_sse_capacity_smoke.mjs) | Slow event readers and long replies |
| [long_output_smoke.mjs](../scripts/long_output_smoke.mjs) | Long model-text persistence and reading |
| [image_capacity_smoke.mjs](../scripts/image_capacity_smoke.mjs) | Concurrent and repeated image input; historical images must not be resent automatically |
| [evaluate_mixed_capacity.py](../scripts/evaluate_mixed_capacity.py) | Evaluate mixed-workload reports against its explicit cgroup, workload, and memory-growth checks |

Direct script runs need an absolute `VISTA_BIN` and isolated `VISTA_TEST_TMPDIR`; mixed/MCP workloads also need `VISTA_MCP_FIXTURE`. Parameters are defined in each script. The evaluator expects the report's specified 8 GiB cgroup and workload; it is not a generic validator for arbitrary reports. Ordinary `make test memory`/`capacity` does not create a constrained cgroup or establish a long-duration capacity guarantee.

Record executable SHA-256/source revision, platform/toolchain, parameters, failures, Worker identities, idle heap/RSS, and any cgroup peak/OOM values. Compare several idle checkpoints after warm-up. Soft reservations and Go soft limits are not OS hard memory limits. Measure browser memory separately; bounded cache tests do not by themselves prove browser endurance.

## Harbor task benchmark

[`benchmarks/harbor/`](../benchmarks/harbor/) contains the Vista adapter, container-side HTTP runner, fixed task manifest, pinned Python dependencies, and result summarizer. It runs an unmodified Vista binary through its real session API and tools. Harbor supplies task containers, original instructions, limits, and verifiers. This is an optional live-model benchmark, separate from `make test all`.

Requirements: Python 3.12+, Git, Docker with Compose, and a Linux Vista binary compatible with the task images (the pinned sample images are for amd64). Prepare the environment from the repository root:

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

Fill the isolated `models.json` with the intended connection and model. API keys remain in that ignored file. For a Codex OAuth connection, include its `connectionId` and copy the matching `codex-auth/<connectionId>.json` into the isolated profile; an expired snapshot may refresh during execution, so use independently obtained credentials when another instance uses the same account. The adapter transfers only the selected model, its matching Codex credential, and an optional `omp-models.json` catalog. It does not copy global instructions, memory, skills, or other model connections, and never changes the source profile.

```sh
make benchmark BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/harbor/profile --model YOUR_MODEL_ID --dry-run'
make benchmark BENCHMARK_ARGS='--binary candidate=bin/vista --profile .vista/benchmarks/harbor/profile --model YOUR_MODEL_ID'
```

`make benchmark` alone prints help. Set `HARBOR_PYTHON` to use another prepared environment. Omit `--reasoning-effort` to follow the configured default, or set an explicit supported value. Repeat `--task` to select tasks. `--dry-run` checks the binary, selected profile, and pinned task files and prints the schedule without starting Harbor, checking Docker, or calling a model. A real run also validates the installed Harbor version and local task image digests. Changed dataset revisions, task files, or image digests require an intentional manifest update.

Repeat `--binary before=/path/to/baseline --binary after=bin/vista` and set `--rounds 3` for an interleaved comparison. Every label runs every selected task once per round, with rotating order, concurrency 1, zero Harbor retries, and original task limits and verifiers. The binaries use the same selected model and source profile; differing built-in catalogs or defaults remain part of the binaries being compared. The adapter adds a working-directory prefix to the original instruction and uses a full-file reference when the combined instruction exceeds Vista's input limit. It never supplies answers to questions on the agent's behalf.

Runs default to `.vista/benchmarks/harbor/runs/<timestamp>/`; `--output` must also point under `.vista/` and must not exist. `plan.json` records the schedule, model, effort, binary/profile/adapter hashes, dataset revision, and image identities. Each trial retains original Harbor results, verifier output, Vista events, messages, build identity, and artifacts. Input changes abort subsequent trials. Existing records are not overwritten; no automatic image pruning occurs.

`summary.json` keeps failed, unjudged, missing, and infrastructure-error trials in the planned count. Autonomous completion and original verifier reward are separate evidence; a successful Vista run alone does not mean the task passed. Timing aggregates include only autonomous completions that passed the verifier and report their sample count. Incomplete request usage remains unknown, and unknown cost remains unknown. Regenerate a local summary with:

```sh
.vista/benchmarks/harbor/venv/bin/python benchmarks/harbor/summarize.py .vista/benchmarks/harbor/runs/RUN_DIRECTORY
VISTA_BENCHMARK_BIN="$PWD/bin/vista" .vista/benchmarks/harbor/venv/bin/python -m unittest discover -s benchmarks/harbor/tests -v
```

The adapter tests use fixtures and a simulated model; they do not measure live task success. The three fixed sample tasks cover data processing, code repair, and dependency/build work. They are exploratory regression samples, not a representative estimate of overall task success rate. Dataset contents and live run records are not part of the maintained benchmark source.

## Generated provider fixture

The [provider matrix fixture](../scripts/provider_matrix/testdata/catalog.csv) is deterministic catalog/routing data, not a real-provider report. Regeneration and evidence limits are in [providers](./providers.md#protocol-and-verification-boundaries).
