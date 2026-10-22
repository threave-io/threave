# Asynchronous agent questions

Codex can emit `item/completed` with an `agentMessage` whose `delivery` is
`async` and whose `questions` contain titles and optional string choices.
Gorchestra normalizes this to `agent.input.requested`, reusing `UserInputCard`
and the existing answer endpoint. These questions do not pause activity indicators
or replace the composer's draft/Queue action. Free-text answers remain available.

The optional provider-agnostic `AsyncUserInputBroker` registers a delivery
callback. The Codex callback sends `turn/steer` with the original `threadId` and
`expectedTurnId`; the normal event reader routes its acknowledgement while tools
and messages continue streaming. A finished or cancelled run closes its requests.

Answer lifecycle:

- Claim the request so competing clients cannot submit it twice.
- Persist `agent.input.submitted` before delivering an asynchronous answer.
- Persist `agent.input.answered` only after provider acknowledgement.
- On unconfirmed delivery, persist `agent.input.failed`; do not automatically
  resend, because the provider may already have received the answer.
- The older blocking question flow keeps its existing wait-and-answer behavior.

History responses include a separate `input_events` control snapshot, at the
page's server sequence watermark. This lets a new client restore unanswered
questions from long turns without downloading the entire transcript. The client
retains controls independently of bounded history and keeps resolution tombstones
to prevent a racing history response from resurrecting answered questions.

Coverage includes fake app-server steering, ongoing streaming, stale turns,
competing answers, persistence failures, cancellation, bounded-history replay,
existing card choices/free text, and composer draft preservation. Browser QA uses
synthetic sessions; physical iOS and a live model round-trip remain manual checks.

## Send now (generic steering)

During a Codex or Claude message run, the composer also offers **Send now**, without needing
an open question. Cmd/Ctrl+Enter sends to the active run; plain Enter and
Cmd/Ctrl+Shift+Enter still queue the draft for a later run. Shift+Enter inserts a
newline. Queue and Stop retain their existing behavior. Narrow layouts use an
accessible send icon and wrap controls when needed.

Send now accepts text, images, and selected skills, but retains the active run's
model and settings. It is disabled offline, while a blocking question awaits an
answer, or while another submission is pending. Other adapters keep their current
composer behavior until they support steering.

The normal message endpoint accepts `steer: true`, `expected_run_id`, and a
`client_submission_id`. It cannot combine steering with queuing or settings
changes. The optional provider-neutral `SteeringBroker` registers a callback for
the exact Gorchestra run; Codex captures the original provider thread/turn IDs and
uses the same acknowledged `turn/steer` delivery path as async answers. A stale
target is rejected rather than applied to the next run.

Claude implements that same broker with UUID-correlated streaming input replays.
It can take the correction at the next processing opportunity; it does not cancel
the current tool or restart the provider. See [Claude input](claude-input.md#send-now)
for acknowledgement, queued-result handling, and local validation details.

The client saves the submission and its target run before clearing the draft.
The server reserves the existing durable submission receipt, persists
`user.message.steer.submitted` before delivery, and appends a normal
`user.message.completed` (with `delivery: "steer"`) only after provider acceptance.
That acknowledgment deduplicates the optimistic message. Unconfirmed delivery
records `user.message.steer.failed` and remains recoverable without automatic
resending; recovery retains the original run target even after a reload.

Coverage includes run-bound delivery and cleanup, persistence-before-delivery,
provider rejection, duplicate submissions, unchanged queues, keyboard shortcuts,
attachments/skills, and durable recovery against a successor run. Synthetic
browser checks verified click/keyboard submission and separate queuing at desktop,
390px, and 320px widths; these are not physical-device or live-model tests.
