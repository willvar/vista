# Vista Development Rules

## Product Audience and Tradeoffs

Consult the [audience and design baseline](./docs/product.md#audience-and-design-baseline) before changing features or interactions. Vista primarily serves users who can independently manage files, install software, and adjust common settings; programming, command-line, API, and service-deployment knowledge are not prerequisites. Use clear defaults and explain unfamiliar concepts through their purpose and tradeoffs; expose advanced controls as needed.

Keep product wording neutral about deployment location; do not position Vista as tied to a user's personal computer.

## Documentation

Vista supports its own development. Product documentation serves users; development documentation primarily serves agents. Prefer executable commands, code entry points, and interface/state/data constraints over tutorials and duplicated inventories.

- Keep `README.md` and its Chinese edition as the user entry points. Put maintained product and technical documentation in `docs/`; keep frontend-specific development instructions in `web/README.md`.
- Public documents describe current implementation. Check behavior against source and relevant tests; do not turn intended behavior, catalog metadata, or mock tests into claims of verified live support.
- Store plans, decisions, POCs, acceptance checklists/results, and local diagnostics under ignored `.vista/`, for example `.vista/plans/`, `.vista/decisions/`, and `.vista/acceptance/`. Move lasting implemented contracts into the relevant maintained document.
- Maintain this `AGENTS.md` in English. Other permanent Markdown documents use English in the unsuffixed file and Simplified Chinese in `.zh-CN.md`, with reciprocal language links. Keep both editions and relative links consistent.
- Generated regression fixtures belong with their tests, not in a public reports directory. Do not publish user configuration, credentials, traces, screenshots, or machine-specific measurements.

## Build and Verification

- Set `GOWORK=off` for direct Go commands. `make build` embeds the frontend and uses version `dev`; `make build VERSION=v0.1.0` supplies a release version.
- Non-`dev` builds refresh bundled UA versions from the clients' official npm packages before compiling; any lookup failure stops the build. Use `make update-ua` to refresh separately. The generated snapshot belongs in release source; `UPDATE_UA=0` reuses that snapshot for reproducible or multiple-platform rebuilds. Do not edit `internal/useragent/versions_generated.go` directly.
- After each completed batch of development changes, run `make build` to refresh `bin/vista` before handing the work back. An isolated test binary does not satisfy this requirement. Rebuilding does not authorize restarting the running instance.
- Select relevant checks with `make test <goal>`. Goals are `go`, `frontend`, `e2e`, `integration`, `mcp`, `memory`, and `capacity`. `make test` defaults to `go`; `make test all` runs all seven. See [verification](./docs/testing.md) and the [Makefile](./Makefile).
- Isolate configurations, ports, browser profiles, and test binaries. Do not replace the user's executable or restart their instance as part of testing. Put generated material under `.vista/`.
- Tie verification claims to the source, artifact, environment, and checks actually used. Mock protocol tests do not establish live-provider compatibility; cross-compilation does not establish runtime behavior on the target OS.

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

The formal remote is [willvar/vista](https://github.com/willvar/vista). The former pre-remote exemption for deleting development data and omitting compatibility work no longer applies as a standing rule. Assess persistence and configuration changes against the supported behavior; the historical development rules are retained under `.vista/decisions/`.

Commit or push only when the user explicitly requests that operation. Authorization to edit, build, or test does not authorize a commit or push. Keep all Git operations within the requested scope.
