# Vista client SDK

[简体中文](./client-sdk.zh-CN.md)

`@vista/client` is the in-repository TypeScript client of Vista's WebUI. It ships ESM and TypeScript declarations, with no runtime package dependencies. A runtime needs Fetch, Web Streams, and `AbortSignal.any` (for example, a current browser or Node.js 24). This private package and the Vista backend are built from the same source revision; there is no registry release or independent backend-version guarantee.

## Build and consume

From the repository root, create a tarball for local installation:

```sh
npm --prefix sdk ci
mkdir -p .vista/dist
npm pack ./sdk --pack-destination .vista/dist
```

Install the generated tarball in a separate npm project with `npm install /path/to/vista-client-0.0.0.tgz`. The package does not need a WebUI checkout when installed. WebUI builds instead use `file:../sdk` and the same compiled entry point.

```ts
import { createVistaClient, type Session } from '@vista/client'

const client = createVistaClient({ baseURL: 'http://127.0.0.1:5186' })
const openSessions = await client.request<Session[]>('/sessions?open=true')
```

`baseURL` is the service URL, without `/api`. It can include a deployment path prefix; omitting it uses same-origin `/api`. Optional `fetch`, `headers`, `language`, and `credentials` configure HTTP reads and writes, images read with `image()`, and both SSE connections. `url()` resolves an API path for a native media link or preview; a browser element does not apply the SDK's custom headers. Use `image()` when an image needs those headers. The SDK does not grant backend access or change [Vista's deployment boundary](./product.md#current-boundaries).

## Synchronization

- Use one `createWorkspaceSync({ client, status, metrics })` per workspace. Subscribe to the topics the application needs, then call `start()`. The synchronizer owns the shared SSE connection, updates topic interests, and delivers revision- and epoch-scoped payloads. Unsubscribe when a view closes, and `stop()` when the owner is disposed. The subscription callback can use the supplied abort signal for any missing snapshot read.
- Use `createConversationSync` for a selected conversation. Connect its callbacks to `restoreConversationSnapshot` and `reduceConversationEvent` (or an equivalent store); report the applied event/reply cursors and the authoritative queue state. `start()` reads the bounded snapshot, and reconnects replay missing records before resuming the durable stream. Abort the supplied signal when leaving the view. See `web/src/composables/useConversation.ts` for an implementation that preserves paging and concurrent send acknowledgements.
- Send commands explicitly through `request`; the synchronizers only recover state and their subscriptions, and do not resend messages. Report read acknowledgements only when your UI knows the content has been seen. Apps own route changes, local drafts, browser foreground/online signals, and rendering.

See [architecture](./architecture.md) for the HTTP/SSE behavior and recovery invariants.
