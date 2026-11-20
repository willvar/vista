# Message withdrawal and editing

[简体中文](./message-revert.zh-CN.md)

Editing opens a temporary composer without changing history. Sending the edit or confirming a withdrawal creates a new branch at the selected user message's send boundary. Earlier messages, including previously committed replies in the same run, carry through. Withdrawal puts the original text and attachments in the new branch's composer without sending them.

The source remains available under **Conversation branches**. Changing branches does not stop its work or transfer queued input. One foreground run can be active across the branches of a conversation. Already executed tools, work on the host filesystem and remote side effects are not undone.

## Storage and context

A branch has a stable ID, parent, and original message identifier. It uses a separate persisted session stream, so UI queries and the Worker use the same branch. Common history is stored once. A branch references the original streams up to its fork point, keeping message and run IDs; only new records belong to its own stream. Attachments, completed tool outputs and replay data are also referenced, not copied. A compaction checkpoint is inherited only if both the checkpoint and its covered history precede the fork. Live queue, continuation and ownership records remain with their source. Events and messages at or after the boundary are not visible in the new branch.

`GET /api/sessions/{id}/branches` lists the branch addresses. A withdrawal returns the new session; the source URL is unchanged. Removal of a conversation with remaining derived branches is rejected instead of deleting their data. Old destructive withdrawals cannot be restored retroactively.

## Verification

- `internal/store/branches_test.go`: original history, the single foreground slot, and compaction cutoff isolation.
- `internal/server/revert_test.go`: original resource ownership and restored composer data without filesystem changes.
- `web/e2e/message-revert.spec.ts`: edit, withdrawal, interjection, branch changes during live work and existing file effects.
