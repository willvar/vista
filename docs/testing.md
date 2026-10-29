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
| [image_guard_smoke.mjs](../scripts/image_guard_smoke.mjs) | A 50 MiB original, Worker memory interruption, retained original and text recovery without replay |
| [evaluate_mixed_capacity.py](../scripts/evaluate_mixed_capacity.py) | Evaluate mixed-workload reports against its explicit cgroup, workload, and memory-growth checks |

Direct script runs need an absolute `VISTA_BIN` and isolated `VISTA_TEST_TMPDIR`; mixed/MCP workloads also need `VISTA_MCP_FIXTURE`. Parameters are defined in each script. The 20-session load scripts explicitly configure an 8 GiB admission budget and 20 concurrent executions in their isolated state; they do not rely on the product defaults. The evaluator expects the report's specified 8 GiB cgroup and workload; it is not a generic validator for arbitrary reports. Ordinary `make test memory`/`capacity` does not create a constrained cgroup or establish a long-duration capacity guarantee.

Record executable SHA-256/source revision, platform/toolchain, parameters, failures, Worker identities, idle heap/RSS, and any cgroup peak/OOM values. Compare several idle checkpoints after warm-up. Measured-memory admission and Go soft limits are not OS hard memory limits. Measure browser memory separately; bounded cache tests do not by themselves prove browser endurance.

## Live-model benchmarks

Use `make benchmark tb` for the three pinned Terminal-Bench samples, or `make benchmark gaia` for GAIA validation tasks. `make benchmark` alone lists the suites. Both use Harbor and the real Vista session API; they are separate from `make test all` and can incur model and search-service charges.

See [benchmarks](./benchmarks.md) for setup, model credentials, execution commands, and result interpretation.
Recorded results and analyses are maintained separately for [Terminal-Bench](./benchmarks/tb.md) and [GAIA](./benchmarks/gaia.md).
The maintained adapters live in [`benchmarks/harbor/`](../benchmarks/harbor/) and [`benchmarks/gaia/`](../benchmarks/gaia/).

## Generated provider fixture

The [provider matrix fixture](../scripts/provider_matrix/testdata/catalog.csv) is deterministic catalog/routing data, not a real-provider report. Regeneration and evidence limits are in [providers](./providers.md#protocol-and-verification-boundaries).
