# Message withdrawal and editing

[简体中文](./message-revert.zh-CN.md)

Editing opens a temporary composer without changing history. Sending the edit or confirming a withdrawal creates a new branch at the selected user message's send boundary. Earlier messages, including previously committed replies in the same run, carry through. Withdrawal puts the original text and attachments in the new branch's composer without sending them.

Use the left and right arrows below the changed message to switch between its versions. If that message is outside the visible history, scroll back to it. Before the branch has a sent replacement, its arrows appear above the composer. Switching branches keeps each draft with its own branch and does not stop work or transfer queued input. One foreground run can be active across the branches of a conversation. Already executed tools, work on the host filesystem and remote side effects are not undone.

The sidebar keeps one row per conversation. It shows activity across open branches; when present, click a status such as **Waiting for your answer** to open the branch that needs attention.
Using the version arrows saves the preferred branch for this Profile on the Vista service. Entering from the sidebar or Manage sessions uses that preference on any device connected to the same Profile. It never redirects another tab that's already reading a branch. Opening a direct URL, reloading, or clicking an activity status does not change the preference.
If the preferred branch isn't open, the row opens the original if available, otherwise another open branch. This does not reopen a closed branch or replace the saved preference. Reopen an entirely closed conversation from Manage sessions. Explicitly choosing a closed version with the arrows reopens just that branch, without resuming its tasks or opening sibling branches.

## Storage and context

A branch has a stable ID, parent, and original message identifier. It uses a separate persisted session stream, so UI queries and the Worker use the same branch. Common history is stored once. A branch references the original streams up to its fork point, keeping message and run IDs; only new records belong to its own stream. Attachments, completed tool outputs and replay data are also referenced, not copied. A compaction checkpoint is inherited only if both the checkpoint and its covered history precede the fork. Live queue, continuation and ownership records remain with their source. Events and messages at or after the boundary are not visible in the new branch.

`GET /api/sessions/{id}/branches` lists the branch addresses, the source message and, when present, the first user message owned by each branch. The response contains message identifiers and sequence numbers, not message bodies. `PUT /api/sessions/{id}/branch-preference` saves that open branch for its own conversation group. Session-list snapshots include a bounded `branchGroup` entry and status projection when open derived branches exist; the branch-specific execution and read state are unchanged. A withdrawal returns the new session; the source URL is unchanged. The WebUI closes a whole conversation with `POST /api/sessions/{id}/group/close` and deletes it with `DELETE /api/sessions/{id}/group`. Either endpoint resolves the root and applies to all members, including closed branches; success returns `{sessionIds}`. The branch-specific endpoints retain their original scope and reject deletion of an ancestor with dependants. Old destructive withdrawals cannot be restored retroactively.

## Verification

- `internal/store/branches_test.go`: original history, the single foreground slot, and compaction cutoff isolation.
- `internal/server/revert_test.go`: original resource ownership and restored composer data without filesystem changes.
- `web/e2e/message-revert.spec.ts`: edit, withdrawal, interjection, branch changes during live work and existing file effects.
