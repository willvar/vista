# Model connections, catalogs, and routing

[简体中文](./providers.zh-CN.md)

## Connections and discovery

The WebUI connection list comes from [provider setups](../internal/modelcatalog/provider_setup.go). Built-in entries require usable catalog metadata. Common API-key providers load their catalog after connection rather than asking users to select model IDs in the form. Custom, Zen, Copilot, and local connections discover their available models. A catalog entry does not establish account entitlement.

Connections have independent IDs. Credentials, discovery, editing, defaults, and disconnection are connection-scoped, even when URLs or model IDs match. Read APIs report credential presence rather than secret values.

A custom connection selects one conversation API format for all its models:

| Format | Implementation |
| --- | --- |
| OpenAI Chat Completions | [openai_chat.go](../internal/provider/openai_chat.go) |
| OpenAI Responses | [openai_responses.go](../internal/provider/openai_responses.go) |
| Anthropic Messages | [anthropic.go](../internal/provider/anthropic.go) |
| Google Gemini generateContent | [gemini.go](../internal/provider/gemini.go) |
| Cohere Chat v2 | [cohere.go](../internal/provider/cohere.go) |

Authentication is independent: Bearer, `x-api-key`, `x-goog-api-key`, or none. Changing format does not change authentication. Custom headers override matching defaults and apply to discovery, conversation, title, and compaction requests. Arbitrary custom headers are currently available for custom connections only. An unchanged destination preserves stored secret header values when the edit leaves them blank; removing a field removes that header.

Metadata supplements names, capabilities, limits, and reasoning settings; it does not override a custom connection's explicit format. Failures do not probe or switch protocols. Manual model IDs are a fallback shown after failed or empty custom discovery, with the specific discovery error retained.

Discovery schedules are implemented separately: custom/Zen every minute, Copilot every five minutes, local services every 30 seconds. Temporary discovery errors retain the latest successful inventory. Editing a connection invalidates obsolete discovery responses. See [custom](../internal/server/custom_models.go), [Copilot](../internal/server/copilot_models.go), and [local](../internal/server/local_models.go) discovery.

## Built-in integrations

Built-in setups encapsulate platform-specific endpoint and authentication requirements:

- OpenAI API-key and OpenAI Codex are separate entries. Codex uses ChatGPT device-code login and its own OMP-backed inventory and credential refresh.
- Copilot provides device-code login and account-specific model discovery.
- Azure keeps the actual model identity separate from the deployment used in requests.
- Bedrock selects credential sources and uses AWS signing/Converse or Mantle Responses as applicable.
- Vertex has one setup; model routing selects Gemini, Anthropic, or MaaS behavior. API-key mode lists compatible Gemini models.
- Ollama, LM Studio, and vLLM discover models locally; authentication is not required for unauthenticated services.

Additional native transports include Mistral and Vercel AI Gateway. These are built-in integration details, not extra formats offered in the custom-connection selector. The shared [credential resolver](../internal/provider/credential_refresh.go) obtains renewable request credentials through the Server and coalesces exchanges by credential source. Explicit keys and short-lived tokens remain user-maintained.

## Catalog ownership

| Source | Purpose |
| --- | --- |
| [Bundled OMP snapshot](../internal/provider/omp_models.json) | Offline model identity, capabilities, limits, API metadata, and thinking policy |
| models.dev online catalog | Additional/current model metadata, kept separate before merging |
| [Provider routes](../internal/provider/catalog_providers.csv) | Vista protocol packages, endpoints, and credential environment variables |
| [Route overrides](../internal/provider/catalog_overrides.csv) | Required per-model protocol or endpoint differences |
| [Generated regression fixture](../scripts/provider_matrix/testdata/catalog.csv) | Deterministic route/configuration comparison, not live-request results |

The [snapshot generator](../scripts/build_omp_model_catalog.py) accepts an explicit OMP checkout; it is not run as a network step of ordinary builds. Runtime checks update OMP and models.dev together. Merge keys are exact provider/model IDs: free-suffix stripping or approximate model-name matching is not used to copy capabilities. OMP keeps its API/special thinking policy; models.dev can supplement names, inputs, limits, and explicit reasoning options. models.dev endpoints and authentication do not enter Vista routing.

Zen, custom, Copilot, and local discovery still decide account/service availability. Metadata alone does not certify tools, images, reasoning, or context size on a particular gateway. Context overflow handling is [independent of catalog limits](./context-compaction.md).

## Download and installation

[OMP downloads](../internal/provider/omp_catalog_update.go) first obtain the file SHA from GitHub Contents API. Raw GitHub and jsDelivr addresses are tried in shuffled order; downloaded bytes must match that Git blob SHA. An unchanged revision skips the OMP file download but still checks models.dev.

[models.dev downloads](../internal/provider/models_dev_catalog.go) use models.dev, models.opencode.ai, and Stencil. Addresses are shuffled, tried at most once, and each download attempt has a 15-second deadline. A normalized metadata digest ignores formatting and unused mirror fields. Superseded digests are remembered for three days to reject known-old content; this is a mirror-delay heuristic, not proof that an unseen digest is newer.

Ordinary additions and small corrections can be accepted from one publisher. [Risky changes](../internal/provider/models_dev_reliability.go)—removals, API changes, capability/effort losses, large known-limit changes, and widespread edits—need confirmation of affected provider/model fields from another publisher. models.dev and models.opencode.ai count as one publisher; Stencil counts as another. Unrelated additions need not match. Unconfirmed changes retain the active catalog and identify conflicts; a mirror serving current content does not erase a known conflict. ETags and old-digest history persist with the catalog.

Both sources are staged and validated before atomic save to configuration-root `omp-models.json`. A failure in either source, connection validation, or saving keeps the current installation. Changed source content updates the reported update time; unchanged checks do not. Restart reloads the saved sources and rebuilds the merge. New runs use the update without a service restart.

[Scheduling](../internal/server/model_catalog_updates.go) saves results, next check, and seven days of history separately in `model-catalog-status.json`. Successful checks schedule 24 hours plus up to 15 minutes of jitter; failures retry after six hours. Startup checks if due. Manual and automatic checks share a lock. `GET /api/model-catalog` reads status/history; `POST` starts a check. About Vista keeps the content-update time outside the history dialog and check/schedule details inside it.

## Protocol and verification boundaries

Adapters preserve portable text/tool pairs while handling protocol-specific replay fields. Provider-executed hosted calls keep opaque IDs and are never executed again as local tools. Extra citations beyond the parser's source limit are ignored rather than failing the whole response. HTTP/stream handling uses shared error, cancellation, and buffering rules in [model_http.go](../internal/provider/model_http.go). Explicit overflow enters compaction; ordinary failures do not replay tool effects.

Regenerate the deterministic fixture after changing the bundled catalog or routes:

```sh
GOWORK=off go run ./scripts/provider_matrix > scripts/provider_matrix/testdata/catalog.csv
GOWORK=off go test ./internal/provider ./internal/modelcatalog ./scripts/provider_matrix
```

Its `configuration_only`, `unverified_per_model`, and `unverified` fields are evidence boundaries, not a completed acceptance list. Protocol tests use local simulated endpoints; they do not establish every account or model's live compatibility. Local live-service checks have a separate [script](../scripts/live_local_chat_smoke.mjs). Keep environment-specific results under `.vista/acceptance/`; reusable test entry points are in [verification](./testing.md).
