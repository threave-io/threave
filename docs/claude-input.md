# Claude questions and image input

This implements the first two priorities from the [agent feature audit](agent-feature-matrix-2026-09-09.md).

Claude runs use bidirectional `stream-json` input, including in plan mode. Text and
runtime context travel on stdin. PNG, JPEG, GIF, and WebP attachments become
base64 image content blocks; image-only prompts are supported. Images are never
put into command arguments or interpreted as filesystem paths.

`AskUserQuestion` control requests use the shared `UserInputBroker` and question
card. Single-choice questions retain their existing immediate selection behavior.
Multiple-choice questions let the user toggle several options, add free text, and
confirm explicitly. The server validates cardinality, empty answers, duplicate
selections, and option membership. Answers are mapped back to Claude using its
exact original question text, with selected labels joined by a comma and space;
the original question input and extra metadata are preserved.

The adapter keeps consuming the provider stream while a question or approval is
pending. Waiter goroutines do not emit events or write to the provider; those
operations stay in the main stream loop. Claude withdrawals close the waiter and
persist `agent.input.cancelled`. The card disappears on live updates and replay,
including bounded-history snapshots. A withdrawal racing an already-persisted
answer does not decrement another question's pending count. Stopping the run,
provider exit, and persistence failures also release waiters. Cancellation can
terminate a provider that has stopped reading a large image from stdin.

Opening stdin does not change the selected tool-permission policy. Ordinary
approval cards are offered only under the `ask` policy. Unexpected approval
callbacks under `deny` or `bypass` are denied, rather than overriding Claude's
own checks. Claude's explicit `dontAsk` mode suppresses `AskUserQuestion` before
it reaches the adapter; use the normal interactive policy or plan mode when
clarifying questions are wanted. Explicit provider deny rules remain in effect.

Background-agent lifecycle management remains a separate audit follow-up.

## Send now

Claude uses the same optional `SteeringBroker`, message endpoint, run targeting,
durable submission receipts, and composer controls as Codex. Text, images, and
selected skill references travel over the existing process's stdin. The active
model, permissions, and run identity are retained; no stop-and-resume is performed.

Message runs enable `--replay-user-messages`. Each injected input has a unique
UUID, and only a main-session `user` replay with `isReplay: true` and that UUID
confirms delivery. A successful pipe write alone does not confirm it. This is
provider input acceptance, not proof that the model has acted on the correction.
Claude can consume input at a subsequent opportunity rather than interrupting an
in-flight tool. Replays are consumed as acknowledgements without duplicating text
or image data into history. Input replay lines support the API's attachment sizes.

If a result precedes a pending replay, it is persisted as an intermediate provider
result and the run stays open for the injected input's response. Final completion,
provider failure, cancellation, or process exit unregisters steering and releases
delivery waiters. Expired or unconfirmed delivery follows the shared visible
failure/recovery path, without retries or silently moving input to a new run.
Compaction runs do not register steering.

Local tests cover matching and unrelated replays, merged/queued input, repeated
corrections, streaming during delivery, images/skills, large echoes, stale targets,
timeout, blocked stdin, provider failure/exit, cleanup, API deduplication, and the
shared composer. On 2026-10-22, local CLI 2.1.226 and Haiku passed both a direct
protocol probe and the Go adapter's live steering test. While Bash ran `sleep 8`,
the test injected a correction from `ORIGINAL` to `STEERED`. Claude replayed the
matching UUID after the tool completed, followed the correction, and produced one
terminal completion in the same Threave run. The adapter confirmed delivery after
about seven seconds. No interrupt or provider restart was needed.

The live test uses a temporary workspace, disables user/project settings and MCP
servers, and allows only Bash for the sleep command. It requires a logged-in CLI
and makes live model calls, so it is skipped by default. To repeat it locally:

```sh
THREAVE_LIVE_CLAUDE_STEERING=1 go test ./internal/agents/claude -run '^TestLiveClaudeSteering$' -count=1 -v
```

## Clear and compact

Claude sessions expose Clear and Compact in the context controls. Both require an
inactive, unarchived session. Clear detaches the provider session so the next message
starts a fresh Claude conversation; existing Threave activity remains visible.
Compact requires an existing provider session and runs Claude's native `/compact`
command with `--resume`. The command is sent alone, without runtime instructions,
skill text, or attachments. Provider failures remain visible as failed runs.
A `compact_boundary` followed by a successful result completes the action even
without an assistant reply. Clear resets the context meter; compaction invalidates
its old context estimate until Claude reports fresh usage.

Tests cover the API action lifecycle, provider reset, exact command input, resume,
empty successful compaction results, failures, UI confirmation, and usage replay.

Protocol references: [Claude user input](https://code.claude.com/docs/en/agent-sdk/user-input),
[streaming image input](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode),
and [permission evaluation](https://code.claude.com/docs/en/agent-sdk/permissions).

Model, effort, and plan selections are saved in server-owned session settings and
remain selected across runs and refreshes. The frontend normalizes omitted Claude
model/effort fields to the same Default/medium values used by the controls so an
initial server-settings update cannot suppress subsequent selection changes.

Validation covers fake CLI round trips, original question and metadata retention,
multi-select and custom answers, permission decisions, plan mode, image blocks,
provider withdrawal/exit, user cancellation, blocked stdin, persistence failure,
pending-count races, and replay. Browser checks exercise desktop, 390px, and 320px
layouts, selection, free text, scrolling, and confirmation. These are synthetic
interaction checks. The installed Claude `2.1.226` binary also successfully
acknowledged a streaming initialization request with plan mode and the stdio
control channel; no user prompt was sent in that check. A real model-driven
question and image-analysis run remains a manual acceptance check.

## Skill selection and context usage

Claude sessions expose the composer Skills picker and `$name` typeahead. Discovery
reads personal `.claude/skills` (or `CLAUDE_CONFIG_DIR/skills`) and repository/ancestor
`.claude/skills` directories, including symlinked skill folders. Personal skills
win name conflicts; nearer project directories win among project sources.
`user-invocable: false` entries are hidden. Discovery errors are returned alongside
valid skills, and refresh and submission validation reread the files.

Selected skills use the existing structured name/path references. Claude receives
an explicit request to read and follow those exact files; Gorchestra does not
expand skill bodies, run dynamic skill commands, or emulate native slash-command
execution. Commands, plugin catalogs, and the managed Skills screens are outside
this filesystem picker integration.

The context meter reduces main-session message usage across persisted events.
Message-start and message-delta snapshots merge without double counting, while
result usage remains a separate run total. The context limit comes from the active
model's result metadata, not the first model in the result map. A known limit is
retained across updates for that model and invalidated when the model changes.
Until it is reported, the meter displays "limit unknown". Truncated histories with
only run totals or output deltas show current context as unavailable. This remains
a token-based context estimate, not a native `/context` snapshot.
