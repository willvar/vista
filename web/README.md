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
| Event state | `src/state/conversation.ts`, `workspace.ts`, `runTrace.ts` |
| Conversation loading/sending | `src/composables/useConversation.ts` |
| Shared workspace, models, polling | `src/composables/useWorkspace.ts` |
| Provider settings / historical runs | `src/composables/useProviderSettings.ts`, `useRunHistory.ts` |
| Shared welcome/conversation input | `src/ConversationSender.vue` |
| Context estimate / compaction | `src/ContextUsage.vue`, `ContextStatus.vue` |
| Routes and modal layout | `src/pages/`, `src/WorkspaceModal.vue`, `src/App.vue` |

Settings and the manager overlay the conversation through named route views; opening them keeps SSE active. Mobile settings uses a category page, then the selected settings page; Back returns through that hierarchy. Desktop retains the settings sidebar.

Conversation switches abort old requests and reject late callbacks by generation. Event sequences deduplicate SSE replay and reconcile polling snapshots. A stale poll must not replace newer titles, execution, or pending-answer state.

Main model text is a message-area row, distinct from execution details. `main-outputs` indexes history; visible rows load full model text. Formal replies replace the corresponding row by `outputTurn` rather than duplicating it. Reasoning appears chronologically inside execution details with no second fold. Tools and children stay in their steps. A call count or whole-run failure alone does not create an empty execution panel; whole-run diagnostics appear in the message area.

Message rows and long traces use virtual rendering and measured heights. Full-text caches are evicted outside their windows; returning to history reloads it. `useFullOutput` shares a bounded cache of 32 tool outputs and roughly 8 MiB of text; cache bounds do not truncate the currently expanded content.

A stable text selection offers quoted follow-up after 350 ms. Dragging/selection changes, copy, scroll, or Escape dismiss it. Quote drafts are conversation-scoped and sent as Markdown with the question through the normal user-input path, including steering/queueing. The combined message still has the 8 KiB input limit; failure retains the full draft. Editing and withdrawal share the [conversation rollback contract](../docs/message-revert.md).

`src/ui/appearance.ts` stores `vista-appearance` and syncs same-origin tabs. Semantic colors live in `src/ui/tokens.css`; `src/ui/theme.ts` configures components. Startup applies theme before styles. Motion follows `prefers-reduced-motion`, including component motion, CSS transitions/animations, and JavaScript smooth scrolling.

## Internationalization

Language registration and client matching are in `src/i18n/languages.ts`; region choices are in `src/i18n/regions.ts`. Only maintained wording variants are offered, not every CLDR territory or differences in date formatting alone. Automatic mode and single-region languages hide the region selector. Effective region is used for formatting and the next run's default reply language; explicit user language requests take precedence.

Maintain Simplified Chinese in `src/i18n/locales/zh-CN.ts`, with corrections in `src/i18n/chinese-overrides.ts`. `scripts/generate-chinese.ts` uses locked OpenCC JS to generate Taiwan (`twp`), Hong Kong/Macau (`hk`), and Singapore/Malaysia catalogs. Do not edit generated catalogs directly. Generation preserves placeholders, inline code, and URLs; OpenCC is a build dependency, not browser runtime code.

Other regional wording is maintained in `src/i18n/regional-overrides.ts`. Register component-library locale support when adding languages. `scripts/generate-reply-languages.ts` derives `internal/workerproto/language_names.json` from the same language registry. Dev and production builds generate both outputs; `check:i18n` rejects stale generation. Tests cover keys, syntax, placeholders, region matching/formatting, backend names, and plural forms.

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

[e2e/fixtures.ts](./e2e/fixtures.ts) manages separate configuration/state, explicit ports, simulated model services, and cleanup. Reports, failed screenshots, traces, and browser profiles stay under `web/.vista/test-state/`. Tests run without automatic retries. Catalog tests seed the automatic-check schedule to avoid unintended upstream fetches. Runtime on another OS and live-account/provider behavior require separate checks, not inference from this suite.
