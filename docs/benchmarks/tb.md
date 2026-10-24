# Terminal-Bench results

[简体中文](./tb.zh-CN.md) · [Run the benchmarks](../benchmarks.md)

The fixed Terminal-Bench sample covers data processing, code repair, and dependency/build work.
Each history row summarizes one client across the selected tasks and rounds.
This fixed sample tracks task completion and execution overhead across versions and helps detect regressions.

## Result history

| Date | Client | Connection | Model | Effort | Coverage | Passed | Model calls | Delegations | Checklist updates |
| --- | --- | --- | --- | --- | --- | ---: | ---: | ---: | ---: |
| [2026-10-23](#2026-10-23) | Vista v0.1.4 | Codex | gpt-6.1-sol | low | 3 tasks × 3 rounds | 9/9 | 216 | 9 | 25 |
| [2026-10-23](#2026-10-23) | Vista v0.1.5 | Codex | gpt-6.1-sol | low | 3 tasks × 3 rounds | 9/9 | 108 | 0 | 9 |
| [2026-10-23](#2026-10-23) | OpenCode v2.1.3 | Codex | gpt-6.1-sol | low | 3 tasks × 3 rounds | 9/9 | — | — | — |

Vista counts include child-agent requests.
OpenCode's primary-session export is not an equivalent total-call measure; its activity counts are therefore not compared here.

## Run analysis

### 2026-10-23

#### Findings

Three tasks, three clients, and three rotating rounds produced 27 trials.
Vista v0.1.4, Vista v0.1.5, and OpenCode v2.1.3 each passed all nine trials with normal autonomous completion.
Vista v0.1.5 reduced recorded model calls from 216 to 108 (50%), delegations from 9 to 0, and checklist updates from 25 to 9 while retaining all nine passes.
With the same model, reasoning effort, and test conditions, these results show that the delegation and checklist prompt adjustments reduced coordination overhead while preserving successful completion on the three tested tasks.

Median model calls per task, with three trials per cell:

| Task | Vista v0.1.4 | Vista v0.1.5 |
| --- | ---: | ---: |
| `log-summary-date-ranges` | 16 | 6 |
| `fix-code-vulnerability` | 24 | 10 |
| `build-cython-ext` | 34 | 21 |

#### Test conditions

| Item | Value |
| --- | --- |
| Dataset | Three Terminal-Bench 2.0 sample tasks |
| Dataset revision | `7e917f35c281188532772312d4ad91ca9274febc` |
| Tasks | `log-summary-date-ranges`, `fix-code-vulnerability`, `build-cython-ext` |
| Clients | Vista v0.1.4, Vista v0.1.5, OpenCode v2.1.3 |
| Model connection | Same Codex access-token snapshot for all clients |
| Model | `gpt-6.1-sol` |
| Reasoning effort | `low` |
| Order | Three rotating rounds |
| Trials | 27: three tasks × three clients × three rounds |
| Concurrency | 1 |
| Attempts per trial | 1 |
| Harbor retries | 0 |
| Agent limit | Original 900-second limit |
| Verifiers | Original task verifiers |

#### Build identities

| Item | Value |
| --- | --- |
| Recorded source base | `15b37acf665a026f45427a5dad4d68dafcde6c98` |
| Source-diff SHA-256 | `b99a1c177aa06cca9d235bbe7ce2de17e7efdeb5e66d64d94ba7c219446f96b0` |
| Vista v0.1.4 SHA-256 | `550dbd7efb7cc2026b157f3bd7bbc19c5b79954e36b421f442a0ff5de5b17130` |
| Vista v0.1.5 SHA-256 | `955629641e34a85d31f589b18fde2cbdf28a98c62b941f9f708c6ff152d42aeb` |
| OpenCode v2.1.3 SHA-256 | `d28ae783dad6bd1d5b2352e4cffc3427225594141c0b5d4dfedc23b87b98d8f9` |
