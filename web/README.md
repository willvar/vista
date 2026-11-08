# Frontend development

[简体中文](./README.zh-CN.md)

## Entry points

Use an isolated Vista backend. In `web/`, install locked dependencies, set its loopback address, and start Vite:

```sh
npm ci
VISTA_DEV_BACKEND=http://127.0.0.1:5186 npm run dev
```

Use the backend's actual port. The dev server listens on `127.0.0.1`; `/api` streams through the proxy. Only the dev page's own Origin is rewritten. Without `VISTA_DEV_BACKEND`, API requests return 503. Browser clients call Vista APIs, not upstream model providers.

| Command | Purpose |
| --- | --- |
| `npm run test:unit` | Node test runner for client, state, composables, and i18n |
| `npm run typecheck` / `npm run check` | Vue/TypeScript checking, including Vite and E2E configuration |
| `npm run generate:i18n` | Generate Chinese catalogs and backend reply-language names |
| `npm run check:i18n` | Verify generation, keys, placeholders, and message syntax |
| `npm run build` | Generate/validate translations and build embedded `app.js`/`vista-web.css` |
| `npm run test:e2e` | Playwright projects, requiring `VISTA_CHROME` |

From the repository root, `make test frontend` runs unit/types/build and `make test e2e` builds its isolated application and runs browser checks. General requirements and evidence limits are in [verification](../docs/testing.md).

## State and UI boundaries

| Layer | Entry points |
| --- | --- |
| HTTP/SSE client | `src/client/types.ts`, `vista.ts`, `sse.ts` |
| Global state synchronization | `src/client/workspaceSync.ts` |
| Conversation initialization/recovery | `src/client/conversationSync.ts` |
| Event state | `src/state/conversation.ts`, `workspace.ts`, `runTrace.ts` |
| Conversation loading/sending | `src/composables/useConversation.ts` |
| Shared workspace, models, state subscriptions | `src/composables/useWorkspace.ts` |
| Provider settings / historical runs | `src/composables/useProviderSettings.ts`, `useRunHistory.ts` |
| Shared welcome/conversation input | `src/ConversationSender.vue` |
| Context estimate / compaction | `src/ContextUsage.vue`, `ContextStatus.vue` |
| Routes and modal layout | `src/pages/`, `src/WorkspaceModal.vue`, `src/App.vue` |

Settings and the manager overlay the conversation through named route views; opening them keeps SSE active. Mobile settings uses a category page, then the selected settings page; Back returns through that hierarchy. Desktop retains the settings sidebar.

`ui/Modal.vue` owns the Ark dialog lifecycle, focus, mask and footer; `ui/ModalHeader.vue` provides the shared accessible title, action area and close control. Use the `header-actions` slot for actions beside Close; keep the `title` slot limited to title content. `WorkspaceModal.vue` reuses this header inside its routed frame and owns mobile Back and route-exit motion. Pages supply titles, labels and actions rather than rebuilding modal headers. `closeDisabled` keeps the close control visible but disabled; `closable: false` hides it. Full-screen editing places its explicit exit action in the same action slot.

Conversation switches abort old requests and reject late callbacks by generation. Event sequences deduplicate SSE replay and reconcile snapshots. A stale snapshot must not replace newer titles, execution, or pending-answer state.

Core initialization uses a consistent `/snapshot`; auxiliary failures do not block streaming.
SSE record coverage and separate checkpoints verify applied progress without advancing the cursor.
Stream faults, inactivity, foreground/online events, and manual retry share bounded replay and snapshot recovery, preserving composer state and older-history windows.
Recovery only reads persisted data; it does not resend messages or invoke models/tools, and adds no periodic conversation-content polling.

The workspace owns one additional fetch-based SSE connection for shared state changes.
Subscribe through `workspace.stateSync`; components do not open independent state streams or periodically request status.
Its connection-scoped subscription ID allows batched `PUT /api/events/subscriptions` interest/timezone updates without reopening the stream or reloading workspace metadata.
`useStateQuery.ts` keeps skill inventories and model-selection statistics on revision-driven, cached, cancellable HTTP reads; list updates never replace skill-editor drafts. `SkillEditor.vue` uses `ui/Tree.vue` for existing files, caches drafts per path, saves the selected UTF-8 file with its SHA-256 revision, and confirms discarding drafts. Shared skill revisions update only tree metadata while it is open; file bodies, raster previews and downloads are separate on-demand reads. System skills use the same viewer read-only. Mobile navigates between the tree and file inside the same modal.
The server coalesces storage scans for visible storage views and schedules statistics hour/day invalidation in each subscribed viewer's timezone.
Routine Refresh controls are absent from automatically updated views; catalog checks, import reads, authorization, cleanup and error recovery retain explicit actions.
Changed sessions/status, models, catalog progress/history, storage status, MCP settings, login outcomes and memory counts/limits arrive as bounded state-frame payloads.
The workspace session list contains open summaries only; archived counts come from status.
The manager owns its paginated summary read and session-revision subscription, cancelling both on unmount; search/filter/count and the selected sort run before database pagination, without the unpaginated endpoint's 1,000-summary limit.
`usePagedList.ts` cancels superseded page reads, deduplicates concurrent requests and compares epoch/revision versions before rereading on a state notification; `ListPagination.vue` provides the shared page and 10/20/50/100 page-size controls.
Replace the current page as one snapshot, keep active title editors mounted, and scope bulk selection to that page; do not reopen the global stream or refresh unrelated endpoints.
`SessionManagerPage.vue` keeps the search/status controls, sorting and selection toolbar, and pagination outside the scrolling list; status filters reuse workspace counts without extra reads. The toolbar sorts by creation time or recent update in either direction, defaulting to newest creation first; pins remain first. Paginated `/sessions` requests accept the whitelisted `sort` values `created-desc`, `created-asc`, `updated-desc`, and `updated-asc`. Recent update derives from the last persisted conversation event, falling back to creation time; read/pin/open/close actions do not affect it. Only pages sorted by update include `updatedAt`; sidebar reads and shared summaries keep their existing scope and order. Each title/information area enters the conversation, reopening a closed session before navigation and showing a local error if reopening fails. Selection mode uses native checkbox hit areas covering each row, while inline renaming reuses `InlineSessionTitle.vue` and holds the displayed rows until editing ends. Search, filter, sort, page, and page-size changes clear the selection.
`BatchOperationModal.vue` is the shared secondary dialog for batch confirmation, progress and per-item failures; it accepts operation labels, counts and results without owning requests or domain data. It blocks dismissal while running, closes after success, and keeps failures available for review. Bulk session actions retain the displayed rows, pagination, counts and Worker indicators behind this dialog; SSE and underlying page state keep synchronizing. Read the current page after the final mutation before releasing the display snapshot, including after partial failures.
Connection, MCP and skill imports use its confirmation slot for source selection, then show indeterminate progress for the existing single import request; failures return to the preview for correction or retry.
Resource cleanup uses the same dialog and displays actual deletion counts from shared state updates; an HTTP 202 acknowledgement starts progress, and the final pushed storage state ends it.
Use determinate progress only when completed and total item counts are known; do not invent per-item progress or split an atomic request just to animate a bar.
The visible fill transitions between completed counts and respects reduced motion; native progress retains the accessible values.
Show failure results only for failed entries or a batch-wide `error`; `failureSummary` is a label, not a failure flag. Keep successful progress content stable through the dialog's exit animation.
HTTP initialization and SSE updates must have the same view scope; check response contents and bytes alongside request counts.
Subscriptions initialize from the latest matching payload and serialize their own reconciliation; components must not add a separate mount-time read for the same subscription.
Initial/reconnect calibration, missing or oversized payloads and bounded content pages use ordinary HTTP.
Shared login updates contain outcomes only; authorization links/codes remain attempt-specific reads.
Personalization opens memory management in a secondary modal with paginated bounded previews and full-text editing in the same modal. Its component subscribes and reads its requested page only while open; memory state frames carry counts and limits without text. Paginated mutation responses update the list without an extra echo read. Remote updates preserve active drafts and compare-and-swap edit/delete targets.
`StatisticsSettings.vue` uses `useStatistics.ts` to read/cache bounded 7/30/90-day chart history only while mounted; the shared stream carries statistics revisions rather than chart payloads. Period changes and reconnects reject cancelled/stale reads. `client/activity.ts` batches actual visible input minutes; it never reports idle presence or reads statistics from other views.
The visible Model HTTP tab consumes saved-record summaries and revision coverage from the same stream; bodies remain on demand and new records preserve the current selection.
Apply session-list payloads against their saved per-conversation event boundaries so delayed global frames cannot undo newer conversation state or send acknowledgements.
Unchanged checkpoints initiate no reads or repeated state payloads.
Apply successful command acknowledgements locally; let topic notifications reconcile shared views instead of adding a second workspace refresh.
Session metadata changes push only the list; runtime lifecycle changes also push status, while model/tool progress stays on the conversation stream.
Global reconnect reconciles current subscribed state, while conversation reconnect retains durable cursor replay.
Metrics are pushed only for debug subscriptions; local elapsed-time clocks are independent of network synchronization.
`RenderGuard.vue` isolates execution-detail failures; `MessageMarkdown.vue` falls back to plain text.
See [synchronization contracts](../docs/architecture.md#conversation-synchronization) for timeouts and ownership.

Main model text is a message-area row, distinct from execution details. `main-outputs` indexes history; visible rows load full model text. Formal replies replace the corresponding row by `outputTurn` rather than duplicating it. Reasoning, tools and child activity appear chronologically inside always-visible, visually secondary execution messages. `RunTrace.vue` and `TraceSteps.vue` share one chronological virtual list instead of separate folded agent lists. Tool previews render immediately; explicit full-result and child-report actions retain bounded, cached reads. A call count or whole-run failure alone does not create an empty execution panel; whole-run diagnostics appear in the message area.

`ConversationExecution.vue` renders the current action, elapsed time and latest activity above the composer. Its local display clock uses run timestamps from the consistent conversation snapshot and durable events, with no polling; child activity updates the last-activity clock without replacing the main action.

`ConversationChecklist.vue` renders the running task's latest checklist above the composer, outside the virtualized message list. Conversation state retains that replacement independently of its recent event window; initial/recovery snapshots include the latest root checklist even when older than the recent page. Completion, failure or interruption removes it from the composer and displays one copyable final checklist body after that run's reply, including runs without final text. Message pages project the latest root `checklist` at `run_finished`; the two bodies share one paging cursor, without adding canonical model history or another persisted event. Reuse the conversation stream without another overview read or polling. New runs start without the old plan; archived plans keep their original statuses, with unfinished work displayed as incomplete/interrupted rather than done.

Message rows and long traces use virtual rendering and measured heights. Full-text caches are evicted outside their windows; returning to history reloads it. `useFullOutput` shares a bounded cache of 32 tool outputs and roughly 8 MiB of text; cache bounds do not truncate the currently expanded content.

A stable text selection offers quoted follow-up after 350 ms. Dragging/selection changes, copy, scroll, or Escape dismiss it. Quote drafts are conversation-scoped and sent as Markdown with the question through the normal user-input path, including steering/queueing. The combined message still has the 8 KiB input limit; failure retains the full draft. Editing and withdrawal share the [conversation rollback contract](../docs/message-revert.md).

`src/ui/` contains Vista's presentation components. Ark UI supplies interaction and accessibility primitives for dialogs, selections, menus, toggles, sliders, pagination, collapsible sections, and the workspace splitter. Basic buttons, text fields, message bubbles, and the composer use native elements. Conversation state, rendering windows, and send behavior remain in Vista's existing state and composable layers. The standalone `@antdv-next/icons` package supplies icons.

`src/ui/appearance.ts` stores `vista-appearance` and syncs same-origin tabs. Semantic colors and control dimensions live in `src/ui/tokens.css`; shared component styles live in `src/ui/components.css`. Startup applies theme before styles. Motion follows `prefers-reduced-motion`, including component motion, CSS transitions/animations, and JavaScript smooth scrolling.

Keyboard navigation stays enabled. Tab moves focus unless an input consumes it for completion; preserve text editing, IME, send shortcuts and Escape. Enter submits synchronously when it is a normal keypress; active composition, `isComposing` and key code 229 prevent candidate-confirmation sends. There is no post-composition time window for Enter. The draft clears after Vista accepts the request, not after upstream model success.
Use `:focus-visible` for navigation indicators; pointer focus alone must not draw a keyboard ring.
`AppProvider` tracks Ark UI's interaction modality; focus outlines use `--focus-outline` so mouse/touch focus restoration stays quiet while keyboard and assistive navigation retain their indicators.

Text actions use the shared `Button` at its default 36px height, including list actions and confirmation popovers; do not use `small` or shrink them through local padding, height, or font overrides. Icon buttons use `.icon-button` and the shared `--button-icon-size`: 32px on desktop, 44px at widths up to 830px or with a coarse pointer. Send controls, message actions, image-preview dismissal, and sidebar navigation retain their dedicated layouts.

Empty views use `src/ui/Empty.vue`, with one shared illustration and spacing and typography in `.vista-empty-state`; narrow sidebars use `.vista-empty-state-compact`. Keep loading and errors separate from empty results, preserve context-specific descriptions, and omit pagination when no results exist.

`TraceToolResult.vue` renders `write_file` artifacts through `FileDiffOutput.vue`.
The diff records the committed write, not current filesystem contents; `RunToolOutput.vue` loads it when the full result is requested through the existing bounded output cache.
Unified hunks retain old/new line numbers, insertion/deletion markers and escaped text; fixed-height virtual rows bound DOM size and horizontal scrolling contains long lines.
Keep creation, unchanged-content and unavailable-diff states distinct, and preserve metadata-only results from older histories.

## Shared configuration editor

`src/SchemaForm.vue` owns the form-only draft and JSON Forms/Ajv validation. There is no raw JSON editor or mode switch. Required-value markers and `aria-required` describe mandatory input, not mere object-property presence: empty-allowed strings and false-capable switches are not marked, while the schema still validates their types and property presence. Renderers in `src/schema/` map scalars, enums, objects, ordered lists and write-only password inputs to Vista controls. Invalid field values stay local and block saving. Parent views own explicit save actions and typed API requests. Capacity presets are a separate schema-driven draft selector: choosing one fills all three numeric limits, never saves automatically or persists a preset ID, and manual values retain a Custom state. Business pages own section order and adjacent explanatory figures; UI schemas declare controls within each group. UI-schema enum controls can provide `enumGroups` for native select groups and `enumFullLabels` for unambiguous collapsed labels; the renderer stays independent of service names and keeps the underlying enum values. Fixed service priority lists use `reorderOnly`, never mutable per-row enum choices. Draft equality ignores object-key order but never sorts arrays. Ajv messages are localized by keyword; path-addressed business issues join JSON Forms additional errors so the actual control gets an error message, ARIA linkage and visible invalid border. Shared field before/after slots keep explanatory content adjacent without teaching renderers business field names.

Adapters expose editable setting fields and separate write-only credential-input drafts for the seven sections of `config.json` (`version: 2`); the agent editor also includes separate global `AGENTS.md` instructions. Static credentials live in `key.json` (`version: 1`), giving three fixed user filenames, not seven JSON files plus instructions. Its `entries` map opaque references to `{section, owner, values}` bundles; managed model/MCP records or the search object carry `credentialRef` in the main document. Owners follow stable identities, not display names or list positions; do not construct owner strings in UI adapters. See [configuration editing](../docs/product.md#configuration-editing) for section purposes, credential fields and migration inputs. Repository schemas and adapters are source code, not runtime configuration files; missing sections keep their existing defaults or environment fallback, and explicit empty/`null` values retain their typed meaning.

Form drafts never contain a complete `config.json`, its `version: 2`/section wrapper, or `key.json`. Credential-input drafts contain blank saved-secret placeholders and explicitly entered values only; write-only renderers mask them, and existing typed adapters retain reuse/clear rules. For service settings, the draft is `{"port":6186}`, stored in `config.json` · `server`, not `{"version":2,"server":{"port":6186}}`. Static model fields `apiKey`/`accessToken`/`authToken`/`appCode`/`headers`, MCP `token`/`clientSecret`/`headers`, and search `apiKeys`/`jinaApiKey`/`deepseekFetchApiKey` belong to private bundles. Entire custom `headers` objects move conservatively, not just recognized authorization headers. Public `clientId`, routing and identity settings stay in the main file.

Both JSON files remain private `0600` files in a `0700` Unix configuration root. This is plaintext file isolation, not encryption, a system keyring, cloud KMS or a same-user process security boundary. The main file can still reveal internal addresses and project/account identifiers and is not automatically safe to share. `AGENTS.md` stays separate from JSON. Skills, SDK account/refresh/login authorization data, generated catalogs and SQLite/runtime state are outside the three-file count; authorization is not redesigned and OAuth/login tokens are not migrated. Startup migration does not overwrite instructions or restore older account identities.

One-time conversion belongs only to Server startup, after acquiring the instance lock and before application settings load: it accepts the seven legacy JSON files only when no main exists, or upgrades the previous inline unified v1. It validates and prepares the conversion, verifies private exact backups with `SHA256SUMS` covering actual sources (including v1 main/existing key files as applicable), publishes keys before main references, verifies the published pair and required durability, then removes only unchanged legacy source originals. The private backup is retained, not automatically restored. A current main makes later starts validation-only: healthy starts do not scan legacy paths, import old data or create another migration backup; leftover old files are ignored even for missing sections.

Normal reads and writes use only current `config.json` v2 and `key.json` v1, without legacy data or credential fallback. Libraries and first writes do not migrate; normal access to old or unmigrated data reports “start Vista to migrate” instead of copying old values. Explicit writes in a fresh empty root may initialize the current files, but do not convert old state.

Provider selection, model discovery, OAuth, imports and private credential controls stay outside the JSON draft. Existing typed APIs resolve private bundles internally; keep their current masking, blank-to-reuse and explicit-clear semantics. Never expose saved credentials or copy redacted GET password/header values back into files. Saving uses those typed APIs, not an arbitrary filesystem-write endpoint or a credential-file editor. Preview limits are UTF-8 byte counts, with 0 meaning unlimited.

Parent-value changes preserve local raw or invalid drafts; reloading current values requires confirmation before discarding them. This local stale-draft handling does not provide CAS across all APIs. Only service configuration uses a server-side SHA-256 revision of the compact `server` section, so unrelated section writes do not conflict; all other adapters retain their existing typed save semantics. The shared store serializes main/key-document writers with `state/config.lock` and updates a section in the latest main document while preserving other sections and credentials. It stages new immutable key references/entries while retaining old entries, publishes main references second, then removes only previously active references dropped by that committed update. Each replacement is atomic, not the pair; existing unreferenced/interrupted entries are not blindly garbage-collected, and secure erasure or automatic old-identity cleanup/recovery is not promised.

Store reads verify current private bytes and SHA-256 before reusing decoded content, including metadata-preserving edits. Secret-bearing reads recheck the main snapshot after reading keys and retry publication/pruning races; invalid/missing references in a stable snapshot fail closed without old-secret fallback. An existing main file is the sole source; older binaries must not use the migrated directory. Nonsecret section reads do not load the key file per request. Confirmed full reset retains the common writer lock, and Unix directory synchronization remains part of publication durability. None of this adds global hot reload for direct file edits. Existing typed API saves keep their runtime effects, agent/UA retain their direct-read boundaries, and cached model/MCP/search/capacity settings keep their reload/start requirements. A user-confirmed port change is the exception: Save and restart reserves the target in the successor before saving and returns the new address; the old tab uses a bounded readiness-image check and an explicit new-tab link. Port errors have their own codes rather than masquerading as revision conflicts. The saved port remains separate from the actual listener: precedence is CLI `--port` > `VISTA_PORT` > current `server.port` > default, and the regular WebUI Restart keeps the actual port. The CLI may read startup port inputs read-only for validation before restarting, without writing or migrating configuration; it does not promote a saved/default port into a synthetic explicit `--port`. The Server resolves the actual startup port after migration or current-format validation; preserving the live port for WebUI Restart is distinct from promoting saved/default values. See [storage contracts](../docs/architecture.md#unified-configuration-storage).

## MCP OAuth callback submission

`MCPSettings.vue` starts an attempt with `POST /api/mcp-servers/{id}/login` and reads its authorization link through `GET /api/mcp-auth/{id}`.
Open or copy that link and authorize in a browser. A browser connection error after authorization is expected: copy the complete callback URL from the address bar, return to Vista, paste it into the dialog, and submit it.
Attempts expire after 5 minutes; closing the dialog cancels with `DELETE /api/mcp-auth/{id}`, including a start response that arrives after closing.
Do not paste callback URLs into chats or logs.

Submit only while `pending`: `POST /api/mcp-auth/{id}` with JSON `{ "callbackUrl": "complete URL" }`.
HTTP 200 returns a Login in `exchanging` with an empty `authorizationUrl`; HTTP 400 leaves the attempt pending so the input can be corrected and retried; HTTP 409 means not ready, already submitted, or terminal and triggers an attempt-specific recovery read.
`starting`, `pending`, and `exchanging` are active; submission is disabled while its request is in flight or while exchanging, but cancellation stays available.
Shared MCP login updates contain only `id`, `status`, and optional `error`; they never carry authorization links or callback URLs.
Attempt IDs, generations, read revisions, and forward-only phases reject late responses and prevent SSE completion from regressing when a POST response arrives later.
Completion uses the shared state payload without another configuration or login read.

The callback field is limited to 8,192 characters and lives only in component memory, with autocomplete and spellcheck disabled.
Cancel, retry, unmount, accepted submission, exchanging, and terminal outcomes clear it; request or validation errors retain it while the attempt remains pending.
The UI sends it only as the Vista API request body: it never opens or fetches that URL, logs it, or saves it to browser storage.
All maintained languages share the instructions and controls; Chinese regional catalogs come from the generator below.

## Internationalization

Language registration and client matching are in `src/i18n/languages.ts`; region choices are in `src/i18n/regions.ts`. Only maintained wording variants are offered, not every CLDR territory or differences in date formatting alone. Automatic mode and single-region languages hide the region selector. Effective region is used for formatting and the next run's default reply language; explicit user language requests take precedence.

Maintain Simplified Chinese in `src/i18n/locales/zh-CN.ts`, with corrections in `src/i18n/chinese-overrides.ts`. `scripts/generate-chinese.ts` uses locked OpenCC JS to generate Taiwan (`twp`), Hong Kong/Macau (`hk`), and Singapore/Malaysia catalogs. Do not edit generated catalogs directly. Generation preserves placeholders, inline code, and URLs; OpenCC is a build dependency, not browser runtime code.

Other regional wording is maintained in `src/i18n/regional-overrides.ts`. `AppProvider.vue` passes the effective locale to Ark UI; component labels and announcements use the same application catalogs. `scripts/generate-reply-languages.ts` derives `internal/replylanguage/language_names.json` from the same language registry. Dev and production builds generate both outputs; `check:i18n` rejects stale generation. Tests cover keys, syntax, placeholders, region matching/formatting, backend names, and plural forms.

## Browser checks

[playwright.config.ts](./playwright.config.ts) defines:

| Project | Viewport |
| --- | --- |
| `desktop` | 1440 × 900 |
| `mobile-portrait` | 390 × 844, touch |
| `mobile-landscape` | 568 × 320, touch |

Set `VISTA_CHROME` to the browser executable; direct Playwright runs may set `VISTA_BIN` (otherwise the fixture uses repository `bin/vista`). Prefer `make test e2e`, which builds `.vista/test-state/bin/vista-e2e`. List tests with:

```sh
VISTA_CHROME=/absolute/path/to/chrome npm run test:e2e -- --list
```

[e2e/fixtures.ts](./e2e/fixtures.ts) manages separate configuration/state, explicit ports, simulated model services, and cleanup. Reports, failed screenshots, traces, and browser profiles stay under `web/.vista/test-state/`. Tests run without automatic retries. Catalog tests seed the automatic-check schedule to avoid unintended upstream fetches.
