# Context compaction

[简体中文](./context-compaction.zh-CN.md)

Vista owns overflow detection. It does not infer a provider's real context limit
from a model ID, the OMP catalog, usage percentages, or previous failures.

## Detection

`internal/provider/context_overflow.go` recognizes explicit context/input/prompt
overflow evidence in HTTP error envelopes, SSE errors, and explicit context
exhaustion stop reasons. The adapters produce a typed error before discarding
untrusted upstream diagnostic bodies. Nested gateway errors and JSON encoded
`metadata.raw` messages are examined to a bounded depth. Error bodies are read
up to 128 KiB with a two-second diagnostic deadline.

The rules adapt the explicit-evidence portion of these MIT sources:

- [Pi overflow.ts, b9ab2a6](https://github.com/badlogic/pi-mono/blob/b9ab2a6238d2c91cf0c6ebf836dfd05883f9db51/packages/ai/src/utils/overflow.ts)
  — [license](../licenses/pi/LICENSE).
- [OMP error/flags.ts, f1ac114](https://github.com/can1357/oh-my-pi/blob/f1ac114d8d3438bee43010aee559bfe720ba3224/packages/ai/src/error/flags.ts)
  — [license](../licenses/oh-my-pi/LICENSE).

Local gateway fixtures simulate the error-envelope formats of
[New API error handling, 7de10f2](https://github.com/QuantumNous/new-api/blob/7de10f2c602b50122017556e8a09cb7c488c273c/service/error.go)
and [One API error handling, 8df4a26](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/relay/controller/error.go).
They cover retained upstream messages/codes, remapped statuses, and HTTP 200 JSON error responses.

Intentionally excluded: a bare 400/413/503, byte or image payload rejection,
generic “too many tokens”, rate/quota/authentication failures, an invalid output
token parameter, ordinary `length`/`max_tokens` completion, disconnections, or a
usage count exceeding catalog metadata. Assistant text is never classified as
an error. Unknown failures stop and surface normally. A gateway that erases the
reason, or silently truncates input, cannot be reliably detected by these rules.

## Recovery and storage

Automatic recovery runs only between model/tool decision boundaries. It keeps
fixed system instructions, the current run's original user task, latest user input,
incomplete call groups, and newest complete assistant/tool group. It summarizes
older complete groups with the current selected model and no tools. Estimates
are used to partition the work, never to assert model capacity.

Automatically joined child reports carry a local `RuntimeData` marker. Their
wire role remains system, but they are evidence rather than fixed instructions:
both automatic and manual compaction can summarize them, including reports just
appended after the latest assistant/tool group. The marker survives history,
checkpoints and model/protocol changes; it is never serialized to provider APIs.
A joined batch shares a 16 KiB inline answer/error text budget, with explicit
truncation flags and original report references. Full reports remain readable
through `read_model_output` or `await_agent` after compaction. The summarizer
receives report text as runtime results and preserves task identity and references.

Manual compaction shares this summarizer and checkpoint path, but may also
summarize the last completed reply, including a conversation with only one
user/assistant pair. It keeps the latest user input verbatim. A pending question
or request for user input still protects the last group until it is answered.

Summary requests that explicitly overflow halve the transcript batch. Each
compaction has at most 32 summary requests, including rejected requests. A
summary must be nonempty text within 8 KiB, with no tool calls, and the resulting
context must be smaller. Failure retains the active checkpoint. The failed main
request gets at most two recovery compactions before surfacing an error; a
successful model turn resets this recovery counter. Completed tools do not run
again during retry. Inputs arriving during compaction are consumed at the next
decision boundary.

An immutable gob file under `state/outputs/context-*.gob` preserves the summary,
remaining messages, resource references, tool pairs and opaque provider replay fields. Image
bytes are never serialized into the checkpoint. Originals live in the shared
content-addressed resource library and remain owned by the Session. The
file is synced before an acknowledged `context_notice` commits its reference
and formal-message cutoff in the existing event log. A lost ACK may follow a
successful commit, so its file is retained.

Main model messages are also retained incrementally in immutable
`state/outputs/history-*.gob` files. Model turns are committed before local tools
start; each tool result is committed independently as it completes, including
errors and cancellation results. The acknowledged `model_history` event contains
only the file reference, turn, part/index and token estimate. These records use
the actual tool arguments and model-visible results, rather than UI previews.
They do not create formal replies or change usage accounting. Intermediate
answers, question tool calls and collected child reports are retained too;
child transcripts remain in their own traces.

Question calls retain their original arguments, call IDs and provider replay
fields. A submitted form stays a user message in the visible conversation, but
its message event links it to the pending call. Model context restores it once
as that call's tool result, including in the first request after answering.
Pending questions survive restart, compaction and conversation rollback without
being marked interrupted. A new ordinary message supersedes an unanswered
question with an explicit unanswered result before that user input.

New runs merge this history with formal user inputs in event order. The formal
assistant projection of an already retained turn is excluded, including replies
committed before an interjection. Long final answers remain available in full.
A compaction checkpoint replaces all model history through its event sequence
and formal messages through its input cutoff, so older tools are not appended
again. If a Worker dies after recording a call but before recording its result,
restoration supplies an explicit unknown/interrupted tool outcome; it never
executes tools. New runs and Worker restarts use this same read path. System
instructions are rebuilt. Changing protocol, provider, endpoint or model strips
provider-owned signatures while preserving portable text/resource references/tool pairs.
Visible chat messages and tool artifacts are never rewritten by compaction.
Only newly submitted attachments or explicit `read_resource` results load
originals into the next request copy, subject to the model's image capability.
After a successful request, later requests retain metadata and hashes until the
model explicitly reads an original again. Switching to a text-only model does
not remove the canonical references. An image omitted
by compaction remains accessible through `list_resources` and `read_resource`;
see [resource storage](resources.md).

## Manual flow and frontend

`POST /api/sessions/:id/compact` takes optional `modelKey` and `reasoningEffort`.
It reserves the session execution slot atomically. Active execution rejects it;
normal send/interjection and a second compaction cannot overlap it. A successful
manual recovery releases a paused empty queue so the next explicit send starts
normally; existing pending inputs retain their paused state. It produces
no user/assistant message and never launches queued input on completion.
Cancellation, Worker loss, and restart release the slot through the same
terminal transaction. The committed checkpoint is the success boundary.

`GET /api/sessions/:id/context` and ordered `context_status`/`context_notice`
events expose progress and outcome. The frontend compares event sequences,
retains its session cancellation guards, and does not let a late POST restore
an older status. There is no capacity, remaining-token, or percentage display. The composer's
context status shows progress, outcomes and an estimated token total for the retained main context,
including instructions, summaries, messages and tool content. Runtime
`context_usage` events and committed compaction notices update the total;
`GET /api/sessions/:id/context-usage` restores it outside the recent event window.
While idle, the same context assembly used for the next request recomputes the
estimate, including retained tool turns from completed or interrupted runs. This number
is never cumulative request usage or an automatic compaction threshold. After manual
success, Continue is offered in the placeholder only while idle with an empty
draft and no images. Activating a conversation focuses its enabled composer;
pressing Tab fills the draft without sending. Shift+Tab and Tab without a suggestion retain normal focus navigation.
A newer user message dismisses
the suggestion, including after reload. Failed runs retain a copyable error and
guidance to investigate in a new conversation; the manual action remains
available after the failed run has ended.
