# Prompt caching

[简体中文](./prompt-caching.zh-CN.md)

Vista prepares cache-friendly inputs during actual model requests. The service owns storage, expiry, eligibility and billing. An unchanged prefix, explicit breakpoint or stable key does not guarantee a hit or lower cost. Vista makes no background prewarming or keepalive calls and does not create, renew or delete cache resources.

## Request-time policy

[Capability selection and breakpoint compilation](../internal/provider/prompt_cache.go) follow [OpenCode's request-driven policy](https://github.com/anomalyco/opencode/blob/6dd40633a15ec82c5fa2f2c4ea17e22c2ba67084/packages/ai/src/cache-policy.ts), adapted under its [MIT license](../licenses/opencode/LICENSE). Selection considers the API format, service, model and explicit compatibility overrides. Unknown custom services receive no vendor-specific fields automatically.

For explicit-marker APIs, Vista targets the last tool definition, the first and last system blocks, and the latest cacheable conversation block. Text is preferred within a message; a tool-result-only tail remains eligible. There are at most four markers across the request. Existing manual markers consume the budget and retain their lifetimes; invalid excess markers or lifetime order are rejected, and automatic placements that conflict with manual lifetimes are skipped. Signed thinking blocks are preserved without cache markers. These are request-local changes, never edits to canonical history.

| Channel | Default behavior | Explicit options and boundaries |
| --- | --- | --- |
| Native OpenAI Chat / Responses | Service-managed automatic prefix caching; a session-derived `prompt_cache_key` where supported | `retention` forwards `in_memory` or `24h`; supported modern Responses models use `ttl` as `prompt_cache_options.ttl` (`30m` or `12h`). Vista does not enable prewarming or OpenAI explicit-only mode. |
| Anthropic / Vertex Messages | `cache_control` on tool, system and conversation boundaries | Default service lifetime or `5m` / `1h`; cache writes may be billed. System blocks keep instruction authority. |
| Bedrock Converse | Supported Claude models receive positional `cachePoint` blocks | Model-specific support and five-minute-only exceptions are checked, including regional IDs. Legacy models omit the TTL field even when `1h` was requested. Nova and other families retain their service's implicit behavior; an explicit `bedrock` format can opt an opaque inference profile into checkpoints. |
| Gemini / Vertex generateContent | Implicit caching; no inline markers or resource-management calls | An existing `cachedContent` can replace a verified prefix as described below. |
| DeepSeek and other automatic-cache Chat services | Stable request prefixes and reported usage | No Anthropic markers or invented OpenAI keys; a compatible custom service may explicitly enable capabilities. |
| OpenRouter / Vercel AI Gateway | Claude receives Anthropic hints; Qwen receives system/history hints without tool markers; other families use automatic caching | The actual wire representation is used: `cache_control` for Chat, `providerOptions.anthropic.cacheControl` (Claude) or `providerOptions.alibaba.cacheControl` (Qwen) for AI SDK v3. No Gemini tail-cache hint. Native Alibaba Qwen Chat uses system/history hints with a service-managed lifetime. |

Model and service support can change. See [OpenAI caching](https://developers.openai.com/api/docs/guides/prompt-caching), [Anthropic caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [Bedrock caching](https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html) and [Gemini caching](https://ai.google.dev/gemini-api/docs/caching) for current limits and charges. Protocol fixtures establish the request contract, not live-provider access or savings.

## Connection overrides

Custom connections expose one explicit cache-format choice in the existing form. New unknown custom services show "Omit cache hints"; choosing OpenAI enables the cache key by default, without a separate UI switch. Advanced API/configuration key overrides remain supported; unrelated edits preserve them, and explicitly changing the cache format resets them to that format’s defaults. Existing automatic configurations display their effective behavior and retain their saved policy during unrelated edits. These overrides apply to every model in that connection; native-provider selection still considers the API, service and individual model. Discovered models inherit the connection settings and selected proxy while keeping their own capability metadata. Ordinary edits preserve these choices. Changing the address or format clears them unless explicitly selected again. There is no separate cache settings page.

The connection API (`POST` / `PUT /api/provider-config`) and saved model configuration accept `promptCaching`; `GET /api/model-config` reports it:

```json
{
  "promptCaching": { "format": "openai", "key": true, "retention": "24h" }
}
```

`format` accepts `auto` (the default), `none`, `openai`, `anthropic`, `alibaba`, `bedrock` or `gemini`, subject to API-format validation. `none` omits Vista's hints; it cannot disable a service's automatic caching. An omitted `key` uses the service default; `true` or `false` explicitly controls the key capability independently of Codex routing. Saved configuration may supply a printable `promptCacheKey` override; otherwise the logical session supplies the key. Long keys use a deterministic SHA-256 digest. Existing xAI `promptCacheKey` configuration retains its native `x-grok-conv-id` behavior.

Optional `ttl` selects `5m` / `1h` for Messages and Converse, or `30m` / `12h` for supported Responses routes. `retention` and `ttl` cannot be combined. Omit lifetime options to keep the service default; longer lifetimes may change write charges. An explicit OpenAI format declares compatible options for opaque custom model IDs. Codex routing and legacy configuration compatibility are documented in [providers](./providers.md#codex-routing).

### Referencing existing Gemini content

Advanced configuration may include `promptCaching.cachedContent` with `name`, `sha256` and `messages`. The resource must already exist for the same endpoint, account and model. `messages` counts the initial **projected Gemini contents**, rather than raw Vista messages. `sha256` is the hex SHA-256 of compact Go `encoding/json` output for this object, with sorted map keys and the actual projected values:

```json
{"contents": [], "systemInstruction": null, "tools": null}
```

Replace `contents` with that prefix and include the full projected system instruction and tools, using `null` when absent. Only an exact digest match permits Vista to remove those fields and the prefix from the request and send `cachedContent` plus the remaining contents. At least one new content item must remain. A changed rule, tool or prefix fails before HTTP instead of silently reusing stale instructions. The reference is cleared from title, child-task and summary requests. This binding does not verify the remote resource's contents, existence or expiry; its creator must provide matching content. Resource creation and renewal remain external.

## Instruction and history continuity

The main agent retains its initial instruction baseline and latest admitted snapshot through acknowledged `model_history` prefix entries and immutable checkpoints. These metadata-only entries are not provider messages. Later rule, preference, or environment changes append a trusted instruction snapshot at their chronological position instead of replacing the original baseline. Locale changes append only a language control, never a stale copy of global rules or run facts. Current checklist snapshots are ordinary run data, not part of that privileged baseline. Model/connection changes establish a new series. Existing histories without prefix metadata establish one on their next execution; old records are not rewritten.

Prefix history entries use their committed input sequence as the index, so queued language changes before the same model request have distinct identities while retransmitted entries remain idempotent.

Responses sends initial instructions in `instructions` and later trusted changes as in-order developer messages. Other adapters retain their own instruction-placement contracts; this does not promise identical caching behavior across protocols. Current operation checks still read current directory rules and permissions. Pinning prompt bytes does not freeze execution authority.

Asynchronous completions are ordinary user-role context data with a local `RuntimeData` marker, not new user intent or privileged instructions. Their original text and order survive restoration. Older system-role runtime entries are projected as data without changing their saved contents. Images retain the existing one-request activation policy. Native assistant items and encrypted reasoning remain opaque replay state, not text to regenerate or summarize for cache purposes.

An unanswered question's native call remains adjacent to its future answer. Idle estimation and maintenance defer a changed instruction snapshot until an actual input completes that pair. Compaction can summarize completed runtime data and superseded instruction snapshots, while preserving the latest instruction snapshot, current real user input, and required rule/call groups.

Successful compaction removes superseded language controls, retaining only the latest control that follows the latest full instruction snapshot. A newer full snapshot already supplies the language default. This cleanup also recognizes older unmarked Vista language controls; failed or cancelled compaction leaves the original context intact.

## Usage and bounded diagnostics

Cache reads and writes are optional telemetry. Missing telemetry stays unknown; a reported zero is a measurement. Normalized input includes cached reads and writes exactly once: OpenAI, Gemini and Mistral report inclusive input, while Anthropic Messages and Bedrock Converse report uncached input plus separate reads/writes. AI SDK gateway totals are used directly, or derived from complete component counts; incomplete totals remain unavailable without failing generation. Reasoning remains part of output. Older stored records are not reconstructed.
Adapters and the Server's Worker-result boundary share normalized usage validation: each counter is between zero and one billion, cache reads cannot exceed input, and reported cache writes cannot exceed input minus cache reads. Invalid Worker usage is rejected before persistence or reader notification; missing write counts remain unknown.

Opt-in debug HTTP records include a common `cache` observation for all request adapters: available counters, sent request bytes, history-item count, instruction/tool hashes, first changed position when identifiable, and a structural change reason. Moving cache markers is excluded from semantic prefix comparison; tool arguments and native replay content remain significant.

While debug diagnostics are enabled, a resident conversation keeps separate baselines for up to 16 route/model/account identities across ordinary user turns. With diagnostics disabled, request bodies are not captured, parsed or fingerprinted for cache comparison; an unobserved request clears the baselines, so re-enabling starts a new series. Missing telemetry does not erase the last measured baseline; before any measurement, structural comparisons use available requests. Codex token resets do not clear these baselines. A Worker restart starts fresh. Each comparison covers at most 512 input fingerprints and a 16 MiB diagnostic request budget; `unchecked-tail` or `unchecked-request` means equality could not be established. These limits do not truncate requests, responses, tools or native replay. Captured prompt bytes are released after request transmission; structural observations retain hashes and counters, not text or routing tokens. Detailed HTTP capture has its own opt-in bounds and redaction.

An append-only result describes client input structure. It does not establish provider tokenization, cache availability, retention or billing discounts.
