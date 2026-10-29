# Session resources

[简体中文](./resources.zh-CN.md)

Vista stores original attachments in `state/resources/<first 2 hash characters>/<full SHA-256>`.
Identical bytes share one file across all Sessions. Originals are not resized or
recompressed. PNG/JPEG/WebP/GIF originals are limited to 50 MiB per image in both the UI and Worker.
`read_resource` also imports local images, including tool downloads and previously extracted video frames.
Audio/video input and automatic frame extraction are not supported.
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
Only the Worker loads original image bytes and assembles image-bearing model requests, including automatic titles.
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

## Verification

`internal/resources/library_test.go`, `internal/store/resource_storage_test.go`,
`internal/agent/resource_context_test.go`, `internal/provider/media_test.go`,
`internal/server/resource_storage_test.go`, and `internal/server/resource_upload_test.go` exercise original integrity, independent
ownership/names, close/restart/delete behavior, failed commits, concurrent cleanup,
model switching, checkpoint references, tool rereading and protocol projection.
Frontend tests cover delayed withdrawal reads across Session switches;
`web/e2e/resource-storage.spec.ts` and `images.spec.ts` cover the UI and persistence
using an isolated server, synthetic Provider and Chrome profile.
