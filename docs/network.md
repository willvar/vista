# Network configuration

[简体中文](./network.zh-CN.md)

## Scope and defaults

Settings → Network configuration contains Global requests and proxy profiles.
All application-owned clients default to direct connections and ignore uppercase and lowercase `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, and `NO_PROXY` variables.
Direct means no Vista application proxy; operating-system routes, transparent gateways, and tunnels still apply.
Vista does not delete or inject these variables in the environments inherited by shell commands, external programs, or stdio MCP servers.
Their networking, and browser-side authorization pages, are outside this configuration.

The Global requests selection governs search, page reading, catalog downloads, remote HTTP MCP, and its OAuth traffic.
Each model connection independently selects Direct or a profile, including inference, inventory, server-side sign-in, token renewal, titles, compaction, and delegated model work.
A model connection does not inherit the Global requests proxy.
Internal control traffic stays direct.

## Proxy profiles

Create a proxy profile with the + button in the Network configuration header; edit existing profiles in its list.
Network proxy selectors list saved profiles; leave the selection empty or clear it to use no proxy.
Service connection editors select existing profiles; they do not create profiles.
The choices stay synchronized with saved network settings while preserving connection drafts.

Profiles have stable IDs, editable names, protocol, server, port, authentication, and optional connection IP.
HTTP and HTTPS proxies support no authentication or Basic username/password; SOCKS5 supports no authentication or username/password TCP CONNECT.
HTTPS proxy means the connection to the proxy itself uses TLS, not merely that an HTTP proxy can tunnel an HTTPS target.
UDP, PAC, automatic system-proxy discovery, proxy chains, and native node protocols are not provided.

The editor shows Connection IP only for HTTPS proxies; it skips DNS resolution of the proxy server hostname and connects to the specified IP.
HTTPS proxy certificate verification is on by default and can be disabled for that profile.
The switch affects only the proxy certificate and preserves destination HTTPS certificate verification settings.
The proxy hostname is retained for TLS SNI and, while verification is enabled, certificate checks.
The profile stores an explicit opt-out as `insecureSkipVerify: true`; an omitted or false value keeps verification enabled.
Ports default to HTTP 80, HTTPS 443, and SOCKS5 1080; changing protocol updates an unchanged default port and preserves a custom port.
Loopback names and addresses retain Vista-side meaning rather than referring to a remote proxy's host.
`localhost` and names ending in `.localhost` try IPv6 `::1` and IPv4 `127.0.0.1`, falling back when a listener is unavailable; neither uses an external resolver or proxy.
Authentication over plain HTTP or SOCKS5 is not encrypted by that protocol itself.

Settings use explicit saving.
Saved credentials are not returned; blank credential inputs retain them only for the same entry, and selecting no authentication clears them.
Reusing saved credentials after changing the proxy entry requires explicit consent.
A referenced profile cannot be deleted until its selections are changed; the error identifies Global requests and the named model connections with navigation targets.
The normal profile list does not display reference counts.

Model requests do not automatically retry proxy authentication or certificate verification failures.
Pre-response timeouts, temporary network failures, and recognized socket connection failures retain the model request's bounded retry policy.

## DNS and target addresses

Direct requests use the system resolver and the native transport's address fallback.
Ordinary requests through HTTP, HTTPS, or SOCKS5 proxies pass the target hostname to the proxy for resolution and connection.
The proxy entry itself uses system DNS unless its connection IP is supplied.
Original URLs, HTTP Host, TLS SNI, certificate verification names, and request signatures are retained.
Vista does not change system DNS or the DNS used by external programs and external reading services.

Local page reading first uses Vista's system resolver to obtain and validate destination addresses, even when a proxy is selected.
IPv4 and IPv6 lookups run concurrently; an empty or not-found family is a completed negative result.
If one family fails but the other provides permitted addresses, Vista reads the page itself using only those validated addresses and does not submit the URL to an external reader.
A name resolvable only by the proxy is therefore unavailable to the local reader, even when ordinary proxied requests can reach it.
The connection uses only the validated addresses through the selected route.
HTTP(S) proxies must accept CONNECT to the validated IP and port, including for plain HTTP targets; refusal is an error rather than a fallback to proxy-side hostname resolution.
Multiple validated addresses use staggered connection attempts through that same route; success or cancellation stops remaining attempts and closes late connections.

## Page reading

Public pages eligible for third-party submission use the configured Jina/DeepSeek order.
Local, loopback, and clearly internal HTTP(S) pages use Vista-side fetching and conversion, without submitting their URLs or bodies to external readers.
`webfetch` accepts `reader: "local"` for an explicitly intended internal domain or authorized sensitive URL; it is a fetch location, not an instruction to ignore the network policy.
Local fetching follows the Global requests proxy selection.
It does not execute JavaScript, log in, inherit browser cookies, or fall back to external readers after a failure.
Only text document types are read; binary downloads remain separate tools.
HTML is converted to Markdown or readable text.
Before conversion, HTML encoding is detected from a BOM, the HTTP charset, or early HTML meta declarations; other text types use their declared charset or default to UTF-8.
Documents beyond the DOM-conversion memory budget retain their full HTML instead of truncating the download.

If no addresses are available, the operation is cancelled, or its deadline expires, reading fails. DNS failures alone do not classify a target as internal.
A public-looking hostname resolving to non-public addresses requires explicit local-reading intent; disallowed endpoints and unsafe redirects remain errors.
Validated address results are bound to the local connection, with redirect targets checked individually.
Connection pools for bound requests are isolated by normalized hostname and validated IP set as well as network policy; changing that set cannot reuse a connection to an old address.
The same set can reuse connections regardless of address order. Bound pools are capped at 64; eviction closes idle connections without interrupting active requests.
Unicode and Punycode hostnames share the same normalized binding identity; invalid binding names are rejected.
Cross-origin redirects remove Referer, Authorization and Cookie headers; same-origin redirects retain the normal referrer.
Sensitive URL metadata is redacted from results.
The output uses the existing complete artifact and byte-preview mechanism.
Not submitting content to a third-party reader does not prevent the resulting tool text from subsequently being sent to the selected model.

The proxy profile modal provides the connection check. It accepts HTTP and HTTPS targets and uses the ordinary request path, including native DNS, address fallback, timeouts, and cancellation. Checks do not save or apply settings.

## State and APIs

The `network` section in `config.json` owns `globalProxy` and `profiles`; profile username/password bundles live in `key.json` under stable profile ownership.
Model anchors store only `proxyId`, with an empty selection meaning Direct.
Public settings and state contain credential-presence flags, never resolved private execution policies.
Each execution and MCP lease keeps its private immutable network snapshot; new settings do not interrupt active streams.
HTTP operations capture network settings on their first global or connection policy selection and share that snapshot, including load failures, through subsequent selections.
Static resources and local requests do not load proxy settings or credentials merely by passing through HTTP middleware.
Search MCP connections retain that same snapshot through delayed initialization and reconnection, including an explicit Direct route.
Pool identity includes functional routing and credentials, not profile display labels.
MCP HTTP leases release their owned pools when closed or when initialization fails; unrelated clients retain their connection pools.
Failures never silently change proxy, route, or resolver.
Application HTTP clients declare application or model-connection scope through one shared constructor.
Connection clients require an explicit connection binding, including Direct, or a private pinned policy; a global request policy cannot satisfy that requirement.
Discovery receives the complete connection through one entry for custom, Copilot, Zen, and configured local services.
HTTP wrappers expose their network leaf; an undeclared wrapper chain fails before dispatch.
SDKs that omit the caller context receive a client bound to the operation's policy, deadline, and cancellation.
Runtime metadata credentials identify Vista's own execution environment.
AWS EC2 instance roles, ECS/EKS container credential endpoints (including explicit full-URI overrides), GCE tokens, and metadata-backed Vertex file federation share one identity-source guard.
These requests require a direct connection; a route that would proxy them is rejected before dispatch, with guidance to clear the selected proxy or supply explicit credentials.
Vista does not silently override the selected route or accept a proxy's runtime identity.
Redirects from or into metadata identity sources are rejected, including same-host redirects.
Normal STS, SSO, OAuth, impersonation and non-metadata remote token-supplier requests continue through the connection policy.

Bedrock retains the AWS SDK's default credential chain, source precedence, credential caching, endpoint overrides, metadata-disable setting and retries.
The SDK's automatic defaults-mode metadata discovery uses the same guard; unavailable discovery does not prevent an earlier credential source from working.
AWS credential HTTP clients honor `AWS_CA_BUNDLE` and shared-profile `ca_bundle`, including SDK precedence and invalid-bundle rejection.
A transport-only SDK configuration pass loads CA/client settings with credential retrieval and automatic environment discovery disabled.
Normal configuration retains the SDK native client required by its CA resolver.
Credential dispatch obtains each service's resolved HTTP settings from the installed SDK constructors, including defaults mode, automatic region selection, connect/TLS budgets and service read timeouts.
Separate service/region clients retain their own pools; SDK request middleware dispatches through Vista's guarded, operation-bound clients.
The read-inactivity bridge retains the SDK's exported timeout value and timeout-error type because the frozen native client does not expose its transport leaf.
A rejecting native proxy hook survives SDK dialer replacement, preventing direct egress if a code path omits the dispatcher.
Container credential requests carry explicit identity-source purpose because their SDK branch constructs a separate client.
Each credential operation releases its owned idle pools after resolution.
Vertex preserves Google's file-before-metadata precedence and delegates file credential parsing and token exchange to `oauth2/google`.
AWS federation's configured region, role/credential and IMDSv2 URLs are identity sources, including inside an impersonated credential file.
URL-sourced federation recognizes standard metadata authorities and explicit Google/Azure metadata headers; other remote URL sources retain their normal routing.
Without a credential file, Vertex requests a GCE token through the metadata SDK with the connection's HTTP client and operation context, within a five-second budget.
It does not invoke the SDK's separate platform detection or system-DNS probe.
SDK metadata HTTP requests stop when their Vista operation is cancelled, including requests whose SDK credential cache detaches the request context.

- `GET /api/network-config` returns `{config, revision, version}` with redacted profiles.
- `PUT /api/network-config` accepts `{config, revision, reuseCredentials?}` through the typed adapter. Do not copy credential-presence fields or redacted values into writes.
- `POST /api/network-check` accepts the same draft plus `profileId`, `target`, and its current `revision`. It performs no settings write and sends no model key or inference request.
- Provider create/edit accepts `proxyId`; omission retains an existing binding on edits, while an explicit empty value selects Direct. Editing uses the model-settings revision.
- `PATCH /api/provider-config` accepts `{key, proxyId, revision}` for a network-only edit that preserves advanced connection fields.

Network state uses the existing scoped `network` SSE topic, with bounded redacted payloads and revision recovery.
Concurrent views share a fallback HTTP read; closing one view cancels it only when no readers remain. Failed active reads remain unapplied and retry through stream recovery.
Editors preserve drafts on remote changes, reject stale saves, and confirm discarding before reload or exit.
Manual checks show the requested target, time, effective route, and actual failure information; a received HTTP status proves network response, not service authorization.
Certificate failures distinguish the HTTPS proxy handshake from the destination TLS handshake.
There is no background health polling or automatic test-site request.

## Code entry points

- `internal/network/`: configuration validation, policy snapshots, route selection, address pinning, and transports.
- `internal/network/client.go`, `internal/model/network.go`, `internal/modelcatalog/discovery.go`: scoped clients, model execution binding, and connection-owned discovery.
- `internal/configstore/`: network credential splitting and atomic publication.
- `internal/server/network_config.go`, `provider_network.go`: typed settings, checks, references, and binding changes.
- `internal/tools/webfetch_local.go`: local target classification and conversion.
- `web/src/NetworkSettings.vue`, `NetworkProfileEditor.vue`, `ProxySelection.vue`: UI and write-only drafts.
