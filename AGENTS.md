# Vista Development Rules

## Product Audience and Tradeoffs

Consult the [audience and design baseline](./docs/product.md#audience-and-design-baseline) before changing features or interactions. Vista primarily serves users who can independently manage files, install software, and adjust common settings; programming, command-line, API, and service-deployment knowledge are not prerequisites. Use clear defaults and explain unfamiliar concepts through their purpose and tradeoffs; expose advanced controls as needed.

Keep product wording neutral about deployment location; do not position Vista as tied to a user's personal computer.

## Client–Server Data Flow

Optimize the complete data path: round trips, payload size, server work, and recovery correctness.
Use the shared SSE connection for changing application state; keep conversation history on its durable event stream.
Publish after the underlying write commits, coalesce related updates, and include bounded state payloads instead of making clients fetch the same state after every notification.
Reuse synchronized snapshots when opening a view, deduplicate concurrent reads, and do not refresh unrelated endpoints after mutations.
Scope data to the active view: the workspace synchronizes open session summaries and aggregate counts; load closed summaries only while the session manager is open, and release its subscription when it closes.
Apply the same scope to HTTP responses and SSE payloads; fewer requests must not hide unnecessary preloading or broadcasts.
Keep large logs, diagnostic bodies, images, and older history as bounded, on-demand HTTP reads; push their availability or lightweight indexes rather than their contents.
Retain revision checks and authoritative snapshot recovery for initial loading, reconnects, missed events, and oversized payloads.
Do not replace these safeguards with periodic HTTP polling or broadcast credentials and login authorization URLs through shared state.
Background synchronization must preserve active editors and reject stale responses.
Verify request counts, payload contents and size as well as visible updates, including cross-client changes and recovery.

## Documentation

Vista supports its own development. Product documentation serves users; development documentation primarily serves agents. Prefer executable commands, code entry points, and interface/state/data constraints over tutorials and duplicated inventories.

- Keep `README.md` and its Chinese edition as the user entry points. Put maintained product and technical documentation in `docs/`; keep frontend-specific development instructions in `web/README.md`.
- Public documents describe current implementation. Check behavior against source and relevant tests; do not turn intended behavior, catalog metadata, or mock tests into claims of verified live support.
- Keep repository workflow rules here. Public documentation should not repeat these rules or contain superseded development phases, local decision history, or temporary acceptance status.
- Store plans, decisions, POCs, acceptance checklists/results, and local diagnostics under ignored `.vista/`, for example `.vista/plans/`, `.vista/decisions/`, and `.vista/acceptance/`. Move lasting implemented contracts into the relevant maintained document.
- Maintain this `AGENTS.md` in English. Other permanent Markdown documents use English in the unsuffixed file and Simplified Chinese in `.zh-CN.md`, with reciprocal language links. Keep both editions and relative links consistent. Embedded system skills and their supporting documents are English-only; the UI may localize its controls.
- Vista's original code and documentation use MIT (`LICENSE`). Preserve third-party license, copyright, and NOTICE texts in their original language; project licensing does not replace them.
- Generated regression fixtures belong with their tests, not in a public reports directory. Do not publish user configuration, credentials, traces, screenshots, or machine-specific measurements.
- Keep benchmark instructions in `docs/benchmarks.md` and result histories in `docs/benchmarks/tb.md` and `docs/benchmarks/gaia.md`, with matching Chinese editions. Append result rows and dated analyses; preserve earlier records and identify corrections explicitly. Keep table columns and failure categories stable, use one sentence or condition per source line, and avoid reformatting existing rows when adding results.

## Build and Verification

- Set `GOWORK=off` for direct Go commands. `make build` embeds the frontend and uses version `dev`; `make build VERSION=v0.1.0` supplies a release version.
- Non-`dev` builds refresh bundled UA versions from the clients' official npm packages before compiling; any lookup failure stops the build. Use `make update-ua` to refresh separately. The generated snapshot belongs in release source; `UPDATE_UA=0` reuses that snapshot for reproducible or multiple-platform rebuilds. Do not edit `internal/useragent/versions_generated.go` directly.
- `make licenses` rebuilds the frontend and regenerates `licenses/NOTICE` from its bundled packages, OpenCC conversion data, pinned Go modules, the Go standard library, and licenses of adapted code/model catalogs. Preserve upstream auxiliary notices. `make build` runs this step automatically; use `make check-licenses` to verify it without rewriting the notices. Do not edit the generated file directly.
- Package binaries with `make package VERSION=<version>` (Python 3 required), or `python3 scripts/package_release.py --binary <path> --version <version> --output-dir <directory>` for an already built target. Every archive must contain `LICENSE` and `THIRD_PARTY_NOTICES`; the packager rejects binaries that do not embed the current notices. Outputs default to ignored `.vista/dist/`. These commands do not publish releases.
- After each completed batch of development changes, run `make build` to refresh `bin/vista` before handing the work back. An isolated test binary does not satisfy this requirement. Rebuilding does not authorize restarting the running instance.
- Select relevant checks with `make test <goal>`. Goals are `go`, `frontend`, `e2e`, `integration`, `mcp`, `memory`, and `capacity`. `make test` defaults to `go`; `make test all` runs all seven. See [verification](./docs/testing.md) and the [Makefile](./Makefile).
- Isolate configurations, ports, browser profiles, and test binaries. Do not replace the user's executable or restart their instance as part of testing. Put generated material under `.vista/`.
- Tie verification claims to the source commit, artifact hash, environment, and checks actually used. Keep local verification records under `.vista/acceptance/`. Mock protocol tests do not establish live-provider compatibility; cross-compilation does not establish runtime behavior on the target OS.

## Git Commit Messages

Use exactly one line, with no body:

```text
TYPE: English description
```

Use an uppercase type and a concise, specific English description:

| Type | Purpose |
| --- | --- |
| `ADD` | New functionality or capabilities |
| `FIX` | Corrections to defects |
| `OPT` | Usability or performance improvements |
| `REFACTOR` | Structural changes without intended behavior changes |
| `TEST` | Automated test additions or changes |
| `DOCS` | Documentation changes |
| `CHORE` | Builds, dependencies, versioning, and maintenance |

Keep each commit focused on one related group. Implementation and directly related tests may be committed together. Avoid vague descriptions such as `update` or `changes`.

## Repository State

The formal remote is [willvar/vista](https://github.com/willvar/vista).

Commit or push only when the user explicitly requests that operation. Authorization to edit, build, or test does not authorize a commit or push. Keep all Git operations within the requested scope.
