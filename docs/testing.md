# Verification entry points

[简体中文](./testing.zh-CN.md)

The [Makefile](../Makefile) groups checks by behavior. The sections below describe their entry points, dependencies, and environments.

## Make targets

Run from the repository root with Go 1.26+, Node.js with native TypeScript support, npm, and Make. The race target also needs a supported C compiler/toolchain. Direct Go commands use `GOWORK=off`.

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

| Goal | Current checks |
| --- | --- |
| `test` (without another goal) | Go/frontend checks, fixed browser smoke flows, and affected modules plus their consumers |
| `go` | Go unit tests, `go vet`, and `go test -race` |
| `frontend` | Node unit tests, UA version updater checks, Vue/TypeScript checking, i18n validation, and embedded asset build |
| `e2e` | Every Playwright scenario on desktop; responsive, touch and scrolling checks on mobile portrait/landscape |
| `integration` | Native CLI/Server/Worker application test and smoke scripts for persistence, tools, queues, compaction, conflicts, and faults |
| `mcp` | SDK stdio/HTTP fixtures and pinned upstream Filesystem/Everything servers |
| `memory` | 20 open conversations plus 200 additional turns in one persistent Worker, with memory regression checks |
| `capacity` | Mixed stdio/HTTP MCP, shell and child work; Chat/Responses long output; slow SSE; eight image adapters |
| `all` | All seven goals in sequence, stopping at failure |

Explicit goals such as `make test frontend memory` run only those checks; shared prerequisites run once. Locked npm dependencies are installed when needed. Browser checks find Chrome/Chromium on `PATH`; set `VISTA_CHROME` to override. Integration scripts require `sqlite3`. Missing prerequisites fail explicitly. MCP upstream dependencies may require network access to install.

Make builds test executables under `.vista/test-state/bin` and uses separate configurations and ports. It does not replace `bin/vista` or restart the user's instance. Browser state, traces, screenshots, and HTML reports live under `web/.vista/test-state/`; see [frontend checks](../web/README.md).

`E2E_ARGS` passes options to Playwright without changing the default projects, for example `make test e2e E2E_ARGS='--max-failures=1 --workers=2'`.
Independent cases can run in parallel within one spec file.
Local runs default to at most four workers, capped by available CPU parallelism; CI uses two, and `--workers` overrides either default.
Each case retains its own browser context and mutable fixture state, including isolated configuration, database and port for native Vista instances.
Shared browser fixture builds and the two source-module failure-injection exceptions are described in [frontend checks](../web/README.md#browser-fixtures).
Failed cases run once more to capture a trace; even a passing retry fails the gate. The original failure keeps its screenshot/error context, while the trace describes the retry. Use `--trace=retain-on-failure` when original-attempt tracing is needed for intermittent failures.

`make test` compares staged, unstaged and untracked source against `HEAD` by default.
Set `TEST_BASE=<commit>` to include changes already committed since an earlier validation.
The selector in [`e2e-coverage.ts`](../web/scripts/e2e-coverage.ts) adds the browser consumers of known modules and always retains `@smoke` workflows.
Shared components, global styles, fixtures, build/configuration changes and unclassified source select the complete browser suite.
Regenerated frontend bundles follow the accompanying source selection; bundle-only changes select the complete suite.
Documentation and unit-test-only edits retain smoke without adding unrelated browser scenarios.
Selection and result records, source identity, binary SHA-256 and elapsed browser time are written under `.vista/acceptance/local-checks/`.
Custom file/grep filters use `make test e2e`; the local selector rejects filters that could silently remove mandatory smoke or consumers.

Desktop remains the complete browser baseline.
Mobile projects use the explicit responsive coverage list, including multilingual layout, touch, dragging, scrolling, virtualized history and modal geometry; width-independent persistence and protocol recovery run once on desktop.
Mixed suites for controls, page contracts, quoted replies and background processes mark their viewport-dependent cases with `@responsive` instead of repeating the whole file on phones.
Value rules, keyboard behavior and request contents retain their desktop browser assertions; dedicated touch cases and layout checks still run in both mobile orientations.
The same classification applies locally and in CI.
Language detection, saved preferences, localized controls and request-language injection remain desktop workflows; separate language-parameterized checks perform the page-wide layout sweeps, verify both themes, and capture nested dialogs on all three viewports.
Pure state/protocol checks also run through Node and Go tests rather than depending solely on browser repetitions.

## GitHub builds and releases

[`validate.yml`](../.github/workflows/validate.yml) accepts manual `workflow_dispatch` runs and pushes of `v*` tags.
Run checks without publishing from Actions → Build and release, or `gh workflow run validate.yml -f browser=true`.
Manual branch runs build `dev` archives; tag runs use the tag's `vMAJOR.MINOR.PATCH` version.

Go units/vet/race and frontend checks run on Ubuntu.
The native matrix builds and packages committed source separately for x86-64 and ARM64 on Linux, Windows and macOS, covering all six system/architecture combinations.
[`ci_native.py`](../scripts/ci_native.py) rejects a cross-compiled target, checks source/platform build metadata, verifies archive contents and checksums, and runs the unpacked executable through the existing application, platform and MCP-host tests.
Its report lists the toolchain, source commit, executable/archive hashes and skipped tests; mandatory application and process checks must pass.
The script uses the committed frontend assets, UA versions and notices without rewriting them.
It can also run locally with Python 3.11+, for example `python3 scripts/ci_native.py --target linux/amd64 --version dev`; local reports identify uncommitted source explicitly.
On Windows, [`ci_windows.ps1`](../scripts/ci_windows.ps1) runs the checks through Explorer or a local WMI desktop process so the runner's enclosing process Job does not prevent detached application startup.
Hosted Windows ARM64 jobs dismiss first-logon privacy setup before desktop checks.
When the runner lacks an Explorer desktop entry, the process starts Explorer and requires a ready taskbar before validation; desktop-launch, tray and native status tests must pass on both architectures.
The native script checks process-breakaway permission before building, without changing the application's launch flags.

The Linux browser matrix installs the Chromium version matching the pinned Playwright package and runs each viewport project on a separate runner with two workers, using the coverage classification above.
Manual runs may disable it; pushed release tags always run it.
Native reports, archives and browser summaries are retained for three days; browser traces/screenshots are uploaded on failure only.
Runner results establish the checks actually run; simulated model endpoints do not establish live-provider compatibility.

Pushing an annotated `vMAJOR.MINOR.PATCH` tag starts an automatic release.
The tag message supplies the release notes; use a signed tag with the concise English release description.
After all checks pass, [`ci_release.py`](../scripts/ci_release.py) verifies every native report's source, version, result and archive checksum, then combines the exact tested archives with one `SHA256SUMS` file.
Only the publishing job has repository write permission; it creates a regular GitHub Release after uploading the archives, without rebuilding or replacing an existing published release.

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

When running tests through the foreground `shell` tool, set the test runner's diagnostic timeout below the shell timeout.
For example, use `GOWORK=off go test -timeout=120s ./internal/agent` with `timeoutSeconds: 180`; Go can print its timeout stack trace before shell process cleanup.
For a suite that may exceed the foreground timeout, use a managed background job and read its saved output rather than increasing or removing the shell's deadline.
