# Session resources

[简体中文](./resources.zh-CN.md)

Vista stores immutable originals in the selected local or encrypted object-storage backend.
The fixed local location is `state/resources/<first 2 hash characters>/<full SHA-256>`.
Identical bytes share one active original across all Sessions; retained physical copies are accounted separately. Originals are not resized or
recompressed. PNG/JPEG/WebP/GIF originals are limited to 50 MiB per image in both the UI and Worker.
`read_resource` also imports local images, including tool downloads and previously extracted video frames.
Audio/video input and automatic frame extraction are not supported.
This library is separate from project working directories and output/process logs.

Generated image and video originals reuse the same backends, SHA-256 IDs and Session ownership. Generated MP4/WebM originals are limited to 128 MiB and remain available by resource ID after the upstream URL expires. The original video is a result, not a video attachment or model input. The tool reports success only after the Session owns the original. Decisions retain their structured result as a tool artifact, not a media resource. `list_resources` lists media references; `read_resource` still supplies only image input to models.

## Storage location

Settings → Storage selects local storage or an S3-compatible HTTP(S) endpoint, bucket, optional region and prefix.
The choice applies to the Vista instance, not the browser or an individual conversation.
Local directories are not selectable; databases, settings, locks, process logs and project files do not move.
The byte-storage layer is independent of resource origin and media policy; changing storage does not enable additional model input/output modalities.

Object storage uses the independent [DOFS](https://github.com/willvar/dofs) Go backend without FUSE or a PostgreSQL requirement.
Each registered backend has an instance-managed namespace and local DOFS SQLite metadata under `state/resource-backends/`.
Objects are encrypted with DOFS chunked AES-GCM; this is server-managed encryption, not zero-knowledge storage.
Access credentials and the generated master key use the `storage` section's private `key.json` bundles and are never returned to settings forms or shared state.
Credentials can be reused for the same saved location; checks do not replace in-use credentials until changes are applied.
Storage requests use the existing Global requests network policy and preserve SDK request contexts, retries and connection cleanup.

Originals are staged with bounded buffers in private `state/resource-staging/` files when hashing, transferring or serving seekable remote downloads.
Temporary copies still need local disk capacity; normal completion and startup recovery remove them.
Recovering object-backed resources requires the Vista database, matching DOFS metadata and encryption keys as well as encrypted objects.
Back up these local files consistently; object storage alone is not a conversation backup.

## References and retention

SQLite stores `resources` metadata, `session_resources` ownership, and ordered
`message_resources` / `queued_input_resources` links. Original filenames belong
to references, so sharing bytes does not overwrite another Session's filename.
No attachment binary is stored in SQLite, Worker commands or context checkpoints.

The Session is the retention root. Closing a Session, withdrawing/discarding queued
input, compaction, cancellation, failure, and restart retain its ownership. Deleting
the Session cascades its links; another Session's links keep shared originals alive.
Conversation rollback also releases ownership held only by discarded history; those resources are no longer exposed by that conversation. Originals with no references remain in storage until explicit cleanup in Settings → Storage.
Earlier tool imports/rereads retain their ownership through later rollbacks; replacement messages retain only their explicitly submitted, already-owned image references.
There is no automatic cleanup or action to strip attachments while retaining chat text.

New user attachments are materialized from references into a temporary copy for
the next request, subject to the current model's visual-input capability. After
that request succeeds, later requests retain resource metadata and hashes only.
The model explicitly calls `read_resource` when it needs to inspect an original
again. A text-only model sees metadata and an omission notice; switching models
does not automatically resend earlier attachments or modify canonical history.
`read_resource` accepts exactly one of `path` (an absolute local image path) or `hash` (an exact hash owned by the conversation).
Path reads copy the original into the library before activating it; later reads survive source-file changes or deletion.
Both tools return a session-owned HTTP `url` alongside each resource reference. Replies can display it with `![description](url)`; local filesystem paths and `file:` URLs are not browser image URLs. These URLs reuse the existing ownership-checked original download route. Previously saved replies with incorrect paths are not rewritten.
Summaries include resource hashes. `list_resources` lists retained originals with
a hash cursor, and `read_resource` supplies an authorized original as visual input
to the next request only. The model can recover images omitted by compaction without
relying on the summary to preserve every hash. Runtime image input is projected
after the complete tool-result group, without persisting an artificial user message.
The one-request activation is not serialized into history or checkpoints. Live
context-overflow recovery preserves pending attachments until the request succeeds;
it does not reactivate historical images.
Delegated tasks can use the Session's resource tools, but do not automatically
inherit the parent's current image payload.

## Disk operations and API

Publication streams into a temporary file, computes SHA-256, syncs it, then renames
it to the final path. A cross-process lock covers publication through reference
commit, file reads, and cleanup's final reference check and unlink. A failed
transaction can leave a reclaimable file, never a committed reference to partial
content. Reads verify size and full content hash. The Server streams raw uploads to private `state/inputs/` staging files with bounded buffers.
The conversation Worker verifies and imports each original, waits for the Server's ownership-commit acknowledgment, then returns a reference.
Upload staging is removed after the operation or at the next startup following interruption; published originals without ownership remain manually cleanable.
Only the Worker materializes complete image payloads in memory and assembles image-bearing model requests, including automatic titles. Remote reads and migration use private temporary files and bounded buffers.
The Server streams original downloads after bounded size/hash verification, and draft/rollback APIs return references.
The browser downloads a sent message's originals before withdrawing it, so releasing discarded ownership does not prevent restoring the attachments to the draft.

- `POST /api/sessions/:id/resources?name=<filename>` takes a raw image body and its image `Content-Type`, up to 50 MiB, for an open conversation with a ready Worker. It returns `{hash, name, mediaType, bytes}` after ownership commits.
- Message submissions include these references in `images`; Base64 image bodies are not accepted by the message API.

- `GET /api/resource-storage` returns the current asynchronous operation and last
  scan. `POST` with `{"action":"scan"}` or `{"action":"clean"}` returns 202 and
  starts one operation; concurrent requests share the running operation.
- Usage counts actual files once, separating referenced and cleanable counts/bytes.
  Cleanup rechecks ownership immediately before each deletion, reports count and
  freed bytes, and finishes with a fresh scan. Closing the settings modal does not
  cancel the server operation. Page requests abort when the component unmounts.
- `GET /api/sessions/:id/resources/:hash` serves the original only if that Session
  owns it. Existing message image URLs continue to resolve ordered attachments.

## Migration and reclamation

A storage change is one durable background task; closing settings does not cancel it.
The source remains the only active backend throughout copying, including originals published by running conversations.
Copies are identified by plaintext SHA-256 and size, uploaded as immutable objects and verified by reading the destination back.
Publication notifications coalesce wakeups, but notifications and an empty work queue never prove completeness.

The final check takes the cross-process publication lock and re-enumerates source originals plus retained logical references.
The destination's durable verified inventory must cover both sets before one Vista SQLite transaction commits its active backend, incremented storage epoch and completed migration state.
No remote transfer, HEAD request or content hashing runs in that final critical section.
Workers resolve the active backend after taking the lock; private publication frames carry its ID and epoch, and ownership commits conditionally validate that epoch.
Published RPC/tool results retain lock protection until ownership acknowledgement or Worker shutdown even if their caller cancels.
The ordinary resource lock still serializes operations and may wait for uploads/downloads; this is not a constant-time or zero-wait switch.

A failed transfer keeps the source active and can retry; interrupted single files restart as fresh immutable uploads without discarding uncertain-object recovery keys.
Cancellation before cutover stops copying and leaves the source active; returning after cutover is a new reverse migration.
Uncancelled work is recovered after restart and target proofs are reverified.
Continuous creation faster than synchronization can delay completion; Vista does not force conversations to stop or switch with a missing original.

Unreferenced data means a physical copy no longer serving effective ownership and not protected by an in-flight operation.
It includes ordinary orphans, cancelled-target copies, retired-source copies and incomplete or uncertain uploads.
The UI keeps one total and cleanup action, not backend-specific garbage categories.
Cleanup checks active storage, ownership and migration/check protection immediately before deletion.
A durable deletion fence rejects delayed ownership frames without holding a Vista SQL transaction over network deletion.
Removing a redundant copy never deletes another backend's effective resource identity or conversation links.

Upload intents precede payload transfer; exact-namespace inventories also find uncertain PUTs which appear after interruption.
Incomplete multipart uploads use the SDK's list/abort APIs and durable exact object keys. Exact-key recovery also works with services that do not implement namespace-prefix multipart listing; cleanup never falls back to a bucket-wide scan or deletes another namespace.
Successful deletion is confirmed before recovery records and accounting are retired; failures remain retryable.
Unreachable storage is reported as unverifiable rather than empty.
Usage includes physical copies and logical original sizes, not an exact provider billing report including encryption overhead or historical versions.
Object-storage verification requires a bucket with versioning disabled; Vista does not change bucket policies.

`GET /api/resource-storage` also returns the redacted configuration, a private-section revision and current migration state.
`POST` supports `check` and `migrate` with `{config, revision}`, and `cancel`/`retry` with `{migrationId}`.
Checks perform encrypted read/write/delete verification; a failed check returns a request-local `checkError` with the updated draft revision and never activates the target.
`scan` and `clean` retain their asynchronous 202 contract.
Progress and final state reuse scoped storage SSE snapshots, not browser polling.

## Verification

`internal/resources/library_test.go`, `internal/store/resource_storage_test.go`,
`internal/agent/resource_context_test.go`, `internal/provider/media_test.go`,
`internal/server/resource_storage_test.go`, and `internal/server/resource_upload_test.go` exercise original integrity, independent
ownership/names, close/restart/delete behavior, failed commits, concurrent cleanup,
model switching, checkpoint references, tool rereading and protocol projection.
Frontend tests cover delayed withdrawal reads across Session switches;
`web/e2e/resource-storage.spec.ts` and `images.spec.ts` cover the UI and persistence
using an isolated server, synthetic Provider and Chrome profile.

`internal/resources/migration_test.go`, `internal/configstore/storage_test.go`, `internal/server/resource_storage_config_test.go` and `internal/worker/resource_ack_linux_test.go` cover migration races, transaction boundaries, credentials, retry, recovery and acknowledgement protection.
`web/e2e/resource-destination.spec.ts` covers native-application settings, ongoing publication, cancellation/retry, reverse migration, cleanup, conflicts and responsive controls using the shared bounded HTTP fixture.
That fixture does not validate S3 signatures.
Run the real service contract separately:

```sh
VISTA_TEST_S3_BINARY=/path/to/minio GOWORK=off go test ./internal/resources -run '^TestResourceStorageLiveS3$' -count=1 -timeout=150s
```

Alternatively provide `VISTA_TEST_S3_ENDPOINT`, `VISTA_TEST_S3_BUCKET`, `VISTA_TEST_S3_ACCESS_KEY`, `VISTA_TEST_S3_SECRET_KEY`, and optional `REGION`/`PREFIX`/`SESSION_TOKEN` variables with the same `VISTA_TEST_S3_` prefix.
The test writes only a newly generated child prefix and namespace, installs cleanup before its first payload, and does not change bucket-wide policies.
A fixture pass does not establish live compatibility with every provider endpoint.
