# Message withdrawal and editing

[简体中文](./message-revert.zh-CN.md)

Withdrawal and editing share one conversation rollback path. The boundary is the selected user message's send time, not an entire run; earlier replies already submitted in that same execution may remain.

- Withdrawal restores the conversation before the message was sent and returns its text and attachments to the composer without starting work.
- Entering edit mode only loads a draft; it does not stop work or change history. Canceling restores the draft from before editing.
- Submitting an edit sends one request with `replacesMessageId`, rolling back before starting a new execution. If rollback succeeds but startup fails, the edited draft remains available for retry as an ordinary message.
- Rollback stops the conversation's Worker, subtasks, and background processes. It waits for the process tree to end before truncating history, then starts a new Worker if the conversation remains open. Other conversations continue.
- Rollback is serialized with sends, compaction, and queue startup in the same conversation. Old undelivered inputs do not resume automatically.

## Files and tool side effects

Only conversation history and model context are rolled back. File creation, modification, and deletion remain. Database, remote-service, and other tool side effects are also retained; later execution sees the current filesystem state. The withdrawal confirmation and edit notice explicitly state that file changes remain.

File and shell tools execute their ordinary operations without making rollback copies or scanning working directories. `write_file` still uses expected SHA-256 checks and atomic writes to avoid overwriting changed files.

## Effective context

One SQLite transaction removes formal messages, model history, output chunks, compaction checkpoints, and subtask report references at and after the boundary. It restores earlier checklists and pending questions and discards the old queue. Resource ownership follows retained messages and earlier tool imports/rereads; an edit also retains its explicitly submitted, already-owned image references in that transaction. Before withdrawal, the browser downloads the original attachments to restore the draft. If this download fails, history is unchanged. History, model-output, and resource-list reads cannot recover rolled-back content through its old ID.

When part of an execution remains, its surviving records are explicitly finalized as interrupted, so removing a terminal event cannot leave it apparently running. Conversation titles and model choices are independent user settings and do not roll back. Late automatic titles may write back only if their original first user message still exists.

`conversation_reverted` receives a new sequence after the previous maximum; SSE sequences are never reused. On this event or the rollback response, the browser reloads effective history, cancels old requests, clears output caches, retains the current draft, and continues guarding against late responses with the conversation generation. Controls live in the message action area and reuse existing virtualized message rows.

## Verification

- `internal/agent/tool_shell_linux_test.go`: foreground and background commands in directories containing special files; command failures and signal exits retain real output, error state, and file changes.
- `internal/store/revert_test.go` and `internal/server/revert_test.go`: retained file changes, context/output truncation, same-run interjections, monotonic event sequences, attachments, resource ownership, pending questions, task reports, and late titles.
- `web/src/composables/useConversation.test.ts`: edit cancellation, resubmission, restored attachments, rollback across tabs, and rapid conversation switching.
- `web/e2e/message-revert.spec.ts`: desktop and mobile in isolated Chrome, with real Worker file changes, external modifications, editing, withdrawal, active rollback, background processes, and interjection boundaries.
- `web/e2e/images.spec.ts`: editing, withdrawal and resending preserve original image bytes across resource-ownership changes.
