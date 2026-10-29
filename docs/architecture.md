# Architecture and runtime contracts

[简体中文](./architecture.zh-CN.md)

This document describes the current source. Product tradeoffs are in [product behavior](./product.md); development rules are in [AGENTS.md](../AGENTS.md).

## Modules

| Directory | Responsibility |
| --- | --- |
| `cmd/vista/`, `internal/instance/` | Lifecycle CLI, background service, instance identification and shutdown |
| `internal/server/` | HTTP/SSE, configuration, admission, Worker supervision, and persistence coordination |
| `internal/worker/`, `internal/workerproto/` | Conversation processes, pipe protocol, and persistence acknowledgments |
| `internal/agent/` | Model loop, tool scheduling, child-task joining, and compaction |
| `internal/provider/` | Metadata, protocol requests, streaming, authentication, and overflow detection |
| `internal/modelcatalog/` | Connections, discovery, model selection, and runtime configuration |
| `internal/store/` | SQLite transactions, messages, events, tasks, and output references |
| `internal/resources/` | SHA-256 original files and cross-process publication/read/cleanup locking |
| `internal/tools/`, `internal/mcpclient/` | File, process, web, Skills, and MCP operations |
| `internal/mcphost/` | Shared MCP connections, execution leases, and supervised host process |
| `internal/agentconfig/` | Global instructions and delegation settings |
| `internal/appdirs/`, `internal/platform/` | OS directories, process ownership, browser launch, and metrics |
| `internal/buildinfo/` | Version, executable hash, and Go VCS metadata |
| `web/src/` | Vue UI, conversation state, HTTP/SSE client, and virtual scrolling |

The frontend builds into `internal/server/assets/` and is embedded by Go. Server, Worker, MCP host, and CLI use one executable. Providers run in Go without a separate JavaScript provider process.

## Process and storage ownership

Each open conversation normally owns a resident Worker, subject to admission and failure recovery. The main thread runs sequentially; tools, children, and other conversations may run concurrently. There is no fixed conversation working directory. File and shell tools receive explicit paths.

The Server owns SQLite writes and per-conversation event sequences. Workers send results over local pipes. Boundaries including tool starts, task startup/termination, and checkpoints wait for persistence acknowledgments. Browsers receive persisted events and deduplicate SSE replay by sequence. Model text is chunked; tool and process artifacts are read on demand. See [process lifecycle](./process-lifecycle.md).

[Directory resolution](../internal/appdirs/dirs.go) uses `os.UserConfigDir()/vista`. Settings and credentials, including `models.json`, `mcp.json`, `search.json`, `user-agent.json`, global `AGENTS.md`, `skills/`, `omp-models.json`, and `model-catalog-status.json`, live at this root. SQLite, instance records, locks, logs, outputs, and resource originals live in `state/`. Model catalog files are not under `state/`.

`GET /api/status` exposes version, cached executable SHA-256, Go source revision/dirty state, and the server OS/version. `internal/buildinfo.Version` defaults to `dev`; the Makefile supplies release versions. No build timestamp is injected. The hash is calculated at startup and is not embedded back into the executable.

## Conversation synchronization

`GET /api/sessions/<id>/snapshot` reads the core conversation view and its `throughSequence` boundary in one SQLite transaction.
It includes current execution/pending state, the latest formal reply sequence, bounded recent messages/events, message result metadata, and the latest rollback sequence.
Older root progress outside the recent event page is included separately from its pagination boundary.
Full outputs, execution indexes, feedback, and context estimates load independently and cannot gate the SSE subscription.

SSE `record` frames carry `afterSequence`, the preceding delivered boundary, so a missing frame can be detected without assuming contiguous stored sequences after rollback.
Separate `sync` frames report committed state after initial catch-up, every 15 seconds, and after execution completion or rollback.
Records through each checkpoint are sent first; the checkpoint never advances the browser's applied cursor.

The selected conversation's `web/src/client/conversationSync.ts` owns initialization and serialized recovery.
Stream failure, 45 seconds without received bytes, a coverage/checkpoint mismatch, foreground/connectivity restoration, or manual retry initiates on-demand reconciliation.
Recovery replays at most ten event pages, with an optional inclusive `through` boundary, and rebuilds the core snapshot if replay cannot restore it.
Connection attempts and recovery reads have 10-second timeouts; reconnection backs off from one to 30 seconds, stopping on terminal client errors or closed conversations.
Reopening the selected conversation restores its subscription without clearing the composer state.
Conversation recovery does not submit model/tool requests or periodically poll conversation content.

Replacement preserves drafts, quotes, attachments, model choices, intentional history windows, and accepted messages newer than the snapshot.
Only visibility-based read receipts acknowledge replies.
Synchronization errors have their own retry control and bounded diagnostics in the execution debug dialog; a successful send does not clear them.
Execution-detail rendering is isolated, and Markdown failure falls back to escaped plain text so the formal reply remains available.

## Workspace synchronization

Each page owns one fetch-based `GET /api/events` state stream in addition to the selected conversation's durable event stream.
`internal/server/state_events.go` publishes coalesced topic revisions after committed mutations and asynchronous state transitions.
Changed session lists, runtime status, model inventories, catalog-update status/history, resource-scan status, MCP settings/login outcomes, provider-login outcomes and memories travel as bounded payloads in the same frame.
The workspace reads `GET /api/sessions?open=true`; global session payloads also contain only open summaries and their event boundaries.
Closed conversations contribute aggregate counts in status, without their summaries or history.
The session manager subscribes to session revisions only while mounted and reads `GET /api/sessions` on entry and changes; leaving cancels pending reads and releases that subscription without reconnecting the shared stream.
It replaces its management list as one snapshot, so concurrent close/reopen commits cannot create temporary omissions by mixing open and closed lists from different times.
If the selected conversation disappears from the open list, `GET /api/sessions/<id>/checkpoint` distinguishes closing from deletion without loading archives or conversation history; a closed selected conversation remains readable with its draft preserved.
The unfiltered sessions endpoint remains available for administrative clients.
Login outcomes are matched by attempt ID; device codes and authorization URLs remain attempt-specific HTTP reads.
Newly opened views receive the latest payload for its revision; per-subscriber cursors serialize initial reads with arriving updates and avoid duplicate initialization.
Memory updates preserve unsaved editors and the original text used to detect edit/delete conflicts, including when another client removes the edited entry.
Read receipts, titles, model choices and queue edits push the session list without resending runtime status.
Run/compaction, task/process and Worker lifecycle transitions also push runtime status; model and tool progress stays on the conversation stream.
Successful message and execution-control acknowledgements do not initiate a separate workspace refresh; the state stream reconciles shared views.
Session-list entries and their per-conversation event boundaries come from one SQLite read transaction; newer conversation events and accepted sends take precedence when applying delayed lists.
Global revisions are captured before snapshot reads so concurrent commits retain a later notification.
Each connection and 15-second heartbeat carries the server generation and all current revisions; `web/src/client/workspaceSync.ts` applies changed payloads directly and reads subscribed snapshots when a changed topic has no payload.
Initial and reconnect checkpoints use HTTP snapshot calibration; unchanged heartbeats carry no repeated payload or HTTP reads.
Frames exceeding 120,000 JSON bytes omit their largest payloads until they fit, preserving topic revisions for targeted HTTP recovery within the shared SSE transport's frame limit.
Reconnection or a different server generation reconciles all subscribed views, and returning to the foreground or regaining connectivity reconnects the stream.
Failed reads leave the checkpoint unapplied and retry through stream recovery; unchanged heartbeats do not initiate HTTP reads.
The shared `sse.ts` transport owns connection/idle timeouts, cancellation and reconnect backoff for both stream types.

Initial/recovery snapshots, oversized lists, provider setup catalogs, diagnostic bodies, history, files, and bounded output pages remain ordinary HTTP reads; commands remain ordinary HTTP writes.
Provider-connection mutations rely on state notifications rather than refreshing unrelated workspace snapshots; connection settings and provider setup catalogs have separate reads.
The frontend no longer periodically queries workspace metadata, catalog progress, login status, resource scans, process details or log growth.
Debug subscriptions receive backend metrics every five seconds on the same stream and stop requesting samples while hidden or when debug mode is disabled.
Background writes publish throttled byte-count progress through durable job events; the output viewer refreshes bounded visible/tail blocks while retaining its virtualized cache.
Backend catalog scheduling, model discovery and upstream device-authorization polling retain their own lifecycles.

## Execution and child tasks

Normal sends during execution steer at the next decision boundary; explicitly queued messages wait until the run ends. Cancellation/failure pauses undelivered inputs. A fresh message after execution stops starts independently; it does not resume the old paused queue. Changes to the selected model apply to later runs.

The [delegation registry](../internal/agent/delegation.go) associates stable task IDs with the parent and original run:

| Policy | Finalization behavior |
| --- | --- |
| `required` | Wait for a terminal state, collect persisted reports, and request model synthesis if reports remain uncollected. |
| `best_effort` | Does not block submission; unfinished work is canceled when its parent finalizes. |
| `detached` | Conversation-owned; may outlive the parent execution and cancellation. Reports stay in the original trace without a new formal reply. |

Explicit task cancellation also cancels its descendants. Later runs of the same parent can query or cancel its detached work. Closing the conversation or stopping Vista terminates its managed work.

When a model proposes a tool-free answer, the run loop checks uncollected required children before finishing. Joined reports are structured runtime evidence (`RuntimeData`), with a shared 16 KiB inline preview and references for rereading. The model performs semantic synthesis. A run can retain multiple formal replies at interjection boundaries; it is not limited to one message for its whole lifetime.

The shared `runScope.modelTurns` allocates turn numbers; it is not a model-call budget. Delegation depth is checked, but there is no unified run-wide deadline, total-call budget, or bounded join-generation counter.

Checklists are replacement events scoped to a run, not conversation-global state. New runs start without inheriting the old checklist. Tool-specific validation reports argument errors as tool results. A valid `question` call is main-agent-only and must be alone in its model turn; it persists until answered or superseded. Provider-hosted tools are saved as already executed and are never dispatched as local tools.

## Context continuity

Main model messages and tool results are saved to immutable history files, committed through acknowledged `model_history` references. Later runs merge them with formal inputs in event order and exclude duplicate formal assistant projections. Missing results after interruption become explicit unknown outcomes; restoration does not run tools.

Automatic compaction requires explicit upstream context-overflow evidence. Manual compaction shares summary/checkpoint storage while idle. Catalog windows and retained-token estimates do not trigger it. Checkpoints replace prior history through event and input cutoffs, preserving tool pairing, resource references, and provider replay fields. Summary failure does not replace the active checkpoint. See [context compaction](./context-compaction.md).

Global instructions, preferences, current checklist, and available tool information are rebuilt for execution. Project and skill bodies are read on demand. Model/protocol switches strip incompatible provider-owned replay data while keeping portable text, tool pairs, and resource references.

Reply language is request metadata, inherited by child tasks and refreshed when an interjection supplies a new preference. The shared model-request entry point applies the policy from `internal/replylanguage` to a request-local copy before protocol conversion; conversation, title, and compaction prompts do not define separate language policies. Explicit user language requests override the default; absent a valid preference, the default follows the user's message. Token estimates use the same preparation path, and generated language instructions are not saved in model history or checkpoints.

## Model HTTP diagnostics

Debug mode opts new executions into model HTTP capture through the submitting browser's `vista-debug-mode` cookie.
Queued inputs retain that choice; child tasks inherit it, and title and compaction requests carry their own purpose.
`internal/modeltrace` observes inference after credential refresh and authentication/signing, excluding login and token exchanges.
It records request URLs, headers and consumed body bytes, HTTP status, response headers and consumed response bytes, receive timings, completion/errors, and available parser counters.
Chat diagnostics include the current SSE line/event, observed reasoning fields, fragment count, merged block count, and retained byte count.
The Chat adapter merges compatible reasoning fragments before enforcing its 64 KiB and 256-independent-block retention limits; opaque blocks and incompatible signatures remain separate.

Records are written after a request completes or fails under `state/model-diagnostics/<session>/<run>/`, with credentials redacted before writing.
They include prompt and response content; they are separate from model history and are not sent back to the model.
Capture is bounded to 1 MiB of request body and 2 MiB of response body per model call, 8 HTTP exchanges, and 2,048 receive timings per exchange; truncation is explicit and does not truncate the live request or response.
Binary response bodies use base64, and timing offsets refer to original bytes before redaction.
The store retains at most 256 records within 128 MiB; startup, hourly, and write-time cleanup remove expired records after 7 days.
Deleting a conversation or resetting Vista removes its diagnostic records.

`GET /api/sessions/<session>/runs/<run>/diagnostics` lists saved call summaries; appending `/<record>` reads one record.
Both endpoints check run ownership and disable caching.
The execution's debug dialog reads them independently of conversation SSE, with refresh, request selection, original response display, and copy controls.
After an atomic file save, Workers report the record ID; the Server verifies its small summary before publishing an index update on the shared state stream.
Title requests publish through the same path, and compaction/child requests retain their execution scope.
The transport retains at most 256 recent summaries; revision coverage detects gaps and falls back to the execution's HTTP index.
Only an open Model HTTP tab subscribes to those updates; unrelated executions cause no index/body reads, and new records preserve the selected immutable body.
Earlier uncaptured requests and requests interrupted by process termination cannot be reconstructed.

## Providers and tools

OMP and models.dev supply model metadata. Vista owns routing and authentication; a custom connection's API format is explicit. Both catalog sources update as one staged installation, with the old catalog retained on failure. Existing executions keep their configuration snapshots. See [provider routing](./providers.md).

`websearch` tries configured external providers in saved order; `webfetch` tries Jina Reader and DeepSeek in its separate saved order, defaulting to Jina first. The first search order is shuffled once. Each reader response is fully decoded and staged before selection; empty extracted content, explicit error/verification pages, JS-only placeholders and unsupported response formats fail over to the next configured reader. Short useful content remains valid. There is no built-in origin fetch/converter; local or private downloads use `shell`. Search and fetch DeepSeek keys are independent. Initial output defaults to 50 KiB of UTF-8 text for all results of one search and 50 KiB per page; zero removes that initial truncation. Search source metadata and summaries share one payload budget; artifact/continuation metadata is additional. Saved character-based defaults migrate to the new defaults, unlimited stays unlimited, and custom limits migrate using the maximum UTF-8 capacity, rounded up to KiB. There is no second fixed tool-result cutoff for these network results. Explicit artifact rereads remain byte-paged. These settings live in `search.json` and apply from the next run.

File search applies ancestor/nested `.gitignore` rules, including exceptions, before scanning ignored subtrees. Explicit reads are unaffected. `read_file` uses 1-based `startLine` and `lineCount` (default 200), returns original line numbers and `nextLine`, and streams the full original file for SHA-256 and total line count while spooling selected lines to disk. Oversized lines use saved artifacts without a whole-line RAM allocation. Search includes files larger than 1 MiB, with a shared 32 MiB / 10,000-file / 100-result budget and an explicit incomplete-result flag. `read_tool_output` remains byte-based.

Before `read_file`, `write_file`, `search`, filesystem `read_skill`/`read_resource`, `shell`, or `start_background`, the tool runtime discovers ancestor `AGENTS.md`, `CLAUDE.md`, and `.github/copilot-instructions.md` files from the explicit target path or working directory, ordered outer to nearest.
When every applicable version is already fully read in the model's retained context, or no rules exist, the operation runs immediately without a separate discovery turn.
Otherwise it returns `instructions_required` with `executed: false` and unread file paths, sizes and SHA-256 hashes; the original operation has no effects.
The model reads all pages through `read_project_instruction`, applies their scope and precedence, and retries or revises its operation.
Read coverage is rebuilt from paired tool results in each successful request, separately per task, so a same-turn read cannot authorize a concurrent operation; omitted pages, changed files, and compacted-away text require rereading.
Checks do not cache empty directories or parse shell commands for other target paths; explicit `project_instructions` remains available for cross-directory work.
Embedded `skill://` documents and hash-addressed resources have no filesystem target to check.

MCP settings save `mcp.json`. Stable IDs are separate from display names. One supervised MCP host process owns local stdio servers and remote connections, including MCP-backed network search. It asynchronously preconnects enabled services with bounded concurrency and reuses connections across executions. Each execution retains its configuration versions until it and its detached children finish; edits and disable/delete operations affect subsequent executions, and old versions close when unused. Renaming preserves the connection. Settings connection checks use the same host. Workers exchange tool data directly with the private host endpoint; large results use temporary files outside the main Server. Connection failures reconnect with backoff; interrupted calls with uncertain side effects are never replayed. The MCP service itself may share browser or workspace state across conversations. OAuth publishes credentials after authorization and a successful handshake only if connection configuration remains unchanged. See [frontend](../web/README.md) for UI/state entry points.

System skills are discovered from embedded folders under [`internal/tools/systemskills/`](../internal/tools/systemskills/). Each folder supplies one English `SKILL.md` and any supporting documents; adding a folder does not require per-skill registry or UI branches. `system:<folder>` IDs and `skill://system/<folder>/` read paths are separate from user folder identities. `read_skill` reads embedded Markdown directly, without extracting it. Settings and runtime discovery use the same English metadata and documents, regardless of UI language. User skills remain ordinary folders under the configuration root.

`POST /api/extension-import` accepts `source: "opencode"`, `kind: "mcp" | "skills"`, `action: "preview" | "import"`, optional `configPath`, and optional absolute source `directory` (default: Vista's startup directory). Confirmation requires the returned `fingerprint` and selected `ids`. It rereads source configuration and resolved references; skill fingerprints also include every file's content and executable bits. Changed sources require a new preview. Previews expose no credentials and save nothing. Imports preserve existing destinations and never modify the source.

MCP imports accept both v2 `mcp.servers` and v1 flat server entries; native entries take precedence within a document. They preserve disabled state, resolve local commands/directories, and retain compatible bearer tokens, custom HTTP headers, or OAuth client settings. Header values are redacted from settings reads and can be preserved, replaced, or removed in Advanced settings. Unsupported environment, timeout, and transport options are skipped rather than dropped. Imported OAuth services may need authorization in Vista. Skill imports scan global `skill/` and `skills/` folders and configured `skills` paths (v2 arrays or v1 `paths` / `urls` objects), stage complete folder copies, reserve new destinations, and clean up newly created folders on failure. Remote skill URLs and unsupported file/link layouts are not imported. Neither importer executes source plugins. Enabled MCP services are connected asynchronously by the host after configuration is committed.

## Resources, rollback, and limits

Original attachments live at `state/resources/<hash prefix>/<SHA-256>`. SQLite stores metadata and ownership, not image bytes. The Server streams raw uploads; the Worker verifies originals up to 50 MiB, publishes them and waits for committed session ownership. Image bytes and image-bearing model requests stay in the Worker. New attachments and explicit `read_resource(path|hash)` results activate visual input for the next request only; canonical history and checkpoints keep references. See [resource contracts](./resources.md).

Withdrawal/edit submission stops the conversation's work before truncating history at the selected user message. It does not undo files or other tool effects. Event sequences are not reused. See [message rollback](./message-revert.md).

About Vista exposes memory presets and custom limits through `capacity.json` and `GET`/`PUT /api/memory-config`; shared status SSE includes the saved configuration and measured main/Worker/MCP-host RSS. Defaults are a 2,048 MiB total admission budget, 1,024 MiB per Worker or MCP host and four concurrent executing conversations, including background activity. There is no fixed open-conversation count cap. The [admission implementation](../internal/server/admission.go) rejects new Workers, runs and image work when measured usage reaches the budget or available host memory drops below 128 MiB. Existing Workers and the MCP host receive changed limits; Go soft memory limits target 75% of each Worker limit and 90% of the MCP-host limit, and one-second RSS checks stop processes over their configured limit. These are soft guards, not OS-enforced ceilings or predicted peak usage; transient peaks can occur between samples, and external shell/MCP subprocess memory is outside the admission total. Interrupted work is recorded without automatic replay. Component buffer limits and per-operation timeouts do not establish a general workload-capacity guarantee; see [verification](./testing.md).

The service binds to loopback, validates Host/Origin, and runs tools with local user permissions without per-call approval. HTTP/stream failures preserve redacted provider diagnostics for the message area; actual step errors remain in their steps. The platform layer implements Linux, Windows, and macOS process handling.
