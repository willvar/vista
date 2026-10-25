# Product behavior and audience

[简体中文](./product.zh-CN.md)

Vista is a general-purpose agent with a browser interface. Users describe a goal, provide relevant paths or attachments, and work with a model that can call tools. The executable contains the Go service, conversation Workers, and embedded Vue interface.

## Audience and design baseline

Vista primarily serves people who can independently use and configure everyday computer or phone software:

| User profile | Typical capabilities |
| --- | --- |
| Proficient everyday use | Manages files and folders, installs software, changes common settings, and searches for solutions. |
| Advanced tool use | Also combines tools, extensions, batch processing, or simple automation and follows instructions to complete unfamiliar configuration. |

Programming, command-line, API, and service-deployment knowledge are not prerequisites.

Use these principles when changing the product:

- Clear defaults and instructions let users with everyday software skills start independently.
- Progressive settings let users configure additional models, skills, and MCP tools by following instructions, without complicating everyday use.
- Explain unfamiliar controls through their purpose, benefits, and costs.
- Describe Vista through tasks and collaboration without assuming it runs on the user's own computer.
- More advanced users retain configuration room. Technical ability and willingness to study configuration are separate; assess desktop and phone use separately.

## Conversation behavior

- First use connects a model provider in Settings. Built-in connections encapsulate provider setup; a custom connection explicitly selects its API format and authentication. Model availability depends on the connected service and account.
- The first sent message creates a conversation and Worker. Open conversations have resident Workers, subject to admission and health checks. Navigating away or closing the browser leaves work running; closing a conversation stops its Worker and managed work but retains history. Deletion removes its stored conversation data.
- Main-thread execution is sequential per conversation. Other conversations, child tasks, and tools may run concurrently. Normal sends during execution steer at a decision boundary; explicitly queued sends wait. Cancellation and failure pause undelivered inputs; a fresh send after execution stops can start new work without releasing the old queue.
- Restart restores Workers for open conversations and records unfinished work as interrupted. It does not resume interrupted model requests or replay tool side effects.
- Text, execution details, questions, checklists, and usage remain with their originating messages. The sidebar distinguishes foreground execution, background-only activity, unread replies, pending answers, paused input, Worker unavailability, and failure.
- Automatic model selection offers Fast and Quality modes; manual selection chooses a connection/model and available reasoning setting. Settings changes during execution apply to subsequent runs.
- Automatic conversation titles are enabled by default and can be disabled in Personalization. When enabled, a separate model request generates the first title without tools or an explicit reasoning effort; it may fail independently. When disabled, new conversations display a localized default name followed by their creation time in `YYYYMMDDHHmmss` form, such as “New Session-20261025174143”. The timestamp uses the client’s local time. Existing and manually assigned titles are preserved.

See [architecture](./architecture.md) and [process lifecycle](./process-lifecycle.md) for runtime ownership.

## Context and resources

Completed model turns and tool results survive later messages and Worker restarts. Explicit upstream context-overflow evidence triggers compaction and retry of the failed request. Manual compaction uses the same checkpoint mechanism while idle and does not send or continue automatically. The composer shows an estimate of retained context, not a model-capacity percentage. See [compaction](./context-compaction.md).

Images use shared original files with conversation-owned references. New attachments or explicit resource reads activate visual input for the next supported request; earlier images are not automatically resent. Closing retains ownership; deleting a conversation releases its links, and fully unreferenced files can be cleaned manually. See [resources](./resources.md).

Editing and withdrawing sent messages roll back conversation/model history only. Files, shell effects, and remote changes remain. See [message rollback](./message-revert.md).

## Personalization and extensions

- Global instructions are the user's configuration-root `AGENTS.md`, read at execution start. Directory-scoped instructions are discovered and read through tools; nearer rules take precedence within their scope, and explicit current requests take precedence over defaults.
- Memory stores explicit lasting preferences across conversations. Users manage it in Personalization; only the main agent can call `memory`, while children inherit the preference snapshot. Deleting an individual conversation does not delete memory.
- The maximum delegation depth defaults to 1 and accepts 0–8. Zero disables child delegation. This is a maximum, not a fixed number of levels the model must create. Required tasks are joined before finalization; best-effort and detached tasks have different lifetimes.
- System skills are embedded in Vista, visible as read-only entries in settings, and updated with Vista without being extracted to the user directory. The bundled `vista-guide` helps configure and troubleshoot Vista. Users place their own skill folders containing `SKILL.md` in the configuration-root `skills/` directory and refresh the settings list. Each execution receives system and enabled user skills' names, descriptions, paths, and sources; full instructions and supporting documents are read on demand. Descriptions follow Agent Skills' limit of 1024 Unicode characters.
- MCP connections supply tools over local stdio or remote Streamable HTTP, with enable/disable controls and connection-scoped credentials. They do not provide MCP resources, prompts, sampling, or elicitation.
- MCP and skills settings can import OpenCode v1/v2 configuration through an explicit read, selection, and confirmation. Existing entries are kept. Skill imports copy complete folders, including supporting and binary files; MCP imports save compatible configuration without starting services. Unsupported entries are shown with reasons. See [import contracts](./architecture.md#providers-and-tools).
- Network Queries configures separate search and page-reading services, order, keys, and initial output lengths. Search returns sources and summaries; page reading retrieves a known URL.

With Debug mode enabled, About Vista offers a confirmed full reset. It stops running tasks, managed background processes and logins, then deletes all conversations, attached resources, connections and credentials, user skills, global instructions, memory and other Vista settings. Embedded system skills remain available. The current browser's Vista preferences and drafts are cleared, the runtime restarts, and the page opens the first-use screen at its new address. External files and services, including files previously changed by tools, are not reverted. The listener uses the default port after reset, or the port specified by the process environment; the previous launch port is discarded. Browser preferences in other clients and settings supplied through the process environment are outside this reset.

## Current boundaries

Vista binds to loopback and runs tools with the service user's local permissions. There is no per-tool approval or filesystem rollback. Conversations share the real filesystem and have no fixed project directory; tools require explicit target paths. Optimistic checks in file-writing tools do not cover arbitrary shell writes.

Image attachments currently accept PNG, JPEG, WebP, and GIF, up to 1 MiB each. The attachment path does not accept audio/video or expose tool-generated images. There is no built-in LSP, dedicated Git workflow, or terminal UI. Model/protocol metadata and local simulations do not certify every upstream account or model; see [providers](./providers.md) and [verification](./testing.md).
