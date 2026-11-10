# Architecture and runtime contracts

[简体中文](./architecture.zh-CN.md)

This document describes the current source. Product tradeoffs are in [product behavior](./product.md); development rules are in [AGENTS.md](../AGENTS.md).

## Modules

| Directory | Responsibility |
| --- | --- |
| `cmd/vista/`, `internal/instance/` | Lifecycle CLI, background service, instance identification and shutdown |
| `internal/server/` | HTTP/SSE, configuration, admission, Worker supervision, and persistence coordination |
| `internal/worker/`, `internal/workerproto/` | Conversation processes, pipe protocol, and persistence acknowledgments |
| `internal/agent/` | Model loop, tool scheduling, child-task completion events, and compaction |
| `internal/provider/` | Metadata, protocol requests, streaming, authentication, and overflow detection |
| `internal/modelcatalog/` | Connections, discovery, model selection, and runtime configuration |
| `internal/store/` | SQLite transactions, messages, events, tasks, and output references |
| `internal/resources/` | SHA-256 original files and cross-process publication/read/cleanup locking |
| `internal/tools/`, `internal/mcpclient/` | File, process, web, Skills, and MCP operations |
| `internal/mcphost/` | Shared MCP connections, execution leases, and supervised host process |
| `internal/agentconfig/` | Global instructions and delegation settings |
| `internal/configstore/` | Private main/credential documents, section updates, migration and read cache |
| `internal/appdirs/`, `internal/platform/` | OS directories, process ownership, browser launch, and metrics |
| `internal/buildinfo/` | Version, executable hash, and Go VCS metadata |
| `web/src/` | Vue UI, conversation state, HTTP/SSE client, and virtual scrolling |

The frontend builds a static ESM application into `internal/server/assets/`, with an HTML entry and content-hashed JS/CSS, and Go embeds the entire directory. Settings, session management, statistics and language catalogs load on demand. Static resources use immutable caching; HTML revalidates with ETag. Vista serves uncompressed resources; response compression belongs to the deployment's reverse proxy. Server, Worker, MCP host, and CLI use one executable. Providers run in Go without a separate JavaScript provider process.

## Process and storage ownership

Each open conversation normally owns a resident Worker, subject to admission and failure recovery. The main thread runs sequentially; tools, children, and other conversations may run concurrently. There is no fixed conversation working directory. File and shell tools receive explicit paths.

The Server owns SQLite writes and per-conversation event sequences. Workers send results over local pipes. Boundaries including tool starts, task startup/termination, and checkpoints wait for persistence acknowledgments. Browsers receive persisted events and deduplicate SSE replay by sequence. Model text is chunked; tool and process artifacts are read on demand. See [process lifecycle](./process-lifecycle.md).

[Directory resolution](../internal/appdirs/dirs.go) uses `os.UserConfigDir()/vista`. The main `config.json`, static-credential `key.json`, global `AGENTS.md`, `skills/`, separate account authorization directories, and generated `omp-models.json`/`model-catalog-status.json` live at this root. SQLite, instance records, locks, logs, outputs, and resource originals live in `state/`. Generated model catalog files remain outside `state/` and are not user settings.

`GET /api/status` exposes version, cached executable SHA-256, Go source revision/dirty state, and the server OS/version. `internal/buildinfo.Version` defaults to `dev`; the Makefile supplies release versions. No build timestamp is injected. The hash is calculated at startup and is not embedded back into the executable.

## Unified configuration storage

[`internal/configstore/`](../internal/configstore/) owns the private main and static-credential documents; typed section validators and settings APIs remain authoritative.

### Documents and credential boundaries

The `config.json` root must be one JSON object with mandatory integer `version: 2` and optional raw JSON sections `server`, `capacity`, `agent`, `search`, `userAgent`, `models`, and `mcp`.
For example, a complete main document containing only a saved port is `{"version":2,"server":{"port":6186}}`; the service editor instead receives only `{"port":6186}`.
An absent section keeps the previous default/environment fallback; explicit `null`, empty objects/arrays and empty values remain distinct from absence through storage and credential splitting, subject to the section's typed validation.

`key.json` is a JSON object with mandatory integer `version: 1` and an `entries` object mapping opaque references to `{section, owner, values}` bundles.
Managed model and MCP records, or the `search` object, hold `credentialRef` in the main file to reference a private bundle.
Resolution checks the bundle's section and stable identity owner; display names and array indexes are not owners.
Reference and owner encodings are internal storage details, not user-editable names.

| Section | Fields moved to bundle `values` |
| --- | --- |
| `models` | `apiKey`, `accessToken`, `authToken`, `appCode`, `headers` |
| `mcp` | `token`, `clientSecret`, `headers` |
| `search` | `apiKeys`, `jinaApiKey`, `deepseekFetchApiKey` |

The entire custom `headers` object moves conservatively, including values of headers not recognized as authentication headers; there is no header-name allowlist for leaving values in the main file.
Public `clientId`, route and identity settings stay in `config.json`.
SDK OAuth account/refresh/login directories remain separate; this storage split does not migrate OAuth tokens or redesign authorization.
The fixed user filenames are `config.json`, `key.json` and optional `AGENTS.md`; caches, generated catalogs, skills, authorization directories and runtime state are outside that count.

Both JSON files are private `0600` regular files in a private configuration root (`0700` where Unix permissions apply).
This is plaintext file isolation only: there is no encryption, system keyring or cloud KMS, and no security boundary against processes running as the same user.
The main file may still disclose internal addresses and project/account identifiers; it is not automatically safe to publish.
JSON Forms receives setting-specific field projections and separate write-only credential-input buffers, never the whole main root, saved secret values or `key.json`; there is no raw JSON text editor.
Typed APIs resolve bundles internally for runtime consumers while retaining existing masking, blank-to-reuse, explicit-clear, validation and save behavior; public/redacted API responses are not credential-storage input.

### Migration and authority

Only Vista Server startup can perform the one-time conversion of old state: it acquires the instance lock, then validates or prepares configuration storage before application settings load.
The seven old filenames in the [section table](./product.md#configuration-editing) are startup migration inputs only when no main file exists.
An existing previous unified `config.json` v1 is the other supported migration source; its inline static fields are split into v2 references and private bundles.
While holding the shared configuration lock, migration collects actual source bytes, validates JSON and size limits, and prepares the conversion; it does not reconstruct sections from redacted/public API projections or resolved defaults.

Before publishing migrated data, `.config-backup-<time>-<id>/` within the private root stores exact source files and `SHA256SUMS`, with directory mode `0700` and file mode `0600`, and verifies the saved bytes and hashes.
The backup covers the actual inputs: legacy JSON files or v1 `config.json`, and an existing `key.json` as applicable, not merely the seven legacy filenames.
`AGENTS.md` and account authorization/OAuth/login data are neither migrated nor overwritten from backups.
On Unix, backup and configuration directories are synchronized, replacement file contents are synced, and the configuration root is synchronized after publication.
Windows uses synced file contents and native replacement semantics, not Unix directory fsync.
Credential publication follows the key-first ordering below; replacing two files is not one atomic transaction.
Startup migration verifies the published current pair and required durability before removing only unchanged legacy source originals. A v1 main is replaced by the current main at the same path, not deleted after publication.
Post-publication durability or source-retirement issues are logged as warnings; uncertain directory durability leaves the old migration inputs in place. The private backup is retained, not automatically restored.

An existing main file is always the sole source, including during a v1 upgrade: leftover legacy files are ignored, never merged, even for a missing section.
Once the current v2 main is present, later starts only validate the current format and referenced keys. Healthy starts do not scan legacy paths, import old data, create another migration backup or retry source cleanup; leftover originals are ignored, not reimported.
Malformed JSON and missing/unsupported versions are errors, never a trigger for reimport, legacy fallback or an automatic reset.
Once references are used, invalid/missing entries or section/owner mismatches fail closed; old inline secrets, legacy files and backups are not a fallback.
Older binaries must not open the same migrated configuration directory.
Old account identities are never automatically restored; neither this migration nor key-entry pruning promises secure erasure or automatic cleanup/recovery of old identities.
Backups may retain plaintext credentials and remain private.

### Publication, reads and runtime boundaries

Normal `Read`/`Write` operations use only current `config.json` v2 and `key.json` v1; they do not migrate or fall back to legacy data, inline credentials or backups. Libraries and first writes do not perform conversion.
Normal access to old or unmigrated data reports “start Vista to migrate” instead of reading and copying old values. An explicit write may initialize the current files in a fresh empty root only; this does not convert old state.

All cooperating main/key-document writers share the cross-process `state/config.lock`, read the latest main document while holding it, and update only their section.
Other sections and credentials are preserved; section writes do not serialize a public settings response over the root.
For a credential-changing update, the ordering is:

1. Stage new immutable credential references and entries in `key.json` first, retaining old entries and never changing an already-published reference's bundle in place.
2. Publish `config.json` with the new references only after those entries are available. This main-file publication commits the update.
3. Only after that commit, remove references that were active in the previous main document and are no longer referenced by the newly committed main document.

Each replacement uses atomic rename, but the pair is not a single atomic transaction and has no journal or separate recovery system.
A write interrupted before main publication can leave unreferenced new entries; one interrupted after publication can leave old entries.
Pre-existing or interrupted unreferenced entries are not blindly garbage-collected; pruning is limited to previously active references dropped by that committed update, not a scan for every unused key.

Secret-bearing section reads take a main snapshot, read the referenced private bundles, then recheck the main snapshot before returning or treating a reference as invalid.
If publication or pruning changed the main snapshot, the read retries rather than mixing generations or treating a pruned old reference as a permanent credential failure.
A stable snapshot with an invalid/missing reference still fails closed.
Nonsecret section reads do not load `key.json` per request, and runtime consumers receive resolved sections rather than learning a second credential-file API.
Each file read verifies current private bytes and SHA-256 before reusing decoded content, including metadata-preserving edits; cache reuse avoids repeated decoding, not file reads, and is not a runtime hot-reload mechanism.

The service editor's SHA-256 revision covers the compact `server` section, not the whole document, so unrelated section changes do not conflict; a missing section remains distinct from explicit `null` or an empty object.
Other typed APIs retain their existing conflict and save semantics rather than gaining whole-file optimistic concurrency.
Confirmed full reset holds the same writer lock while clearing data and establishing a new empty configuration baseline; it retains the live lock file.
After a normal main-file update commits, post-commit durability issues are logged as warnings rather than reported as an unsaved edit; legacy-source cleanup belongs only to startup migration, not normal writes.

Typed API saves keep their existing runtime side effects, including capacity updates to resident processes and the model/MCP/search configuration lifecycle.
Direct file edits do not introduce global hot reload: agent and UA settings retain their existing direct-read boundaries, while cached model, MCP, search and capacity settings still need their existing reload/start behavior.
The Server resolves the actual startup port after migration or current-format validation; priority remains startup CLI `--port` > `VISTA_PORT` > current `server.port` > default.
The CLI may read startup port inputs read-only for validation before a restart; it does not write configuration, perform conversion or promote a saved/default port into a synthetic explicit `--port`.
A changed-port `PUT /api/server-config` requires `confirmRestart: true`. The independent successor reserves `127.0.0.1:<port>` before the section compare-and-swap, waits for the old instance lock after commit, and starts HTTP on that same socket. Ordinary same-port saves do not restart. Typed port-unavailable, configuration-conflict and lifecycle errors remain distinct. Acceptance returns HTTP 202 with the target address and accepting process identity, not readiness. See [port-change restart](./process-lifecycle.md#port-change-restart).
The regular WebUI Restart intentionally keeps the actual port; it does not promote a pending saved/default value into an override.

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
The initial checkpoint supplies a connection-scoped `subscriptionId`; batched `PUT /api/events/subscriptions` updates its topic interests and viewer timezone without reconnecting the stream.
Closing a view releases its interests, and disconnecting releases the entire subscription; optional snapshots are read and pushed only for interested clients.
Skills and model-selection statistics carry revisions only; active views read versioned inventories through HTTP and reuse matching cached results.
A cross-platform filesystem watcher monitors managed skill/resource roots and immediate folders, including atomic replacement, without following symlinks or watching arbitrary workspaces.
Resource/reference commits invalidate storage generations; one coalesced scan serves all interested storage views, and changes during a scan schedule another pass.
Leaving the last storage view cancels its automatic scan; explicit cleanup continues independently.
Visible model-selection statistics also receive hourly invalidation for their rolling observation window, with at most an hour of cache reuse after reopening.
Cleanup remains explicit and confirmed; discovering a file or reference change never deletes resources.
`internal/server/state_events.go` publishes coalesced topic revisions after committed mutations and asynchronous state transitions.
Changed session lists, runtime status, model inventories, catalog-update status/history, resource-scan status, MCP settings/login outcomes, provider-login outcomes and memory counts/limits travel as bounded payloads in the same frame.
The workspace reads `GET /api/sessions?open=true`; global session payloads also contain only open summaries and their event boundaries.
Closed conversations contribute aggregate counts in status, without their summaries or history.
The session manager subscribes to session revisions only while mounted and reads `GET /api/sessions?page=1&pageSize=10` on entry and changes; optional `search` and `open` filters apply before counting and pagination across the entire archive.
It replaces its current page as one snapshot, so concurrent close/reopen commits cannot create temporary omissions by mixing open and closed lists from different times; leaving cancels pending reads and releases its subscription without reconnecting the shared stream.
Both management lists default to 10 entries per page, offer 20/50/100, and read only the requested page; page totals and entries share a SQLite transaction, and out-of-range pages clamp after deletion.
Memory management reads `GET /api/memories?page=1&pageSize=10` only while its secondary dialog is open; global memory events expose counts and limits without memory text.
Paginated responses carry an epoch/revision `version` captured before the read; matching versions reuse the current page, including a memory mutation response, while changed revisions trigger an active view's page read.
If the selected conversation disappears from the open list, `GET /api/sessions/<id>/checkpoint` distinguishes closing from deletion without loading archives or conversation history; a closed selected conversation remains readable with its draft preserved.
Unpaginated session and memory endpoints retain their existing response shapes for administrative clients; the unpaginated session list is limited to 1,000 summaries.
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

## Activity and consumption statistics

`internal/store/statistics.go` owns the UTC-minute activity table and a per-request token ledger without conversation foreign keys; withdrawals and conversation deletion cannot refund already consumed usage.
`provider.WithUsageRecorder` observes API attempts at the common dispatch boundary, including main/child requests, compaction, text/image title generation, and failures with reported partial usage.
Workers emit bounded `token_usage` frames; the Server validates identities, commits the deduplicated record, then advances the `statistics` revision. Main-process titles use the same store directly.
Only input plus output totals are saved; cache-read and reasoning subsets are not added again, and missing usage is nullable rather than an estimated zero.
Failed or canceled attempts without usable usage emit no ledger record; stream failures retain valid usage already received without completing partial output.
Initialization imports retained `model_result` usage once and records coverage timestamps; normal restarts do not reimport new dispatch records.
Retention matches the longest available view of 90 days; initialization and either activity or usage writes clean expired records from both tables.
The frontend coalesces trusted visible input minutes into bounded `POST /api/activity` reports after 15 seconds or on page exit/hiding; idle pages produce no reports, and overlapping clients deduplicate on the UTC-minute key.
Reports contain no key values, message text, or navigation paths and accept at most 120 recent minutes.
`GET /api/statistics?days=30&timezone=Asia%2FHong_Kong` reads bounded 7/30/90-day daily series, weekday/hour activity, totals and coverage in one SQLite read transaction, using IANA timezone data embedded for cross-platform operation.
The `hours` matrix contains mean active minutes per observed local date/hour slot; its denominator includes inactive slots, excludes time before activity collection and future hours, and counts repeated DST hours once per local date/hour while omitting skipped hours.
The global stream carries only the statistics revision; chart history is read only while the statistics view is mounted, with versioned cache reuse, cancellation, stale-response rejection and reconnect recovery, without polling or broadcasting chart history to unrelated clients.
`POST /api/statistics/reset` requires `{"confirm":"reset-statistics"}` and atomically clears both statistics tables, resets coverage timestamps and preserves the one-time import marker before publishing a statistics revision.
Activity reports through the reset minute are ignored to prevent delayed client buffers from restoring cleared activity; usage finishing after reset enters the new ledger.
While a statistics view is subscribed, its connection schedules invalidation at the viewer's next local hour boundary, including local midnight and DST transitions; closing the view stops that clock.
Reopening across an hour boundary invalidates the local chart cache even if no writes occurred while closed.

## Execution and child tasks

Messages sent during execution join at the next decision boundary, before the next model request; there is no deferred-send mode. Cancellation/failure pauses undelivered inputs. A fresh message after execution stops starts independently; it does not automatically resume previously paused messages. Changes to the selected model apply to later runs.

The [delegation registry](../internal/agent/delegation.go) associates stable task IDs with the parent and original run:

| Policy | Finalization behavior |
| --- | --- |
| `required` | Keep the parent available for completion events and user input until reports have been consumed. |
| `best_effort` | Does not block submission; unfinished work is canceled when its parent finalizes. |
| `detached` | Conversation-owned; may outlive the parent execution and cancellation. Terminal results retain their original trace and can request a continuation. |

Explicit task cancellation also cancels its descendants. Later runs of the same parent can cancel its detached work. Closing the conversation or stopping Vista terminates its managed work.

Child reports arrive automatically; there is no model-facing `await_agent` tool.
The parent consumes committed completions at decision boundaries; a new user message wakes an idle parent without canceling its children.
Each tool publishes its terminal UI event immediately instead of waiting for sibling tools.
Tools still executing after a one-second scheduling yield receive exactly one protocol reply with an execution ID; their final result arrives as a runtime event, without a second tool reply or rerunning the operation.
The yield is not an execution timeout; foreground tools retain their existing cancellation and deadline semantics.
Network completions restore their saved model-facing result locally before inference, preserving the user's initial-output setting independently of the bounded notification preview.
Background process exits use the same completion path; output progress does not request inference.

`internal/store/runtime_events.go` owns the durable completion inbox.
Source identities deduplicate reports; the main agent commits delivery together with immutable model history, while child delivery has an idempotent acknowledgment.
Finished child recipients route late background results to their nearest live ancestor.
When no foreground execution is active, an eligible completion may start a run without inventing a user message, after checking questions, pending user input, current model selection and normal admission limits.
Capacity release reconsiders pending continuations; failure, interruption, rollback, session closure and Worker recovery suppress stale wakeups.
A stopped execution is never automatically replayed by restoring the inbox.
Runtime evidence (`RuntimeData`) has bounded previews and saved output references and remains eligible for compaction.
A run can retain multiple formal replies at interjection and completion boundaries.
The shared `runScope.modelTurns` allocates turn numbers; it is not a model-call budget. Delegation depth is checked, but there is no unified run-wide deadline or total-call budget.

Checklists are replacement events scoped to a run, not conversation-global state. New runs start without inheriting the old checklist. Tool-specific validation reports argument errors as tool results. A valid `question` call is main-agent-only and must be alone in its model turn; it persists until answered or superseded. Provider-hosted tools are saved as already executed and are never dispatched as local tools.
`update_checklist` can set `finish: true` when it is the response's sole tool call and the normal response text contains the complete final answer.
After a successful checklist commit, the same turn enters normal completion checks without another model request; failed updates, missing text and mixed calls cannot finish it.
Pending required work, runtime results and new user inputs retain their existing continuation behavior; checklist statuses are never inferred from run completion.

## Context continuity

Main model messages and tool results are saved to immutable history files, committed through acknowledged `model_history` references. Later runs merge them with formal inputs in event order and exclude duplicate formal assistant projections. Missing results after interruption become explicit unknown outcomes; restoration does not run tools.

Automatic compaction requires explicit upstream context-overflow evidence. Manual compaction shares summary/checkpoint storage while idle. Catalog windows and retained-token estimates do not trigger it. Checkpoints replace prior history through event and input cutoffs, preserving tool pairing, resource references, and provider replay fields. Summary failure does not replace the active checkpoint. See [context compaction](./context-compaction.md).

Global instructions, preferences, current checklist, and available tool information are rebuilt for execution. Project and skill bodies are read on demand. Model/protocol switches strip incompatible provider-owned replay data while keeping portable text, tool pairs, and resource references.

The main agent's `memory` tool supports `list`, `add`, `update`, and `remove` for explicit lasting preferences. `update` accepts either the single-edit fields or a `changes` array of existing-memory `update`/`remove` edits, each with a unique ID and exact `previousText` from `list`. The Server validates all targets, final text uniqueness, and final capacity before committing the edits and one `memory_changed` audit event in the same transaction. Retained IDs and creation times are preserved; conflicts or failed writes leave the entire group unchanged. Worker success and a single shared-state notification follow commit. `list` also reports the existing limits: 128 preferences, 4,096 UTF-8 bytes per preference, and 32 KiB total. Encoded changes leave 4 KiB of the 64 KiB Worker frame for metadata; oversized requests are recoverable tool errors. Consolidation decisions stay with the current main agent; there is no separate memory-analysis request or background consolidation job.

Reply language is request metadata, inherited by child tasks and refreshed when an interjection supplies a new preference. The shared model-request entry point applies the policy from `internal/replylanguage` to a request-local copy before protocol conversion; conversation, title, and compaction prompts do not define separate language policies. Explicit user language requests override the default; absent a valid preference, the default follows the user's message. Token estimates use the same preparation path, and generated language instructions are not saved in model history or checkpoints.

## Model request recovery

`internal/provider/model_retry.go` owns one retry budget across protocol adapters, including Bedrock with SDK retries disabled.
An eligible model request makes its initial attempt plus at most three additional attempts, with nominal 2/4/8-second delays and ±20% jitter.
Eligibility covers HTTP 408/429/500/502/503/504/529 and pre-response network errors; known authentication, billing/quota, retired-model, and context-overflow errors are excluded.
A valid `Retry-After-Ms` or `Retry-After` is a minimum wait; a required wait over one minute ends recovery rather than shortening that wait.
Waiting and requests respect cancellation.
The layer repeats the prepared messages, tools and model selection; it does not rerun local tools or choose another model.
Stream/protocol errors and attempts that already emitted text/reasoning or returned tool calls, hosted calls or usage are not automatically replayed.
Context-overflow compaction remains a separate mechanism.

Each HTTP attempt has its own diagnostic record and usage observation; failed attempts without reported usage do not add consumption.
Conversation retries persist bounded `model_retry` events before notifying the durable conversation stream; each additional attempt records a `model_request` with the same logical turn ID.
The snapshot retains root retry progress even when child activity exceeds the recent event page.
The UI coalesces the retry lifecycle into one recovery step per turn and restores waiting state after reload or reconnection.

For the latest failed execution in an open, idle conversation, Continue execution sends an explicit localized continuation through the existing message endpoint, with the currently selected model.
It retains tool history, unsent drafts, quotes and attachments; it neither withdraws the original message nor resends its entire request automatically.
The continuation asks the model to reuse recorded results and verify uncertain effects before repeating an operation.

## Model HTTP diagnostics

Debug mode opts new executions into model HTTP capture through the submitting browser's `vista-debug-mode` cookie.
Pending inputs retain that choice; child tasks inherit it, and title and compaction requests carry their own purpose.
`internal/modeltrace` observes inference after credential refresh and authentication/signing, excluding login and token exchanges.
It records request URLs, headers and consumed body bytes, HTTP status, response headers and consumed response bytes, receive timings, completion/errors, and available parser counters.
Chat diagnostics include the current SSE line/event, observed reasoning fields, fragment count, merged block count, and retained byte count.
The Chat adapter merges compatible reasoning fragments; opaque blocks and incompatible signatures remain separate.
Upstream SSE events, complete model text, and native replay state have no Vista byte or block-count ceiling; protocol validation and required completion events still apply.
Conversation Workers receive and retain that data under the existing soft memory guards. Small IPC frames, UI previews, paged output reads, tool-input budgets, and diagnostic capture limits do not cap the canonical model response.

Records are written after a request completes or fails under `state/model-diagnostics/<session>/<run>/`, with credentials redacted before writing.
They include prompt and response content; they are separate from model history and are not sent back to the model.
Capture is bounded to 1 MiB of request body and 2 MiB of response body per model call, 8 HTTP exchanges, and 2,048 receive timings per exchange; truncation is explicit and does not truncate the live request or response.
Binary response bodies use base64, and timing offsets refer to original bytes before redaction.
The store retains at most 256 records within 128 MiB; startup, hourly, and write-time cleanup remove expired records after 7 days.
Deleting a conversation or resetting Vista removes its diagnostic records.

`GET /api/sessions/<session>/runs/<run>/diagnostics` lists saved call summaries; appending `/<record>` reads one record.
Both endpoints check run ownership and disable caching.
The execution's debug dialog reads them independently of conversation SSE, with request selection, original response display, and copy controls.
After an atomic file save, Workers report the record ID; the Server verifies its small summary before publishing an index update on the shared state stream.
Title requests publish through the same path, and compaction/child requests retain their execution scope.
The transport retains at most 256 recent summaries; revision coverage detects gaps and falls back to the execution's HTTP index.
Only an open Model HTTP tab subscribes to those updates; unrelated executions cause no index/body reads, and new records preserve the selected immutable body.
Earlier uncaptured requests and requests interrupted by process termination cannot be reconstructed.

## Providers and tools

OMP and models.dev supply model metadata. Vista owns routing and authentication; a custom connection's API format is explicit. Both catalog sources update as one staged installation, with the old catalog retained on failure. Existing executions keep their configuration snapshots. See [provider routing](./providers.md).

`websearch` tries configured external providers in saved order; `webfetch` tries Jina Reader and DeepSeek in its separate saved order, defaulting to Jina first. The first search order is shuffled once. Each reader response is fully decoded and staged before selection; empty extracted content, explicit error/verification pages, JS-only placeholders and unsupported response formats fail over to the next configured reader. Short useful content remains valid. There is no built-in origin fetch/converter; local or private downloads use `shell`. Search and fetch DeepSeek keys are independent. Initial output defaults to 50 KiB of UTF-8 text for all results of one search and 50 KiB per page; zero removes that initial truncation. Search source metadata and summaries share one payload budget; artifact/continuation metadata is additional. Saved character-based defaults migrate to the new defaults, unlimited stays unlimited, and custom limits migrate using the maximum UTF-8 capacity, rounded up to KiB. There is no second fixed tool-result cutoff for these network results. Explicit artifact rereads remain byte-paged. Public settings live in `config.json` · `search`, while its static keys resolve from `key.json`; typed settings API saves apply from the next run, while manual file edits retain the reload/start boundary described above.

File search applies ancestor/nested `.gitignore` rules, including exceptions, before scanning ignored subtrees. Explicit reads are unaffected. `read_file` uses 1-based `startLine` and `lineCount` (default 200), returns original line numbers and `nextLine`, and streams the full original file for SHA-256 and total line count while spooling selected lines to disk. Oversized lines use saved artifacts without a whole-line RAM allocation. `find_files` searches path globs (`**` supported) without reading file contents; `search` matches file contents and supports include/exclude globs, literal or regex matching, case folding, surrounding lines, and content/files/count modes.
Search tool results carry one absolute `root` and relative paths; content matches are grouped under `files`, preserving every returned line, line number, context flag and count.
The UI also reads older absolute-path results.
Literal directory prefixes prune unrelated branches; basename globs such as `*.go` still match at every depth.
Inaccessible descendant directories, including subtrees whose `.gitignore` cannot be read, are skipped with `truncated: true`, `skippedDirectories` and up to 20 path/error warnings; an inaccessible requested root still fails.
Both reuse the same ignore-aware traversal; content search scans bounded fragments and rereads long lines through a bounded-memory matcher.
Content search includes files larger than 1 MiB with a shared 32 MiB / 10,000-file scan budget, a 100-hit content/files limit and an explicit incomplete-result flag; count mode counts within the scan budget.
`read_tool_output` remains byte-paged and can search an existing artifact or background output by pattern, returning matching lines, context and byte positions for continuation without rerunning its producer.

Native tool arguments share a 256 KiB complete-JSON budget declared by `internal/toolinput` and reused by provider adapters and runtime validation.
`delegate.task` accepts up to the 8 KiB prompt budget; its schema declares UTF-8 byte counting and suggests referencing files for longer supporting material.
`shell` and `start_background` have no separate 4 KiB command cutoff; their schemas disclose the complete-JSON budget.
Runtime input-size errors report `executed: false`, the field, actual and maximum bytes, and a recovery hint before tool side effects.

`write_file` captures bounded original text during its locked SHA-256 verification and generates a unified diff only after the atomic replacement succeeds.
The immutable diff is saved as a conversation artifact; model context and event previews carry its reference rather than the changed file contents.
New files, replacements, empty files and unchanged content have explicit presentations; originals above 1 MiB omit the diff without blocking a valid replacement.
Failure to save this optional presentation does not turn an already committed write into a tool failure.
`apply_patch` accepts standard unified or Git text diffs for every model, using `go-gitdiff` rather than a model-specific patch dialect.
It validates paths, UTF-8 text, every hunk and expected SHA-256 before any project write; new destinations require `missing`.
Sorted cross-Worker locks are shared with `write_file`, followed by a second hash check and atomic replacement per destination.
Creation, deletion, Git rename/copy and executable-mode changes are supported; binary patches and nonregular file modes are rejected.
A multi-file patch is not one filesystem transaction: an I/O failure reports committed diffs plus distinct applied/unapplied paths, including a rename whose destination was written but source removal failed.
Shell and MCP file side effects have no automatic diff; Vista does not snapshot their working directories.

Before `read_file`, `write_file`, `apply_patch`, `find_files`, `search`, filesystem `read_skill`/`read_resource`, `shell`, or `start_background`, the tool runtime discovers ancestor `AGENTS.md`, `CLAUDE.md`, and `.github/copilot-instructions.md` files from the explicit target path or working directory, ordered outer to nearest.
When every applicable version is already fully read in the model's retained context, or no rules exist, the operation runs immediately without a separate discovery turn.
Otherwise it returns `instructions_required` with `executed: false`, unread file paths, sizes, SHA-256 hashes and rule text pages fitted to the complete tool-result budget; the original operation has no effects.
The model applies the supplied text and reads only missing pages through `read_project_instruction` before retrying or revising its operation.
Read coverage is rebuilt from actual paired tool results and their runtime completions in each successful request, separately per task, so a same-turn read cannot authorize a concurrent operation; discovery metadata, previews, omitted pages, changed files and compacted-away text cannot authorize work.
Complete retained text must match its recorded SHA-256 before it can authorize an operation.
Asynchronous rule results restore their complete supplied text from saved output before the model consumes them.
Children inherit the parent's actual fully acknowledged rule text, paths, scope and content hashes; inherited read flags alone never authorize work.
Inherited text is retained through compaction while current; target discovery still detects new or changed rules.
Patch checks cover all affected paths.
Discovery reuses common directories only within one operation; later operations check for new or changed rules. It does not parse shell commands for other target paths; explicit `project_instructions` remains available for cross-directory work.
Embedded `skill://` documents and hash-addressed resources have no filesystem target to check.

MCP public definitions save the `mcp` section in `config.json`; static credentials resolve internally from `key.json`. Stable IDs are separate from display names. One supervised MCP host process owns local stdio servers and remote connections, including MCP-backed network search. It asynchronously preconnects enabled services with bounded concurrency and reuses connections across executions. Each execution retains its configuration versions until it and its detached children finish; edits and disable/delete operations affect subsequent executions, and old versions close when unused. Renaming preserves the connection. Settings connection checks use the same host. Workers exchange tool data directly with the private host endpoint; large results use temporary files outside the main Server. Connection failures reconnect with backoff; interrupted calls with uncertain side effects are never replayed. The MCP service itself may share browser or workspace state across conversations. OAuth publishes credentials after authorization and a successful handshake only if connection configuration remains unchanged. See [frontend](../web/README.md) for UI/state entry points.

MCP OAuth uses the SDK's authorization-code and PKCE flow without a callback listener (`internal/mcpclient/oauth.go`).
The redirect URI is `http://127.0.0.1:5186/callback`, independent of Vista's listener port; it identifies the authorization response and does not need to load in the browser.
The authorization server must accept that redirect URI and return the response in its query parameters.
Users open the authorization link, then paste the complete resulting address into Vista, even if the browser reports that the callback page cannot be reached.
`POST /api/mcp-auth/<id>` accepts `{"callbackUrl":"<complete callback URL>"}` in a bounded JSON body; the URL is limited to 8192 bytes, parsed rather than fetched, and checked against the attempt's redirect address and state before exchange.
Malformed, duplicate-parameter, wrong-address or wrong-state input returns 400 without consuming the pending attempt; a valid OAuth denial response terminates the attempt without exchanging a code.
An accepted submission returns the attempt in `exchanging` state with its authorization URL removed; submissions outside `pending`, repeated submissions and ended attempts return 409.
SDK issuer and PKCE checks remain in force; the five-minute deadline covers authorization and connection setup, and cancellation or connection changes prevent late credentials from being published.
`GET` reads one attempt and `DELETE` cancels it, including while exchanging credentials.
Authorization URLs remain attempt-specific reads; pasted responses are not returned or added to shared state, and shared login updates carry only ID, status and sanitized error information.

System skills are discovered from embedded folders under [`internal/tools/systemskills/`](../internal/tools/systemskills/). Each folder supplies one English `SKILL.md` and any supporting documents; adding a folder does not require per-skill registry or UI branches. `system:<folder>` IDs and `skill://system/<folder>/` read paths are separate from user folder identities. `read_skill` reads embedded Markdown directly, without extracting it. Settings and runtime discovery use the same English metadata and documents, regardless of UI language. User skills remain ordinary folders under the configuration root.

`POST /api/extension-import` accepts `source: "opencode"`, `kind: "mcp" | "skills"`, `action: "preview" | "import"`, optional `configPath`, and optional absolute source `directory` (default: Vista's startup directory). Confirmation requires the returned `fingerprint` and selected `ids`. It rereads source configuration and resolved references; skill fingerprints also include every file's content and executable bits. Changed sources require a new preview. Previews expose no credentials and save nothing. Imports preserve existing destinations and never modify the source.

MCP imports accept both v2 `mcp.servers` and v1 flat server entries; native entries take precedence within a document. They preserve disabled state, resolve local commands/directories, and retain compatible bearer tokens, custom HTTP headers, or OAuth client settings. Header values are redacted from settings reads and can be preserved, replaced, or removed in Advanced settings. Unsupported environment, timeout, and transport options are skipped rather than dropped. Imported OAuth services may need authorization in Vista. Skill imports scan global `skill/` and `skills/` folders and configured `skills` paths (v2 arrays or v1 `paths` / `urls` objects), stage complete folder copies, reserve new destinations, and clean up newly created folders on failure. Remote skill URLs and unsupported file/link layouts are not imported. Neither importer executes source plugins. Enabled MCP services are connected asynchronously by the host after configuration is committed.

## Resources, rollback, and limits

Original attachments live at `state/resources/<hash prefix>/<SHA-256>`. SQLite stores metadata and ownership, not image bytes. The Server streams raw uploads; the Worker verifies originals up to 50 MiB, publishes them and waits for committed session ownership. Image bytes and image-bearing model requests stay in the Worker. New attachments and explicit `read_resource(path|hash)` results activate visual input for the next request only; canonical history and checkpoints keep references. See [resource contracts](./resources.md).

Withdrawal/edit submission stops the conversation's work before truncating history at the selected user message. It does not undo files or other tool effects. Event sequences are not reused. See [message rollback](./message-revert.md).

About Vista exposes memory presets and custom limits through `config.json` · `capacity` and `GET`/`PUT /api/memory-config`; shared status SSE includes the saved configuration and measured main/Worker/MCP-host RSS. Defaults are a 2,048 MiB total admission budget, 1,024 MiB per Worker or MCP host and four concurrent executing conversations, including background activity. There is no fixed open-conversation count cap. The [admission implementation](../internal/server/admission.go) rejects new Workers, runs and image work when measured usage reaches the budget or available host memory drops below 128 MiB. Existing Workers and the MCP host receive changed limits; Go soft memory limits target 75% of each Worker limit and 90% of the MCP-host limit, and one-second RSS checks stop processes over their configured limit. These are soft guards, not OS-enforced ceilings or predicted peak usage; transient peaks can occur between samples, and external shell/MCP subprocess memory is outside the admission total. Interrupted work is recorded without automatic replay. Component buffer limits and per-operation timeouts do not establish a general workload-capacity guarantee; see [verification](./testing.md).

The service binds to loopback, validates Host/Origin, and runs tools with local user permissions without per-call approval. HTTP/stream failures preserve redacted provider diagnostics for the message area; actual step errors remain in their steps. The platform layer implements Linux, Windows, and macOS process handling.

## Instruction pages and shell evidence

`internal/tools/output.go` defines the shared 50 KiB / 2,000-line model output budget for ordinary tools.
`read_project_instruction` and `read_skill` fit the complete serialized page within that budget, including JSON escaping, path, SHA-256 and `nextOffset`; they do not have a separate small page size.
Complete UTF-8 byte coverage and the full-source hash still determine whether project rules have been read.
UI event previews and saved full output remain separate from the model-facing page.

Foreground `shell` returns short output in full and shares an 8 KiB source-byte preview between the beginning and end of long output.
`previewBytes` and `nextOffset` describe the head; `tail.offset`, `tail.nextOffset` and `tail.content` identify the final excerpt in the original artifact.
The omitted middle begins at `nextOffset` and ends at `tail.offset`; `read_tool_output` can retrieve it without rerunning the command.
These source-bounded shell results bypass a second generic model preview, retaining the tail and byte-range metadata even for many short lines or JSON escapes.
The UI separates the excerpts with an ellipsis, and exit status, cancellation, timeout and artifact retention keep their existing semantics.
