# Session resources

[简体中文](./resources.zh-CN.md)

Vista stores original attachments in `state/resources/<first 2 hash characters>/<full SHA-256>`.
Identical bytes share one file across all Sessions. Originals are not resized or
recompressed. The current input boundary remains PNG/JPEG/WebP/GIF, at most 1 MiB
per image. The attachment path does not support audio/video or tool-produced images.
This library is separate from project working directories and output/process logs.

## References and retention

SQLite stores `resources` metadata, `session_resources` ownership, and ordered
`message_resources` / `queued_input_resources` links. Original filenames belong
to references, so sharing bytes does not overwrite another Session's filename.
No attachment binary is stored in SQLite, Worker commands or context checkpoints.

The Session is the retention root. Closing a Session, withdrawing/discarding queued
input, compaction, cancellation, failure, and restart retain its ownership. Deleting
the Session cascades its links; another Session's links keep shared originals alive.
Conversation rollback also releases ownership held only by discarded history; those resources are no longer exposed by that conversation. Original files with no references remain on disk until explicit cleanup in Settings → Storage.
There is no automatic cleanup or action to strip attachments while retaining chat text.

New user attachments are materialized from references into a temporary copy for
the next request, subject to the current model's visual-input capability. After
that request succeeds, later requests retain resource metadata and hashes only.
The model explicitly calls `read_resource` when it needs to inspect an original
again. A text-only model sees metadata and an omission notice; switching models
does not automatically resend earlier attachments or modify canonical history.
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
content. Reads verify size and full content hash. Interrupted upload files are
counted as unreferenced and removed only by manual cleanup.

- `GET /api/resource-storage` returns the current asynchronous operation and last
  scan. `POST` with `{"action":"scan"}` or `{"action":"clean"}` returns 202 and
  starts one operation; concurrent requests share the running operation.
- Usage counts actual files once, separating referenced and cleanable counts/bytes.
  Cleanup rechecks ownership immediately before each deletion, reports count and
  freed bytes, and finishes with a fresh scan. Closing the settings modal does not
  cancel the server operation. Page requests abort when the component unmounts.
- `GET /api/sessions/:id/resources/:hash` serves the original only if that Session
  owns it. Existing message image URLs continue to resolve ordered attachments.

## Verification

`internal/resources/library_test.go`, `internal/store/resource_storage_test.go`,
`internal/agent/resource_context_test.go`, `internal/provider/media_test.go`, and
`internal/server/resource_storage_test.go` exercise original integrity, independent
ownership/names, close/restart/delete behavior, failed commits, concurrent cleanup,
model switching, checkpoint references, tool rereading and protocol projection.
Frontend tests cover delayed withdrawal reads across Session switches;
`web/e2e/resource-storage.spec.ts` and `images.spec.ts` cover the UI and persistence
using an isolated server, synthetic Provider and Chrome profile.
