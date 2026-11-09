# Product behavior and audience

[简体中文](./product.zh-CN.md)

Vista is a general-purpose agent with a browser interface. Users describe a goal, provide relevant paths or attachments, and work with a model that can call tools. The executable contains the Go service, conversation Workers, and embedded Vue interface.

## Audience and design baseline

Vista primarily serves people who can independently use and configure everyday computer or phone software:

| User profile | Typical capabilities |
| --- | --- |
| Proficient everyday use | Manages files and folders, installs software, changes common settings, and searches for solutions. |
| Advanced tool use | Also combines tools, extensions, batch processing, or simple automation and follows instructions to complete unfamiliar configuration. |

Programming, command-line, API, and service-deployment knowledge are not prerequisites.

Use these principles when changing the product:

- Clear defaults and instructions let users with everyday software skills start independently.
- Progressive settings let users configure additional models, skills, and MCP tools by following instructions, without complicating everyday use.
- Explain unfamiliar controls through their purpose, benefits, and costs.
- Automatically updated views have no routine Refresh button. A manual check remains when automatic checks are infrequent and users need an immediate check, such as model catalog updates.
- Describe Vista through tasks and collaboration without assuming it runs on the user's own computer.
- More advanced users retain configuration room. Technical ability and willingness to study configuration are separate; assess desktop and phone use separately.

## Conversation behavior

- First use connects a model provider in Settings. Built-in connections encapsulate provider setup; a custom connection explicitly selects its API format and authentication. Model availability depends on the connected service and account.
- The first sent message creates a conversation and Worker. Open conversations have resident Workers, subject to admission and health checks. Navigating away or closing the browser leaves work running; closing a conversation stops its Worker and managed work but retains history. Deletion removes its stored conversation data.
- The sidebar session menu offers renaming, confirmed closing, and confirmed deletion. Sidebar, conversation-header, and session-manager titles save inline when the editor loses focus.
- Pinned sessions appear first in the sidebar and session manager. The sidebar orders each group by descending creation time, with descending IDs breaking ties; the session manager applies its selected sort within each group.
- The session manager requires confirmation before closing sessions with running tasks or background processes, including batches. Idle sessions close directly from the manager.
- Main-thread execution is sequential per conversation. Other conversations, child tasks, and tools may run concurrently. Messages sent during execution join at the next decision boundary, before the next model request. Cancellation and failure pause undelivered inputs; a fresh send after execution stops can start new work without automatically resuming paused messages.
- Restart restores Workers for open conversations and records unfinished work as interrupted. It does not resume interrupted model requests or replay tool side effects.
- Text, execution details, questions, and usage remain with their originating messages. Execution details appear directly as secondary messages in event order, including child activity, without process or agent disclosure controls. Large tool outputs and child reports are read on demand. During execution, the current action, elapsed time, last activity and checklist stay above the message composer, independently of history scrolling. When execution completes, fails or stops, its final checklist becomes a copyable message body in that round’s history and leaves the composer; a new run starts without the previous plan. The sidebar distinguishes foreground execution, background-only activity, unread replies, pending answers, paused input, Worker unavailability, and failure.
- Viewing an error clears its unread reminder while retaining the session's execution-failed state. Pending questions, paused inputs, and unavailable Workers continue to need attention.
- Temporary model-service failures can retry the same request up to three times without repeating completed tools. Waiting is visible and cancellable, and survives page reloads. Known authentication, quota and protocol errors, and failures after output has started remain terminal. After a failure, Continue execution sends a continuation in the same conversation using the retained history; it preserves unsent drafts. See [request recovery](./architecture.md#model-request-recovery) for eligibility and wait limits.
- A model may finish normally without a final text reply, including after delivering results through tools. Vista completes the usual input and required-task checks and shows that execution ended without text; it does not add an assistant message or treat this as proof that the requested outcome is correct. Request and protocol errors remain failures.
- Automatic model selection offers Fast and Quality modes; manual selection chooses a connection/model and available reasoning setting. Settings changes during execution apply to subsequent runs.
- Automatic conversation titles are enabled by default and can be disabled in Personalization. When enabled, a separate model request generates the first title without tools or an explicit reasoning effort; it may fail independently. When disabled, new conversations display a localized default name followed by their creation time in `YYYYMMDDHHmmss` form, such as “New Session-20261025174143”. The timestamp uses the client’s local time. Existing and manually assigned titles are preserved.

See [architecture](./architecture.md) and [process lifecycle](./process-lifecycle.md) for runtime ownership.

Manage sessions searches and filters the entire saved session list, loading one page of summaries at a time. It defaults to 10 per page, with 20/50/100 available; bulk selection applies to the current page.
Status filters include the total, open, and closed counts; search, filters, and pagination stay visible while the list scrolls.
Click a row’s title or information area to enter the conversation; closed sessions reopen before navigation. Pinning and the row menu remain separate actions; the menu offers renaming, closing, retrying an unavailable Worker, or confirmed deletion.
The list toolbar offers Select and sorting by creation time or recent update, in either direction; newest creation first is the default. Sorting applies to all matching sessions before pagination, with pinned sessions first.
Recent update uses the latest persisted conversation event, including messages, execution records and title changes, or creation time for an empty session; reading, pinning, opening and closing do not change it. Row timestamps show the selected time field.
Select enters bulk selection mode, where clicking a row selects it and the action bar shows operations applicable to the selected sessions.
Changing the search, status filter, sort, page, or page size clears the selection; Done exits selection mode.

## Context and resources

Completed model turns and tool results survive later messages and Worker restarts. Explicit upstream context-overflow evidence triggers compaction and retry of the failed request. The conversation header's compaction icon shows the retained-context estimate on hover; clicking asks for confirmation and explains automatic compaction and its tradeoffs. Manual compaction uses the same checkpoint mechanism while idle and does not send or continue automatically. The estimate is neither cumulative usage nor a model-capacity percentage. See [compaction](./context-compaction.md).

Images use shared original files with conversation-owned references. New attachments or explicit resource reads activate visual input for the next supported request; earlier images are not automatically resent. Closing retains ownership; deleting a conversation releases its links, and fully unreferenced files can be cleaned manually. See [resources](./resources.md).

About Vista provides memory presets and custom total, per-conversation and execution-concurrency limits. It shows measured resident usage; reaching the budget refuses new heavy work, and exceeding a Worker limit interrupts that conversation. These are soft guards rather than an OS memory ceiling; external tool processes use additional memory.

Editing and withdrawing sent messages roll back conversation/model history only. Files, shell effects, and remote changes remain. See [message rollback](./message-revert.md).

## Personalization and extensions

- Global instructions are the user's configuration-root `AGENTS.md`, read at execution start. Filesystem tools automatically check their target paths, and shell tools check their working directory for scoped rules. Operations proceed immediately when no new rules need reading; new or changed rules must be read before the model retries or revises the operation. Nearer rules take precedence within their scope, and explicit current requests take precedence over defaults.
- Memory stores explicit lasting preferences across conversations. Personalization opens Manage memories in a secondary dialog, with short previews, full-text editing, and confirmed deletion. The dialog loads 10 entries per page by default, with 20/50/100 available. Only the main agent can call `memory`, while children inherit the preference snapshot. Deleting an individual conversation does not delete memory.
- The maximum delegation depth defaults to 1 and accepts 0–8. Zero disables child delegation. This is a maximum, not a fixed number of levels the model must create. Required task reports arrive automatically before finalization; waiting parents remain available for user input. Best-effort and detached tasks have different lifetimes.
- System skills are embedded in Vista, visible as read-only entries in settings, and updated with Vista without being extracted to the user directory. The bundled `vista-guide` helps configure and troubleshoot Vista. Users place their own skill folders containing `SKILL.md` in the configuration-root `skills/` directory; the settings list updates automatically. Each execution receives system and enabled user skills' names, descriptions, paths, and sources; full instructions and supporting documents are read on demand. Descriptions follow Agent Skills' limit of 1024 Unicode characters. The skill editor provides a directory tree for existing files: UTF-8 text is editable, raster images can be previewed, and other files show metadata only. Desktop shows the tree beside the file; mobile opens each file from the tree with a Back control. Switching files retains drafts, closing requires confirmation to discard changes, and saving rejects a changed source revision. Embedded system skills remain read-only, and the editor does not create, rename or delete individual files.
- MCP connections supply tools over local stdio or remote Streamable HTTP, with enable/disable controls and connection-scoped credentials. They do not provide MCP resources, prompts, sampling, or elicitation.
- MCP OAuth uses manual callback submission: open or copy the authorization link, complete authorization, and paste the complete callback address into the same Vista dialog. The callback page does not need to load, and no automatic callback listener is started. Attempts expire after five minutes; closing the dialog cancels them. Callback links contain short-lived authorization information and should not be sent in conversations or logs. Codex and Copilot continue to use device-code login.
- MCP and skills settings can import OpenCode v1/v2 configuration through an explicit read, selection, and confirmation. Existing entries are kept. Skill imports copy complete folders, including supporting and binary files; MCP imports save compatible configuration; enabled services connect asynchronously after import. Unsupported entries are shown with reasons. See [import contracts](./architecture.md#providers-and-tools).
- Network Queries configures separate search and page-reading services, order, keys, and initial output lengths. Search returns sources and summaries; page reading retrieves a known public URL through Jina Reader or DeepSeek.

## Configuration editing

Settings and About Vista use JSON Forms for the seven configuration sections below. The agent editor also includes global instructions from `AGENTS.md`. Scalar, choice, object and list fields reuse Vista controls. There is no raw JSON text editor or mode switch. Schema-validated form drafts require an explicit Save changes action; invalid field values block saving. Existing business groups keep their descriptions and figures beside the relevant controls; JSON Forms renders those groups rather than deciding their placement. Search/page-reading service lists show fixed names and only move up/down: they are permutations, not per-row service replacements. Schema and business validation show specific messages and visible invalid borders on the actual fields, not a generic highlighted-fields notice. Personalization retains instructions, memory, delegation with its figure, then automatic titles; group saves submit only their own changed field, and automatic titles keeps its direct toggle behavior. Preview limits count UTF-8 bytes; 0 means unlimited.

When a settings view receives changed values while a draft is active, the form keeps the draft, including invalid field values, and blocks saving until current values are reloaded. Reloading asks for confirmation before discarding the draft. This is local draft protection: only service configuration also checks a server-side SHA-256 revision of the compact `server` section on save, not of the whole file, so changes to unrelated sections do not cause a conflict. Other settings retain their existing save behavior.

Forms contain the editable fields for each setting type, never the complete `config.json` root or `key.json`. Newly entered credentials use masked, write-only form controls; saved credential values are not returned to the form. Provider selection, model discovery, OAuth and imports retain their existing workflow and typed APIs. Schema renderers replace the ordinary and password/header inputs instead of maintaining parallel handwritten forms, preserving masking, blank-to-reuse and explicit-clear behavior.

The configuration root has three fixed user configuration filenames: the main `config.json`, static-credential `key.json`, and optional global `AGENTS.md`. Both JSON files are private `0600` documents in a private configuration root (`0700` where Unix permissions apply). The main requires integer `version: 2`; its seven raw JSON sections are optional. In a fresh empty root, Server startup or an explicit write may initialize the current files; this is initialization, not conversion of old state. An absent section retains its existing defaults or environment fallback. Explicit `null` and empty values remain distinct from absence through storage and credential splitting; the section's typed validator and API still determine valid values and behavior.

| Section in `config.json` | Purpose | Startup migration input only |
| --- | --- | --- |
| `server` | Saved listening port, separate from the running listener | `server.json` |
| `capacity` | Memory budgets and execution-concurrency limits | `capacity.json` |
| `agent` | Delegation depth and automatic conversation titles | `agent-settings.json` |
| `search` | Search/page-reading services, order and output preview limits | `search.json` |
| `userAgent` | HTTP client identity settings | `user-agent.json` |
| `models` | Model connections and model settings | `models.json` |
| `mcp` | MCP server definitions and connection settings | `mcp.json` |

`key.json` requires integer `version: 1`; its `entries` object maps opaque references to `{section, owner, values}` bundles. Managed model and MCP records, or the `search` object, carry `credentialRef` in the main document when they refer to such a bundle. Owners follow stable identities, not display names or array positions; reference/owner spellings are not user-editable identifiers.

| Section | Static fields stored in `key.json` |
| --- | --- |
| `models` | `apiKey`, `accessToken`, `authToken`, `appCode`, `headers` |
| `mcp` | `token`, `clientSecret`, `headers` |
| `search` | `apiKeys`, `jinaApiKey`, `deepseekFetchApiKey` |

Custom header values are treated conservatively: the entire `headers` object moves, not just headers whose names resemble authorization fields. Public `clientId`, routing and identity settings stay in `config.json`. SDK-managed OAuth accounts, refresh tokens and login directories stay separate; this does not migrate OAuth tokens or redesign authorization.

This is file isolation only, not encryption or a system-keyring/cloud-KMS integration. The key file remains plaintext, and private permissions do not protect it from processes running as the same user. The main file can still reveal internal addresses and project/account identifiers, so moving credentials out does not automatically make it safe to share publicly. Backups may retain old plaintext credentials; this does not promise secure erasure or automatic cleanup/recovery of old identities.

`AGENTS.md` remains a separate global-instructions file, included in the agent editor and untouched by migration. The old JSON filenames above are startup migration inputs only, not alternative save locations. One-time conversion runs exclusively at Server startup, after acquiring the instance lock and before application settings load. It accepts those seven files only if no main exists, or upgrades the previous inline-credential `config.json` v1. Under the shared configuration lock, it collects actual source bytes, validates JSON and limits, prepares the conversion, and verifies exact private backups with `SHA256SUMS` under `.config-backup-<time>-<id>/`. Backups include v1 `config.json` and an existing `key.json` as applicable, not just the legacy filenames. It publishes keys before main-file references, verifies the published pair and required durability, then removes only unchanged legacy source originals. The converted v1 main is replaced by the current main, not deleted afterward. The private backup is retained, not automatically restored. See [storage contracts](./architecture.md#unified-configuration-storage).

An existing `config.json` is always the sole source, including for a v1 upgrade: leftover old files are ignored, not merged, even for a missing section. Once the current main is present, later starts only validate the current format and referenced keys; healthy starts do not scan legacy paths, import old data or create another migration backup. Malformed JSON, a missing/unsupported version, or invalid references/missing referenced key entries fail closed, never triggering reimport, fallback to old files, inline secrets or backups, or an automatic reset. Section validation keeps its existing typed behavior. Older binaries must not open the same migrated configuration directory. Account authorization directories and OAuth/login snapshots are untouched; old account identities are never restored automatically.

Normal reads and writes use only the current `config.json` v2 and `key.json` v1. There is no runtime legacy data or credential fallback, and no migration on a library’s first write. Normal access to old or unmigrated data reports “start Vista to migrate” instead of importing or copying old values. The fresh-empty-root initialization described above is the only initialization case for explicit writes; it does not convert old state.

These are runtime user files, separate from repository source and embedded defaults. Two additional files are generated, not editable configuration: `omp-models.json` holds the downloaded model catalog, and `model-catalog-status.json` holds catalog update status and scheduling. They do not count toward the three fixed user files; per-account authorizations, skill folders, SQLite/runtime `state/` and browser preferences also stay separate.

All main/key-document writers share the cross-process `state/config.lock` and update their section in the latest main document, preserving other sections and credentials rather than replacing the root with a public API projection. A credential update first stages new immutable references and key entries while retaining old entries, then publishes the main references, and only then removes previously active references dropped by that committed update. Existing unreferenced entries, including those left by interrupted writes, are not blindly garbage-collected. Each file replacement is atomic, but the two files are not a single atomic transaction. Secret-bearing reads recheck the main snapshot after reading keys and retry publication/pruning races; nonsecret section reads do not load the key file.

Typed settings APIs resolve bundles internally for runtime consumers and keep their existing save and runtime side effects. This storage change does not add global hot reload for direct file edits: agent and client-identity settings retain their existing direct-read boundaries; cached model, MCP, search and capacity settings still require their existing reload/start behavior.

## About Vista and service controls

About Vista presents these sections in order:

1. Brand, version, and introduction.
2. The existing model catalog and client identity controls.
3. Memory and capacity controls.
4. Service configuration for the listening port.
5. The Debug mode toggle.
6. Full reset, visible only in Debug mode.
7. Restart Vista, visible whether or not Debug mode is enabled.
8. Build information.

Service configuration edits the listening port, an integer from 1 to 65535.
Changing it uses **Save and restart** with a separate confirmation showing the impact on active tasks and managed background processes.
Before saving, an independent successor binds the target loopback port and keeps that socket through startup. If reservation fails, the current instance and saved configuration remain unchanged, and the form preserves the draft and displays the error.
A revision conflict aborts the reservation instead of overwriting another edit. Unchanged listening ports can be saved without a restart.
After acceptance, the old tab stays open with the new address and an explicit **Open Vista in a new tab** link. Acceptance is not readiness; a bounded, credential-free readiness image check confirms that the new address serves HTTP. Lost responses and timeouts remain unconfirmed and never automatically repeat a lifecycle mutation.
Port changes retain server-side data. Origin-local browser preferences and drafts remain at the old origin; Vista does not copy browser identity or login state across origins.
Ordinary startup priority remains CLI `--port` > `VISTA_PORT` > saved port > default. The separate **Restart Vista** action keeps the actual listening port rather than applying a pending saved port.

Restart Vista is a regular secondary button and always opens a confirmation dialog.
The dialog shows active-conversation and managed-background-process counts when available; unavailable counts are identified as unknown, not zero.
Its warning explains that all running tasks and managed background processes stop, and interrupted work will not resume or be replayed automatically.
Restart keeps conversations, resources, settings, browser preferences and drafts, the configuration directory and the actual listening port, without opening another browser.
A control process independent of the old Server reuses the CLI restart flow: stop → wait for the instance lock to be released → start.
It loads the current binary at the same executable path, which can activate a newer binary already placed there; it does not download or install updates or switch to another checkout.

After confirmation, the dialog shows indeterminate restart/reconnection progress.
Success requires the shared state SSE connection to be ready with an `instanceId` for a new Server process; HTTP 202 only acknowledges acceptance, and an internal App or SSE epoch change is not confirmation.
A timeout or lost response leaves the result unconfirmed rather than automatically sending another restart request.
Retry connection only reconnects to check the result; it does not send another restart POST or replay interrupted work.

With Debug mode enabled, About Vista offers a confirmed full reset. It stops running tasks, managed background processes and logins, then deletes all conversations, attached resources, connections and credentials, user skills, global instructions, memory and other Vista settings. Embedded system skills remain available. The current browser's Vista preferences and drafts are cleared, the application runtime restarts inside the same Server process, and the page opens the first-use screen at its new address. External files and services, including files previously changed by tools, are not reverted. The listener uses the default port after reset, or the port specified by the process environment; the previous launch port is discarded. Browser preferences in other clients and settings supplied through the process environment are outside this reset.

## Activity and usage

Settings → Usage uses one 7/30/90-day period for daily activity/consumption trends and a weekday/hour activity heatmap, displayed in the viewer's timezone.
An open statistics view also updates at local hour/day boundaries without requiring new activity or usage records.
Each heatmap cell shows mean active minutes for that weekday/hour across observed local dates; inactive occurrences count as zero, while dates before collection and future hours are excluded.
An active minute contains a click, typing, or scrolling; simultaneous interactions across clients count once, while idle pages and background execution do not count.
The total uses upstream-reported input plus output tokens, counting cache and reasoning subsets only once, across conversations, child tasks, compaction, and title generation.
Successful calls without usable reported usage are identified and excluded from the total; failed requests without usable usage are not counted.
The total is shown as an approximate comparison with familiar texts; hover, tap, or focus it for the original token count and a brief explanation. See [usage statistics](./usage-statistics.md) for the reference sources and conversion rules.
Existing retained model-result records are imported once; the page identifies when complete collection began, and does not invent historical interaction activity.
Records are kept for 90 days, matching the longest available view; selecting a shorter period does not shorten retention.
Conversation deletion or withdrawal does not deduct retained consumption; a confirmed statistics reset in Usage or a full Vista reset removes the statistics.

## Current boundaries

Vista binds to loopback and runs tools with the service user's local permissions. There is no per-tool approval or filesystem rollback. Conversations share the real filesystem and have no fixed project directory; tools require explicit target paths. Optimistic checks in file-writing tools do not cover arbitrary shell writes.

Image attachments currently accept PNG, JPEG, WebP, and GIF, up to 50 MiB each. The model can import local images through `read_resource`, including downloaded files and extracted frames. Audio/video input and automatic frame extraction are not supported. There is no built-in LSP, dedicated Git workflow, or terminal UI. Model/protocol metadata and local simulations do not certify every upstream account or model; see [providers](./providers.md) and [verification](./testing.md).
