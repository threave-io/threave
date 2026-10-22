import type { AgentEvent } from '@/lib/api'
import {
  activeRunActivity,
  activeStreamingResponse,
  activeThinking,
  activeToolActivity,
  appendEvent,
  appendEvents,
  buildChatTranscript,
  buildChatTimeline,
  groupEvents,
  isDebugOnlyEvent,
  lastSeq,
  latestTokenUsage,
  pendingUserInputRequest,
  pendingPermissionRequests,
  shouldRefreshWorkspaceFilesForEvent,
  statusFromEvent,
} from '@/lib/events'

function event(
  seq: number,
  type = 'agent.message.delta',
  payload: Record<string, unknown> = { text: `event ${seq}` },
): AgentEvent {
  return {
    id: `evt_${seq}`,
    session_id: 'sess_test',
    seq,
    type,
    role: 'assistant',
    status: type.endsWith('.completed') ? 'completed' : 'delta',
    payload,
    created_at: '2026-06-12T16:00:00Z',
  }
}

function failedEvent(seq: number, type = 'agent.run.failed', payload: Record<string, unknown> = { error: 'run failed' }) {
  return {
    ...event(seq, type, payload),
    status: 'failed',
  }
}

test('classifies provider diagnostics consistently with the server stream filter', () => {
  expect(isDebugOnlyEvent(event(1, 'agent.log.delta'))).toBe(true)
  expect(isDebugOnlyEvent(event(2, 'provider.codex.event', {
    provider_event_type: 'thread/tokenUsage/updated',
  }))).toBe(false)
  expect(isDebugOnlyEvent(event(3, 'provider.codex.event', {
    provider_event_type: 'item/completed',
    raw: { item: { type: 'plan' } },
  }))).toBe(false)
  expect(isDebugOnlyEvent(event(4, 'provider.codex.event', {
    provider_event_type: 'item/completed',
    raw: { item: { type: 'commandExecution' } },
  }))).toBe(true)
})

test('async questions replay, stay stable, and resolve independently while streaming continues', () => {
  const question = (seq: number, requestID: string, delivery?: string) => event(seq, 'agent.input.requested', {
    request_id: requestID, delivery, text: `Question ${requestID}`,
    questions: [{ id: 'q1', question: `Question ${requestID}`, is_other: true, options: [] }],
  })
  const events = [event(1, 'agent.run.started'), event(2, 'agent.thinking.started'), question(3, 'first', 'async'), question(4, 'second', 'async')]
  expect(activeThinking(events)).toBe(true)
  expect(pendingUserInputRequest([...events].reverse())).toMatchObject({ requestID: 'first', delivery: 'async' })
  expect(pendingUserInputRequest([...events, question(5, 'blocking')])).toMatchObject({ requestID: 'blocking' })
  const submitted = event(5, 'agent.input.submitted', { request_id: 'first' })
  expect(pendingUserInputRequest([...events, submitted])).toMatchObject({ requestID: 'first', submitting: true })
  const failed = event(6, 'agent.input.failed', { request_id: 'first', error: 'Not confirmed' })
  expect(pendingUserInputRequest([...events, submitted, failed])).toMatchObject({ requestID: 'second' })
  expect(pendingUserInputRequest([...events, submitted, failed, event(7, 'agent.input.answered', { request_id: 'second' })])).toMatchObject({ requestID: 'first', submitting: false, deliveryError: 'Not confirmed' })
  expect(pendingUserInputRequest([...events, event(6, 'agent.input.answered', { request_id: 'first' })])).toMatchObject({ requestID: 'second' })
  expect(pendingUserInputRequest([...events, event(7, 'agent.run.completed')])).toBeNull()
  expect(pendingUserInputRequest([...events, event(7, 'agent.run.cancelled')])).toBeNull()
})

test('Claude multi-select questions replay and provider withdrawals resolve only their card', () => {
  const requested = event(1, 'agent.input.requested', {
    provider: 'claude', request_id: 'claude_question',
    questions: [{ id: 'question_1', question: 'Which sections?', multi_select: true, is_other: true, options: [{ label: 'Tests' }] }],
  })
  expect(pendingUserInputRequest([requested])).toMatchObject({
    provider: 'claude', questions: [{ id: 'question_1', multi_select: true, is_other: true }],
  })
  const unrelated = event(2, 'agent.input.cancelled', { request_id: 'different_question' })
  expect(pendingUserInputRequest([requested, unrelated])).not.toBeNull()
  const cancelled = event(3, 'agent.input.cancelled', { request_id: 'claude_question' })
  expect(pendingUserInputRequest([cancelled, requested, unrelated])).toBeNull()
})

test('async question and confirmed answer remain in transcript history', () => {
  const events = [
    event(1, 'agent.input.requested', { request_id: 'first', delivery: 'async', text: 'Which color?', questions: [{ id: 'q1', question: 'Which color?', is_other: true, options: [] }] }),
    event(2, 'agent.input.submitted', { request_id: 'first', delivery: 'async' }),
    event(3, 'agent.input.answered', { request_id: 'first', delivery: 'async', text: 'Blue' }),
  ]
  const messages = buildChatTranscript(events)
  expect(messages.map((message) => [message.role, message.text])).toEqual([['assistant', 'Which color?'], ['user', 'Blue']])
})

function timedEvent(seq: number, type: string, createdAt: string, payload: Record<string, unknown> = {}) {
  return {
    ...event(seq, type, payload),
    status: type.endsWith('.completed') ? 'completed' : 'started',
    created_at: createdAt,
  }
}

test('event reducer appends events in sequence order', () => {
  const events = appendEvents([], [event(3), event(1), event(2)])

  expect(events.map((item) => item.seq)).toEqual([1, 2, 3])
  expect(lastSeq(events)).toBe(3)
})

test('event reducer dedupes by sequence', () => {
  const events = appendEvent(
    [event(1, 'agent.message.delta', { text: 'first' })],
    event(1, 'agent.message.delta', { text: 'second' }),
  )

  expect(events).toHaveLength(1)
  expect(events[0].payload).toEqual({ text: 'first' })
})

test('completed snapshots replace matching transient deltas', () => {
  const events = appendEvents([], [
    { ...event(1, 'agent.message.delta', { message_id: 'msg_1', text: 'Hel' }), transient: true },
    { ...event(2, 'agent.message.delta', { message_id: 'msg_1', text: 'lo' }), transient: true },
    event(3, 'agent.message.completed', { message_id: 'msg_1', text: 'Hello' }),
  ])

  expect(events.map((item) => item.seq)).toEqual([3])
  expect(events[0].payload).toEqual({ message_id: 'msg_1', text: 'Hello' })
})

test('completed snapshots preserve transient deltas for other identities', () => {
  const events = appendEvents([], [
    { ...event(1, 'agent.message.delta', { message_id: 'msg_1', text: 'First' }), transient: true },
    { ...event(2, 'agent.message.delta', { message_id: 'msg_2', text: 'Second' }), transient: true },
    event(3, 'agent.message.completed', { message_id: 'msg_2', text: 'Second' }),
  ])

  expect(events.map((item) => item.seq)).toEqual([1, 3])
})

test('event groups coalesce consecutive agent deltas and keep completion boundaries', () => {
  const groups = groupEvents([
    event(1, 'agent.message.delta', { text: 'Hello' }),
    event(2, 'agent.message.delta', { text: ' world' }),
    event(3, 'agent.message.completed', { text: '' }),
  ])

  expect(groups).toHaveLength(2)
  expect(groups[0].kind).toBe('agent-message')
  expect(groups[0].text).toBe('Hello world')
  expect(groups[0].startSeq).toBe(1)
  expect(groups[0].endSeq).toBe(2)
  expect(groups[1].events[0].type).toBe('agent.message.completed')
})

test('event groups connect tool start and completion by payload identifier', () => {
  const groups = groupEvents([
    event(1, 'tool.call.started', { item_id: 'tool_1', tool: 'shell', command: 'bun test' }),
    event(2, 'tool.call.completed', { item_id: 'tool_1', output: 'ok' }),
  ])

  expect(groups).toHaveLength(1)
  expect(groups[0].kind).toBe('tool-call')
  expect(groups[0].events.map((item) => item.type)).toEqual(['tool.call.started', 'tool.call.completed'])
  expect(groups[0].defaultOpen).toBe(false)
})

test('event groups connect nearby anonymous tool events', () => {
  const groups = groupEvents([
    event(1, 'tool.call.started', { command: 'go test ./...' }),
    event(2, 'tool.call.completed', { output: 'ok' }),
  ])

  expect(groups).toHaveLength(1)
  expect(groups[0].kind).toBe('tool-call')
})

test('event groups connect file changes by payload identifier across interleaved events', () => {
  const groups = groupEvents([
    event(1, 'file.change.started', {
      item_id: 'edit_1',
      paths: ['/repo/README.md'],
    }),
    event(2, 'provider.codex.event', { provider_event_type: 'thread/tokenUsage/updated' }),
    event(3, 'file.change.delta', {
      item_id: 'edit_1',
      changes: [
        {
          path: '/repo/README.md',
          patch: '@@ -1 +1 @@\n-old\n+new',
        },
      ],
    }),
    event(4, 'provider.codex.event', { provider_event_type: 'account/rateLimits/updated' }),
    event(5, 'file.change.completed', {
      item_id: 'edit_1',
      paths: ['/repo/README.md'],
    }),
  ])

  const fileGroups = groups.filter((group) => group.kind === 'file-change')

  expect(fileGroups).toHaveLength(1)
  expect(fileGroups[0]).toMatchObject({
    id: 'file-change-edit_1',
    label: 'README.md',
    status: 'completed',
    paths: ['/repo/README.md'],
  })
  expect(fileGroups[0].events.map((item) => item.seq)).toEqual([1, 3, 5])
})

test('event groups combine consecutive log output', () => {
  const groups = groupEvents([
    event(1, 'agent.log.delta', { text: 'line 1\n' }),
    event(2, 'agent.log.delta', { text: 'line 2\n' }),
  ])

  expect(groups).toHaveLength(1)
  expect(groups[0].kind).toBe('log')
  expect(groups[0].text).toBe('line 1\nline 2\n')
})

test('failed and unknown provider event groups use the expected default disclosure', () => {
  const groups = groupEvents([
    event(1, 'provider.codex.parse_error', { error: 'invalid JSON' }),
    event(2, 'provider.codex.event', { provider_event_type: 'thread/compacted' }),
  ])

  expect(groups[0].kind).toBe('error')
  expect(groups[0].defaultOpen).toBe(true)
  expect(groups[1].kind).toBe('unknown')
  expect(groups[1].label).toBe('thread/compacted')
  expect(groups[1].defaultOpen).toBe(false)
})

test('session status update events expose their payload status', () => {
  expect(statusFromEvent(event(1, 'session.status.updated', { status: 'idle' }))).toBe('idle')
  expect(statusFromEvent(event(2, 'session.status.updated', { status: 'bogus' }))).toBeNull()
})

test('failed session status updates are not treated as transcript errors', () => {
  const groups = groupEvents([
    failedEvent(1, 'agent.run.failed', { error: 'Selected model is at capacity.' }),
    failedEvent(2, 'session.status.updated', { status: 'failed' }),
  ])

  expect(groups.map((group) => group.kind)).toEqual(['error', 'unknown'])
  expect(groups[1]?.label).toBe('Session status')
})

test('workspace file refreshes are derived from file changes and mutating git commands', () => {
  expect(shouldRefreshWorkspaceFilesForEvent(event(1, 'file.change.completed', { paths: ['web/src/App.tsx'] }))).toBe(
    true,
  )

  expect(
    shouldRefreshWorkspaceFilesForEvent(
      event(2, 'tool.call.completed', {
        item_type: 'commandExecution',
        command: `/bin/zsh -lc "git add web/src/App.tsx && git commit -F - <<'EOF'
Refresh files after git commands
EOF
git push"`,
      }),
    ),
  ).toBe(true)

  expect(
    shouldRefreshWorkspaceFilesForEvent(
      event(3, 'tool.call.completed', {
        item_type: 'commandExecution',
        command: "/bin/zsh -lc 'cd /repo && git pull --rebase'",
      }),
    ),
  ).toBe(true)

  expect(
    shouldRefreshWorkspaceFilesForEvent(
      event(4, 'tool.call.completed', {
        item_type: 'commandExecution',
        command: `/bin/zsh -lc 'git tag -fa v0.1.1 -m "Gorchestra v0.1.1" && git push --force origin v0.1.1'`,
      }),
    ),
  ).toBe(true)

  expect(
    shouldRefreshWorkspaceFilesForEvent(
      event(5, 'tool.call.completed', {
        item_type: 'commandExecution',
        command: "/bin/zsh -lc 'git status --short && git log --oneline --decorate -7 && git rev-parse HEAD'",
      }),
    ),
  ).toBe(false)

  expect(
    shouldRefreshWorkspaceFilesForEvent(event(6, 'tool.call.started', { command: "/bin/zsh -lc 'git pull'" })),
  ).toBe(false)
})

test('pending user input request is derived from replayed events', () => {
  const requested = event(2, 'agent.input.requested', {
    request_id: 'call_test',
    provider: 'codex',
    provider_event_type: 'item/tool/requestUserInput',
    thread_id: 'thread_test',
    turn_id: 'turn_test',
    item_id: 'call_test',
    questions: [
      {
        id: 'question_test',
        header: 'Pick',
        question: 'Pick one',
        is_other: false,
        is_secret: false,
        options: [{ label: 'Beta', description: 'Second' }],
      },
    ],
  })

  expect(pendingUserInputRequest([requested])).toMatchObject({
    requestID: 'call_test',
    questions: [{ id: 'question_test', question: 'Pick one' }],
  })

  expect(pendingUserInputRequest([requested, event(3, 'agent.input.answered', { request_id: 'call_test' })])).toBeNull()
})

test('pending permission requests retain stable option ids and resolve independently', () => {
  const first = event(2, 'agent.permission.requested', { request_id: 'perm_1', provider: 'codex', kind: 'command', title: 'Approve command', command: 'git push', options: [{ id: 'accept', label: 'Allow once', decision: 'allow', scope: 'once' }] })
  const second = event(3, 'agent.permission.requested', { request_id: 'perm_2', provider: 'opencode', kind: 'tool', title: 'Write file', options: [{ id: 'reject-once', label: 'Reject', decision: 'deny', scope: 'once' }] })
  expect(pendingPermissionRequests([first, second]).map((request) => request.request_id)).toEqual(['perm_1', 'perm_2'])
  expect(pendingPermissionRequests([first, second, event(4, 'agent.permission.resolved', { request_id: 'perm_1', option_id: 'accept' })])).toMatchObject([{ request_id: 'perm_2', options: [{ id: 'reject-once' }] }])
})

test('chat timeline only includes hidden debug events when enabled', () => {
  const events = [
    event(1, 'user.message.completed', { text: 'Hello' }),
    event(2, 'session.status.updated', { status: 'running' }),
    event(3, 'agent.log.delta', { text: 'debug line' }),
    event(4, 'tool.call.started', { item_id: 'tool_1', command: 'go test ./...' }),
    event(5, 'tool.call.completed', { item_id: 'tool_1', output: 'ok' }),
    event(6, 'agent.message.completed', { text: 'Done' }),
  ]

  expect(buildChatTimeline(events, false).map((item) => item.kind)).toEqual(['message', 'message'])

  const debugItems = buildChatTimeline(events, true).filter((item) => item.kind === 'debug')

  expect(debugItems.map((item) => item.event.label)).toEqual(['Session status', 'Log'])
})

test('chat timeline labels provider debug events with provider event type', () => {
  const debugItems = buildChatTimeline(
    [event(1, 'provider.codex.event', { provider_event_type: 'turn/completed' })],
    true,
  ).filter((item) => item.kind === 'debug')

  expect(debugItems.map((item) => item.event.label)).toEqual(['turn/completed'])
})

test('latest token usage is derived from codex provider events', () => {
  const usage = latestTokenUsage([
    event(1, 'provider.codex.event', {
      provider_event_type: 'thread/tokenUsage/updated',
      raw: tokenUsageRaw(1000, 500),
    }),
    event(2, 'provider.codex.event', { provider_event_type: 'turn/completed' }),
    event(3, 'provider.codex.event', {
      provider_event_type: 'thread/tokenUsage/updated',
      raw: tokenUsageRaw(13903, 19),
    }),
  ])

  expect(usage).toMatchObject({
    total: {
      totalTokens: 13903,
      inputTokens: 13884,
      cachedInputTokens: 4480,
      outputTokens: 19,
      reasoningOutputTokens: 0,
    },
    last: {
      totalTokens: 13903,
      inputTokens: 13884,
      cachedInputTokens: 4480,
      outputTokens: 19,
      reasoningOutputTokens: 0,
    },
    modelContextWindow: 258400,
    seq: 3,
  })
})

test('latest token usage is derived from claude usage payloads', () => {
  const usage = latestTokenUsage([
    event(1, 'provider.claude.event', {
      provider: 'claude',
      provider_event_type: 'message_delta',
      usage: {
        input_tokens: 6,
        cache_creation_input_tokens: 10246,
        cache_read_input_tokens: 15713,
        output_tokens: 15,
        output_tokens_details: { thinking_tokens: 0 },
      },
    }),
    event(2, 'agent.run.completed', {
      provider: 'claude',
      provider_event_type: 'result',
      usage: {
        input_tokens: 8,
        cache_creation_input_tokens: 10,
        cache_read_input_tokens: 20,
        output_tokens: 30,
        output_tokens_details: { thinking_tokens: 4 },
      },
      model_usage: {
        opus: { contextWindow: 1000000 },
      },
    }),
  ])

  expect(usage).toMatchObject({
    total: {
      totalTokens: 68,
      inputTokens: 38,
      cachedInputTokens: 20,
      outputTokens: 30,
      reasoningOutputTokens: 4,
    },
    modelContextWindow: 1000000,
    seq: 2,
  })
})

test('latest token usage is derived from opencode usage updates', () => {
  const usage = latestTokenUsage([
    event(1, 'provider.opencode.event', {
      provider: 'opencode',
      provider_event_type: 'usage_update',
      raw_update: {
        sessionUpdate: 'usage_update',
        used: 53_000,
        size: 200_000,
        cost: { amount: 0.1234, currency: 'USD' },
      },
    }),
  ])

  expect(usage).toMatchObject({
    kind: 'context',
    total: {
      totalTokens: 53_000,
      inputTokens: 0,
      cachedInputTokens: 0,
      outputTokens: 0,
      reasoningOutputTokens: 0,
    },
    last: {
      totalTokens: 53_000,
      inputTokens: 0,
      cachedInputTokens: 0,
      outputTokens: 0,
      reasoningOutputTokens: 0,
    },
    modelContextWindow: 200_000,
    cost: { amount: 0.1234, currency: 'USD' },
    seq: 1,
  })
})

test('active thinking clears when codex reasoning item completes', () => {
  expect(activeThinking([event(1, 'agent.status.started', { provider_event_type: 'turn/started' })])).toBe(true)

  expect(
    activeThinking([
      event(1, 'agent.status.started', { provider_event_type: 'turn/started' }),
      event(2, 'agent.thinking.completed', {
        provider_event_type: 'item/completed',
        item_type: 'reasoning',
        item_id: 'rs_1',
        text: '',
      }),
    ]),
  ).toBe(false)
})

test('active thinking tracks reasoning item deltas by item id', () => {
  expect(
    activeThinking([
      event(1, 'agent.thinking.delta', { item_id: 'rs_1', text: 'checking' }),
      event(2, 'agent.thinking.delta', { item_id: 'rs_2', text: 'planning' }),
      event(3, 'agent.thinking.completed', { item_id: 'rs_1', text: '' }),
    ]),
  ).toBe(true)

  expect(
    activeThinking([
      event(1, 'agent.thinking.delta', { item_id: 'rs_1', text: 'checking' }),
      event(2, 'agent.thinking.completed', { item_id: 'rs_1', text: '' }),
    ]),
  ).toBe(false)
})

test('active thinking restarts when a new reasoning item starts after completion', () => {
  expect(
    activeThinking([
      event(1, 'agent.status.started', { provider_event_type: 'turn/started' }),
      event(2, 'agent.thinking.completed', {
        provider_event_type: 'item/completed',
        item_type: 'reasoning',
        item_id: 'rs_1',
        text: '',
      }),
      event(3, 'agent.thinking.started', {
        provider_event_type: 'item/started',
        item_type: 'reasoning',
        item_id: 'rs_2',
      }),
    ]),
  ).toBe(true)

  expect(
    activeThinking([
      event(1, 'agent.thinking.started', { item_id: 'rs_2' }),
      event(2, 'agent.thinking.completed', { item_id: 'rs_2', text: '' }),
    ]),
  ).toBe(false)
})

test('active run activity starts from run start and ignores provider noise', () => {
  const activity = activeRunActivity([
    timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
    timedEvent(2, 'provider.codex.event', '2026-06-12T16:00:05Z', { provider_event_type: 'thread/tokenUsage/updated' }),
    timedEvent(3, 'session.status.updated', '2026-06-12T16:00:06Z', { status: 'running' }),
  ])

  expect(activity).toEqual({
    runStartedAt: '2026-06-12T16:00:00Z',
    lastVisibleActivityAt: '2026-06-12T16:00:00Z',
    lastVisibleActivitySeq: 1,
  })
})

test('active run activity advances on visible agent, tool, and file events', () => {
  const activity = activeRunActivity([
    timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
    timedEvent(2, 'agent.status.started', '2026-06-12T16:00:01Z'),
    timedEvent(3, 'agent.thinking.completed', '2026-06-12T16:00:04Z', { item_id: 'rs_1' }),
    timedEvent(4, 'tool.call.started', '2026-06-12T16:00:08Z'),
    timedEvent(5, 'file.change.completed', '2026-06-12T16:00:12Z'),
  ])

  expect(activity?.lastVisibleActivityAt).toBe('2026-06-12T16:00:12Z')
  expect(activity?.lastVisibleActivitySeq).toBe(5)
})

test('active run activity clears on terminal run events', () => {
  expect(
    activeRunActivity([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'tool.call.started', '2026-06-12T16:00:02Z'),
      timedEvent(3, 'agent.run.completed', '2026-06-12T16:00:10Z'),
    ]),
  ).toBeNull()
})

test('active streaming response tracks assistant and plan text streams during a run', () => {
  expect(
    activeStreamingResponse([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'agent.message.delta', '2026-06-12T16:00:01Z', { item_id: 'msg_1', text: 'Working' }),
    ]),
  ).toBe(true)

  expect(
    activeStreamingResponse([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'agent.plan.delta', '2026-06-12T16:00:01Z', { item_id: 'plan_1', text: '- Check' }),
    ]),
  ).toBe(true)

  expect(
    activeStreamingResponse([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'agent.message.delta', '2026-06-12T16:00:01Z', { item_id: 'msg_1', text: 'Working' }),
      timedEvent(3, 'agent.message.completed', '2026-06-12T16:00:02Z', { item_id: 'msg_1', text: 'Working' }),
    ]),
  ).toBe(false)
})

test('active streaming response ignores active tools and clears on terminal run events', () => {
  expect(
    activeStreamingResponse([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'tool.call.started', '2026-06-12T16:00:01Z', { item_id: 'tool_1' }),
    ]),
  ).toBe(false)

  expect(
    activeStreamingResponse([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'agent.message.delta', '2026-06-12T16:00:01Z', { item_id: 'msg_1', text: 'Working' }),
      timedEvent(3, 'agent.run.cancelled', '2026-06-12T16:00:02Z'),
    ]),
  ).toBe(false)
})

test('active tool activity tracks running tool and file-change groups', () => {
  expect(
    activeToolActivity([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'tool.call.started', '2026-06-12T16:00:01Z', { item_id: 'tool_1' }),
    ]),
  ).toBe(true)

  expect(
    activeToolActivity([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'file.change.started', '2026-06-12T16:00:01Z', { item_id: 'edit_1' }),
    ]),
  ).toBe(true)
})

test('active tool activity clears completed groups and terminal runs', () => {
  expect(
    activeToolActivity([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'tool.call.started', '2026-06-12T16:00:01Z', { item_id: 'tool_1' }),
      timedEvent(3, 'tool.call.completed', '2026-06-12T16:00:02Z', { item_id: 'tool_1' }),
    ]),
  ).toBe(false)

  expect(
    activeToolActivity([
      timedEvent(1, 'agent.run.started', '2026-06-12T16:00:00Z'),
      timedEvent(2, 'tool.call.started', '2026-06-12T16:00:01Z', { item_id: 'tool_1' }),
      timedEvent(3, 'agent.run.failed', '2026-06-12T16:00:02Z'),
    ]),
  ).toBe(false)
})

test('chat transcript merges streaming assistant deltas with completion text', () => {
  const transcript = buildChatTranscript([
    event(1, 'user.message.completed', { text: 'Hello' }),
    event(2, 'agent.message.delta', { text: 'Hi' }),
    event(3, 'agent.message.delta', { text: ' there' }),
    event(4, 'agent.message.completed', { text: 'Hi there' }),
  ])

  expect(transcript).toHaveLength(2)
  expect(transcript[0]).toMatchObject({ role: 'user', text: 'Hello' })
  expect(transcript[1]).toMatchObject({ role: 'assistant', text: 'Hi there', streaming: false })
})

test('chat transcript hides OpenCode compaction summaries', () => {
  const compactionSummary = `## Goal
- Keep the session moving
## Constraints & Preferences
- (none)
## Progress
### Done
- Read the request
### In Progress
- Continue the task
### Blocked
- (none)
## Key Decisions
- (none)
## Next Steps
- Answer the user
## Critical Context
- Session runs inside Gorchestra
## Relevant Files
- (none)`

  const transcript = buildChatTranscript([
    event(1, 'user.message.completed', { text: 'Who are you?' }),
    event(2, 'agent.message.delta', {
      provider: 'opencode',
      item_id: 'message:summary',
      text: compactionSummary.slice(0, 120),
    }),
    event(3, 'agent.message.delta', {
      provider: 'opencode',
      item_id: 'message:summary',
      text: compactionSummary.slice(120),
    }),
    event(4, 'agent.message.delta', {
      provider: 'opencode',
      item_id: 'message:answer',
      text: 'I am the OpenCode agent running through Gorchestra.',
    }),
  ])

  expect(transcript.map((message) => message.text)).toEqual([
    'Who are you?',
    'I am the OpenCode agent running through Gorchestra.',
  ])
})

test('chat transcript preserves similarly structured non-compaction responses', () => {
  const response = `## Goal
- Explain the project
## Constraints & Preferences
- Be concise
## Progress
### Done
- Reviewed the repository`

  expect(buildChatTranscript([
    event(1, 'agent.message.completed', { provider: 'opencode', item_id: 'message:partial', text: response }),
  ])).toHaveLength(1)

  expect(buildChatTranscript([
    event(2, 'agent.message.completed', {
      provider: 'codex',
      item_id: 'message:codex',
      text: `${response}\n### In Progress\n- None\n### Blocked\n- None\n## Key Decisions\n- None\n## Next Steps\n- None\n## Critical Context\n- None\n## Relevant Files\n- None`,
    }),
  ])).toHaveLength(1)
})

test('chat transcript measures completed assistant responses from the run start', () => {
  const transcript = buildChatTranscript([
    timedEvent(1, 'user.message.completed', '2026-06-12T16:03:25Z', { text: 'Do the work' }),
    timedEvent(2, 'agent.run.started', '2026-06-12T16:03:26Z'),
    timedEvent(3, 'tool.call.completed', '2026-06-12T16:03:50Z', { item_id: 'tool_1', command: 'go test' }),
    timedEvent(4, 'agent.message.completed', '2026-06-12T16:04:00Z', {
      item_id: 'msg_1',
      text: 'Done',
    }),
  ])

  expect(transcript[1]).toMatchObject({
    role: 'assistant',
    text: 'Done',
    completedAt: '2026-06-12T16:04:00Z',
    durationMs: 34_000,
  })
})

test('chat transcript measures later responses from the previous response instead of cumulatively', () => {
  const transcript = buildChatTranscript([
    timedEvent(1, 'user.message.completed', '2026-06-12T16:03:25Z', { text: 'Do the work' }),
    timedEvent(2, 'agent.run.started', '2026-06-12T16:03:26Z'),
    timedEvent(3, 'agent.message.completed', '2026-06-12T16:03:36Z', {
      item_id: 'msg_1',
      text: 'First update',
    }),
    timedEvent(4, 'agent.message.completed', '2026-06-12T16:04:00Z', {
      item_id: 'msg_2',
      text: 'Final update',
    }),
  ])

  expect(transcript.slice(1).map((message) => message.durationMs)).toEqual([10_000, 24_000])
})

test('chat transcript uses event attachment URLs instead of inline image data', () => {
  const transcript = buildChatTranscript([
    event(7, 'user.message.completed', {
      text: 'see image',
      attachments: [
        {
          name: 'image.png',
          media_type: 'image/png',
          data_url: 'data:image/png;base64,[gorchestra truncated 100 bytes from this field for browser display]',
          size_bytes: 1234,
        },
      ],
    }),
  ])

  expect(transcript).toHaveLength(1)
  expect(transcript[0].attachments).toEqual([
    {
      name: 'image.png',
      mediaType: 'image/png',
      dataURL: 'data:image/png;base64,[gorchestra truncated 100 bytes from this field for browser display]',
      sourceURL: '/api/sessions/sess_test/events/7/attachments/0',
      sizeBytes: 1234,
    },
  ])
})

test('chat transcript retains skill-only user messages as structured invocations', () => {
  const transcript = buildChatTranscript([
    event(8, 'user.message.completed', {
      text: '',
      skills: [
        { name: 'openai-docs', path: '/skills/user/openai-docs/SKILL.md' },
        { name: '', path: '/skills/invalid/SKILL.md' },
      ],
    }),
  ])

  expect(transcript).toHaveLength(1)
  expect(transcript[0]).toMatchObject({ role: 'user', text: '' })
  expect(transcript[0].skills).toEqual([
    { name: 'openai-docs', path: '/skills/user/openai-docs/SKILL.md' },
  ])
})

test('chat transcript merges claude completion with prior deltas missing message id', () => {
  const transcript = buildChatTranscript([
    event(1, 'user.message.completed', { text: 'Hello' }),
    event(2, 'agent.message.delta', { text: 'Hi' }),
    event(3, 'agent.message.delta', { text: '! What can I help you with?' }),
    event(4, 'agent.message.completed', { message_id: 'msg_1', text: 'Hi! What can I help you with?' }),
  ])

  expect(transcript).toHaveLength(2)
  expect(transcript[1]).toMatchObject({ role: 'assistant', text: 'Hi! What can I help you with?', streaming: false })
})

test('chat timeline renders run failures as non-message error rows', () => {
  const events = [
    event(1, 'user.message.completed', { text: 'Make the change' }),
    event(2, 'agent.message.completed', { text: 'I started the work.' }),
    failedEvent(3, 'agent.run.failed', { error: 'read codex app-server stdout: bufio.Scanner: token too long' }),
  ]
  const timeline = buildChatTimeline(events, false)

  expect(timeline.map((item) => item.kind)).toEqual(['message', 'message', 'error'])
  expect(buildChatTranscript(events).map((message) => message.text)).toEqual(['Make the change', 'I started the work.'])

  const errorItem = timeline[2]
  expect(errorItem?.kind).toBe('error')
  if (errorItem?.kind !== 'error') {
    throw new Error('expected error timeline item')
  }
  expect(errorItem.error).toMatchObject({
    label: 'Run failed',
    error: 'read codex app-server stdout: bufio.Scanner: token too long',
    status: 'failed',
    startSeq: 3,
    endSeq: 3,
  })
})

test('chat timeline renders session action markers as separators', () => {
  const events = [
    event(1, 'user.message.completed', { text: 'Hello' }),
    event(2, 'session.action.completed', { action: 'clear', text: 'Clear context' }),
    event(3, 'agent.message.completed', { text: 'Done' }),
  ]
  const timeline = buildChatTimeline(events, false)

  expect(timeline.map((item) => item.kind)).toEqual(['message', 'action', 'message'])
  expect(buildChatTranscript(events).map((message) => message.text)).toEqual(['Hello', 'Done'])

  const actionItem = timeline[1]
  expect(actionItem?.kind).toBe('action')
  if (actionItem?.kind !== 'action') {
    throw new Error('expected action timeline item')
  }
  expect(actionItem.action).toMatchObject({
    action: 'clear',
    label: 'CONVERSATION CLEARED',
    startSeq: 2,
    endSeq: 2,
  })
})

test('chat timeline includes old and new paths for workspace changes', () => {
  const timeline = buildChatTimeline(
    [
      event(1, 'session.action.completed', {
        action: 'workspace_changed',
        label: 'WORKSPACE CHANGED',
        previous_workspace_path: '/repo/old',
        workspace_path: '/repo/new',
      }),
    ],
    false,
  )

  const actionItem = timeline[0]
  expect(actionItem?.kind).toBe('action')
  if (actionItem?.kind !== 'action') {
    throw new Error('expected action timeline item')
  }
  expect(actionItem.action).toMatchObject({
    action: 'workspace_changed',
    label: 'WORKSPACE CHANGED',
    detail: '/repo/old -> /repo/new',
  })
})

test('chat timeline renders legacy user action markers as separators', () => {
  const timeline = buildChatTimeline(
    [event(1, 'user.action.completed', { action: 'compact', text: 'Compact context' })],
    false,
  )

  expect(timeline.map((item) => item.kind)).toEqual(['action'])

  const actionItem = timeline[0]
  expect(actionItem?.kind).toBe('action')
  if (actionItem?.kind !== 'action') {
    throw new Error('expected action timeline item')
  }
  expect(actionItem.action.label).toBe('CONVERSATION COMPACTED')
})

test('chat transcript renders structured plan events as visible plan messages', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.plan.delta', { item_id: 'plan_1', text: '# Plan\n' }),
    event(2, 'agent.plan.delta', { item_id: 'plan_1', text: '- Check the transcript\n' }),
    event(3, 'agent.plan.completed', { item_id: 'plan_1', text: '# Plan\n- Check the transcript\n' }),
  ])

  expect(transcript).toHaveLength(1)
  expect(transcript[0]).toMatchObject({
    role: 'assistant',
    label: 'Plan',
    variant: 'plan',
    text: '# Plan\n- Check the transcript\n',
    streaming: false,
  })
})

test('chat transcript renders legacy raw Codex plan provider events', () => {
  const transcript = buildChatTranscript([
    event(1, 'provider.codex.event', {
      provider_event_type: 'item/plan/delta',
      raw: { threadId: 'thread_1', turnId: 'turn_1', itemId: 'plan_1', delta: '# Plan\n' },
    }),
    event(2, 'provider.codex.event', {
      provider_event_type: 'item/plan/delta',
      raw: { threadId: 'thread_1', turnId: 'turn_1', itemId: 'plan_1', delta: '- Check the transcript\n' },
    }),
    event(3, 'provider.codex.event', {
      provider_event_type: 'item/completed',
      raw: {
        threadId: 'thread_1',
        turnId: 'turn_1',
        item: { type: 'plan', id: 'plan_1', text: '# Plan\n- Check the transcript\n' },
      },
    }),
  ])

  expect(transcript).toHaveLength(1)
  expect(transcript[0]).toMatchObject({
    role: 'assistant',
    label: 'Plan',
    variant: 'plan',
    text: '# Plan\n- Check the transcript\n',
  })
})

test('chat transcript groups tool calls under the assistant message', () => {
  const transcript = buildChatTranscript([
    event(1, 'user.message.completed', { text: 'Run tests' }),
    event(2, 'tool.call.started', { item_id: 'tool_1', command: 'go test ./...' }),
    event(3, 'tool.call.completed', { item_id: 'tool_1', output: 'ok' }),
    event(4, 'agent.message.completed', { text: 'Tests passed.' }),
  ])

  expect(transcript).toHaveLength(2)
  expect(transcript[1]).toMatchObject({ role: 'assistant', text: 'Tests passed.' })
  expect(transcript[1].tools).toHaveLength(1)
  expect(transcript[1].tools[0]).toMatchObject({
    label: 'Tool: go test ./...',
    status: 'completed',
    text: 'go test ./...\nok',
  })
})

test('chat transcript separates assistant message items and keeps tools in event order', () => {
  const transcript = buildChatTranscript([
    event(1, 'user.message.completed', { text: 'Split this into sections' }),
    event(2, 'agent.message.delta', { item_id: 'msg_1', text: 'Section 1' }),
    event(3, 'agent.message.completed', { item_id: 'msg_1', text: 'Section 1' }),
    event(4, 'tool.call.started', { item_id: 'tool_1', command: '/bin/zsh -lc pwd' }),
    event(5, 'tool.call.completed', { item_id: 'tool_1', output: '/repo' }),
    event(6, 'agent.message.delta', { item_id: 'msg_2', text: 'Section 2' }),
    event(7, 'agent.message.completed', { item_id: 'msg_2', text: 'Section 2' }),
    event(8, 'tool.call.started', { item_id: 'tool_2', command: "/bin/zsh -lc 'git status --short'" }),
    event(9, 'tool.call.completed', { item_id: 'tool_2', output: ' M file.ts' }),
    event(10, 'agent.message.completed', { item_id: 'msg_3', text: 'Done.' }),
  ])

  expect(transcript.map((message) => message.text)).toEqual([
    'Split this into sections',
    'Section 1',
    'Section 2',
    'Done.',
  ])
  expect(transcript[1].tools.map((tool) => tool.label)).toEqual(['Tool: pwd'])
  expect(transcript[2].tools.map((tool) => tool.label)).toEqual(['Tool: git status --short'])
  expect(transcript[3].tools).toHaveLength(0)
})

function tokenUsageRaw(totalTokens: number, outputTokens: number) {
  return {
    tokenUsage: {
      total: {
        totalTokens,
        inputTokens: totalTokens - outputTokens,
        cachedInputTokens: 4480,
        outputTokens,
        reasoningOutputTokens: 0,
      },
      last: {
        totalTokens,
        inputTokens: totalTokens - outputTokens,
        cachedInputTokens: 4480,
        outputTokens,
        reasoningOutputTokens: 0,
      },
      modelContextWindow: 258400,
    },
  }
}

test('chat transcript dedupes repeated shell command text in tool output', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Checking.' }),
    event(2, 'tool.call.started', { item_id: 'tool_1', command: '/bin/zsh -lc pwd' }),
    event(3, 'tool.call.completed', { item_id: 'tool_1', command: '/bin/zsh -lc pwd' }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: pwd',
    text: 'pwd',
  })
})

test('chat transcript includes codex command aggregated output in tool details', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Listing files.' }),
    event(2, 'tool.call.started', { item_id: 'tool_1', command: "/bin/zsh -lc 'ls -la'" }),
    event(3, 'tool.call.completed', {
      item_id: 'tool_1',
      command: "/bin/zsh -lc 'ls -la'",
      aggregated_output: 'total 56\nREADME.md\nweb\n',
      exit_code: 0,
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: ls -la',
    status: 'completed',
    text: 'ls -la\ntotal 56\nREADME.md\nweb\n',
  })
})

test('chat transcript reads historical nested MCP arguments and results', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Running tests.' }),
    event(2, 'tool.call.started', {
      item_id: 'tool_1',
      item_type: 'mcpToolCall',
      server: 'life',
      tool: 'exec_command',
      arguments: { command: 'go test ./...', cwd: '/repo' },
    }),
    event(3, 'tool.call.completed', {
      item_id: 'tool_1',
      item_type: 'mcpToolCall',
      server: 'life',
      tool: 'exec_command',
      arguments: { command: 'go test ./...', cwd: '/repo' },
      result: {
        content: [{ type: 'text', text: '{"output":"ok\\n"}' }],
        structuredContent: { status: 'completed', output: 'ok\n' },
      },
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: go test ./...',
    status: 'completed',
    text: 'go test ./...\nok\n',
    error: '',
    content: [],
  })
})

test('chat transcript preserves MCP media, resources, structured data, and nested errors', () => {
  const completed = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Fetching artifacts.' }),
    event(2, 'tool.call.completed', {
      item_id: 'tool_1',
      item_type: 'mcpToolCall',
      tool: 'fetch_artifacts',
      result: {
        content: [
          { type: 'image', data: '', mimeType: 'image/png', _gorchestra_truncated: true },
          { type: 'audio', data: 'YXVkaW8=', mimeType: 'audio/wav' },
          {
            type: 'resource',
            resource: { uri: 'mcp://files/report.pdf', blob: 'cGRm', mimeType: 'application/pdf' },
          },
          {
            type: 'resource_link',
            name: 'Reference',
            uri: 'https://example.com/reference',
            description: 'Supporting source',
            mimeType: 'text/html',
          },
        ],
        structuredContent: { items: ['one', 'two'] },
      },
    }),
  ])

  expect(completed[0].tools[0]).toMatchObject({
    text: '{\n  "items": [\n    "one",\n    "two"\n  ]\n}',
    content: [
      {
        kind: 'image',
        mediaType: 'image/png',
        sourceURL: '/api/sessions/sess_test/events/2/tool-content/0',
      },
      {
        kind: 'audio',
        mediaType: 'audio/wav',
        sourceURL: '/api/sessions/sess_test/events/2/tool-content/1',
      },
      {
        kind: 'resource',
        name: 'report.pdf',
        mediaType: 'application/pdf',
        sourceURL: '/api/sessions/sess_test/events/2/tool-content/2',
      },
      {
        kind: 'resource-link',
        name: 'Reference',
        uri: 'https://example.com/reference',
      },
    ],
  })

  const failed = buildChatTranscript([
    event(3, 'agent.message.completed', { item_id: 'msg_2', text: 'Trying a tool.' }),
    failedEvent(4, 'tool.call.completed', {
      item_id: 'tool_2',
      item_type: 'mcpToolCall',
      tool: 'unavailable_tool',
      error: { message: 'Tool unavailable', code: -32000 },
    }),
  ])
  expect(failed[0].tools[0]).toMatchObject({ status: 'failed', error: 'Tool unavailable' })
})

test('chat transcript labels file changes as edits and shows emitted patches', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Updating tests.' }),
    event(2, 'file.change.started', {
      item_id: 'edit_1',
      paths: ['/Users/joey/Source/gorchestra/internal/httpapi/sessions_test.go'],
    }),
    event(3, 'file.change.completed', {
      item_id: 'edit_1',
      changes: [
        {
          path: '/Users/joey/Source/gorchestra/internal/httpapi/sessions_test.go',
          patch: '@@ -1,3 +1,3 @@\n-old line\n+new line',
        },
      ],
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'sessions_test.go',
    status: 'completed',
    text: '@@ -1,3 +1,3 @@\n-old line\n+new line',
  })
})

test('chat transcript falls back to file paths when file change diffs are unavailable', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Updating files.' }),
    event(2, 'file.change.completed', {
      item_id: 'edit_1',
      paths: [
        '/Users/joey/Source/gorchestra/internal/httpapi/sessions_test.go',
        '/Users/joey/Source/gorchestra/internal/httpapi/sessions.go',
      ],
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'sessions_test.go +1',
    text: [
      '/Users/joey/Source/gorchestra/internal/httpapi/sessions_test.go',
      '/Users/joey/Source/gorchestra/internal/httpapi/sessions.go',
    ].join('\n'),
  })
})

test('chat transcript labels web search tools from completed query metadata', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Checking weather.' }),
    event(2, 'tool.call.started', {
      item_id: 'web_1',
      item_type: 'webSearch',
      action: { type: 'other' },
      query: '',
    }),
    event(3, 'tool.call.completed', {
      item_id: 'web_1',
      item_type: 'webSearch',
      action: {
        type: 'search',
        query: 'weather: 33445, United States',
        queries: ['weather: 33445, United States'],
      },
      query: 'weather: 33445, United States',
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: Web search: weather: 33445, United States',
    text: ['Query: weather: 33445, United States', 'Queries:', '- weather: 33445, United States'].join('\n'),
  })
})

test('chat transcript labels OpenCode read tools from paths', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Reading the file.' }),
    event(2, 'tool.call.started', {
      tool_call_id: 'call_read',
      kind: 'read',
      title: 'read',
    }),
    event(3, 'tool.call.completed', {
      tool_call_id: 'call_read',
      kind: 'read',
      title: 'read',
      locations: [{ path: '/Users/joey/Source/gennari/index.html' }],
      raw_input: { filePath: '/Users/joey/Source/gennari/index.html', limit: 120 },
    }),
  ])

  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: Read index.html',
  })
})

test('chat transcript labels OpenCode glob and bash tools from raw input', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', { item_id: 'msg_1', text: 'Checking the workspace.' }),
    event(2, 'tool.call.started', {
      tool_call_id: 'call_glob',
      kind: 'glob',
      title: 'glob',
    }),
    event(3, 'tool.call.completed', {
      tool_call_id: 'call_glob',
      kind: 'glob',
      title: 'glob',
      locations: [{ path: '/Users/joey/Source/gorchestra' }],
      raw_input: { pattern: '**/*.go', path: '/Users/joey/Source/gorchestra' },
    }),
    event(4, 'tool.call.started', {
      tool_call_id: 'call_bash',
      kind: 'bash',
      title: 'bash',
    }),
    event(5, 'tool.call.completed', {
      tool_call_id: 'call_bash',
      kind: 'bash',
      title: "date '+%A, %B %d, %Y'",
      raw_input: { command: "date '+%A, %B %d, %Y'" },
    }),
  ])

  expect(transcript[0].tools.map((tool) => tool.label)).toEqual([
    'Tool: Glob **/*.go in gorchestra',
    "Tool: date '+%A, %B %d, %Y'",
  ])
})

test('chat transcript synthesizes legacy Claude tool calls from provider events', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', {
      provider: 'claude',
      message_id: 'msg_1',
      text: 'Let me read that file.',
    }),
    event(2, 'provider.claude.event', {
      provider: 'claude',
      provider_event_type: 'content_block_start',
      raw_event: {
        type: 'content_block_start',
        index: 0,
        content_block: {
          type: 'tool_use',
          id: 'toolu_read',
          name: 'Read',
          input: {},
        },
      },
    }),
    event(3, 'agent.message.completed', {
      provider: 'claude',
      message_id: 'msg_1',
      raw_message: {
        content: [
          {
            type: 'tool_use',
            id: 'toolu_read',
            name: 'Read',
            input: { file_path: '/Users/joey/Source/gennari/index.html' },
          },
        ],
      },
    }),
    event(4, 'provider.claude.event', {
      provider: 'claude',
      provider_event_type: 'user',
      raw: {
        message: {
          content: [
            {
              type: 'tool_result',
              tool_use_id: 'toolu_read',
              is_error: false,
              content: '1\\t<!DOCTYPE html>',
            },
          ],
        },
        tool_use_result: '1\\t<!DOCTYPE html>',
      },
    }),
  ])

  expect(transcript).toHaveLength(1)
  expect(transcript[0]).toMatchObject({ text: 'Let me read that file.' })
  expect(transcript[0].tools).toHaveLength(1)
  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: Read index.html',
    text: '1\\t<!DOCTYPE html>',
  })
})

test('chat transcript does not synthesize duplicate Claude tools when canonical events exist', () => {
  const transcript = buildChatTranscript([
    event(1, 'agent.message.completed', {
      provider: 'claude',
      message_id: 'msg_1',
      text: 'Running a command.',
    }),
    event(2, 'provider.claude.event', {
      provider: 'claude',
      provider_event_type: 'content_block_start',
      raw_event: {
        content_block: {
          type: 'tool_use',
          id: 'toolu_bash',
          name: 'Bash',
          input: {},
        },
      },
    }),
    event(3, 'tool.call.started', {
      provider: 'claude',
      tool_call_id: 'toolu_bash',
      name: 'Bash',
    }),
    event(4, 'tool.call.delta', {
      provider: 'claude',
      tool_call_id: 'toolu_bash',
      name: 'Bash',
      command: 'pwd',
      raw_input: { command: 'pwd' },
    }),
    event(5, 'tool.call.completed', {
      provider: 'claude',
      tool_call_id: 'toolu_bash',
      name: 'Bash',
      command: 'pwd',
      output: '/Users/joey/Source',
    }),
  ])

  expect(transcript[0].tools).toHaveLength(1)
  expect(transcript[0].tools[0]).toMatchObject({
    label: 'Tool: pwd',
    text: 'pwd\n/Users/joey/Source',
  })
})

test.each([false, true])('Claude selects the main model window regardless of model map order (%s)', (reverse) => {
  const entries: [string, { contextWindow: number }][] = [
    ['claude-haiku-4-5', { contextWindow: 200_000 }],
    ['claude-opus-4-6', { contextWindow: 1_000_000 }],
  ]
  const history = [
    event(1, 'agent.run.started', { provider: 'claude', provider_event_type: 'system/init', model: 'claude-opus-4-6' }),
    event(2, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', raw_event: { message: { id: 'msg_1', model: 'claude-opus-4-6', usage: { input_tokens: 10, cache_read_input_tokens: 150_000 } } } }),
    event(3, 'provider.claude.event', { provider: 'claude', provider_event_type: 'message_delta', usage: { output_tokens: 100 } }),
    event(4, 'agent.run.completed', { provider: 'claude', provider_event_type: 'result', usage: { input_tokens: 50, cache_read_input_tokens: 900_000, output_tokens: 1000 }, model_usage: Object.fromEntries(reverse ? entries.reverse() : entries) }),
  ]
  const result = latestTokenUsage(history)
  expect(result).toMatchObject({ modelContextWindow: 1_000_000, last: { totalTokens: 150_110 }, total: { totalTokens: 901_050 } })
  const resumed = [...history,
    event(5, 'agent.run.started', { provider: 'claude', provider_event_type: 'system/init', model: 'claude-opus-4-6' }),
    event(6, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', message_id: 'msg_2', usage: { input_tokens: 10, cache_read_input_tokens: 151_000 } }),
  ]
  expect(latestTokenUsage(resumed)).toMatchObject({ modelContextWindow: 1_000_000, last: { totalTokens: 151_010 } })
  // Reconnect overlap and input ordering do not change the derived state.
  expect(latestTokenUsage([...resumed].reverse().concat(history))).toEqual(latestTokenUsage(resumed))
})

test('Claude merges snapshots, ignores subagents, and allows context to shrink after compaction', () => {
  const history = [
    event(1, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', model: 'sonnet', message_id: 'one', usage: { input_tokens: 10, cache_read_input_tokens: 100_000, output_tokens: 2 } }),
    event(2, 'provider.claude.event', { provider: 'claude', provider_event_type: 'message_delta', usage: { output_tokens: 50 } }),
    event(3, 'provider.claude.event', { provider: 'claude', provider_event_type: 'message_delta', usage: { output_tokens: 60 } }),
    event(4, 'agent.message.completed', { provider: 'claude', provider_event_type: 'assistant', message_id: 'one', raw_message: { usage: { input_tokens: 10, cache_read_input_tokens: 100_000, output_tokens: 2 } } }),
    event(5, 'agent.message.completed', { provider: 'claude', provider_event_type: 'assistant', parent_tool_use_id: 'tool', model: 'haiku', raw_message: { id: 'helper', usage: { input_tokens: 50_000, output_tokens: 10 } } }),
  ]
  expect(latestTokenUsage(history)).toMatchObject({ modelContextWindow: null, last: { totalTokens: 100_070 }, total: { totalTokens: 100_070 } })
  history.push(event(6, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', model: 'sonnet', message_id: 'two', usage: { input_tokens: 10_000, output_tokens: 2 } }))
  expect(latestTokenUsage(history)).toMatchObject({ last: { totalTokens: 10_002 }, total: { totalTokens: 110_072 } })
})

test('Claude context usage resets on clear and waits for fresh usage after compaction', () => {
  const history = [event(1, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', model: 'sonnet', message_id: 'one', usage: { input_tokens: 50_000, output_tokens: 10 } })]
  const compacted = [...history, event(2, 'provider.claude.event', { provider: 'claude', provider_event_type: 'system', subtype: 'compact_boundary' })]
  expect(latestTokenUsage(compacted)?.last).toBeNull()
  const resumed = [...compacted, event(3, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', model: 'sonnet', message_id: 'two', usage: { input_tokens: 2_000, output_tokens: 1 } })]
  expect(latestTokenUsage(resumed)?.last?.totalTokens).toBe(2_001)
  const cleared = [...resumed, event(4, 'session.action.completed', { action: 'clear' })]
  expect(latestTokenUsage(cleared)).toBeNull()
  expect(latestTokenUsage([...cleared, event(5, 'agent.run.started', { provider: 'claude', provider_event_type: 'system/init', model: 'sonnet' })])).toMatchObject({ last: null, modelContextWindow: null, total: { totalTokens: 0 } })
  expect(latestTokenUsage([...cleared].reverse().concat(history))).toBeNull()
})

test('Claude local Sonnet fixture retains the reported 200k window and current message usage', () => {
  expect(latestTokenUsage([
    event(1, 'agent.run.started', { provider: 'claude', provider_event_type: 'system/init', model: 'claude-sonnet-4-6' }),
    event(2, 'agent.status.started', { provider: 'claude', provider_event_type: 'message_start', raw_event: { message: { id: 'main', model: 'claude-sonnet-4-6', usage: { input_tokens: 3, cache_creation_input_tokens: 21331, output_tokens: 2 } } } }),
    event(3, 'provider.claude.event', { provider: 'claude', provider_event_type: 'message_delta', usage: { input_tokens: 3, cache_creation_input_tokens: 21331, output_tokens: 18 } }),
    event(4, 'agent.run.completed', { provider: 'claude', provider_event_type: 'result', usage: { input_tokens: 3, cache_creation_input_tokens: 21331, output_tokens: 18 }, model_usage: { 'claude-haiku-4-5-20251001': { contextWindow: 200_000 }, 'claude-sonnet-4-6': { contextWindow: 200_000 } } }),
  ])).toMatchObject({ modelContextWindow: 200_000, last: { totalTokens: 21352 } })
})

test('Claude partial history never substitutes aggregate or output-only usage for context', () => {
  expect(latestTokenUsage([
    event(1, 'provider.claude.event', { provider: 'claude', provider_event_type: 'message_delta', usage: { output_tokens: 100 } }),
    event(2, 'agent.run.completed', { provider: 'claude', provider_event_type: 'result', usage: { input_tokens: 700_000, output_tokens: 100 }, model_usage: { haiku: { contextWindow: 200_000 }, opus: { contextWindow: 1_000_000 } } }),
  ])).toMatchObject({ last: null, modelContextWindow: null, total: { totalTokens: 700_100 } })
})

test('Claude matches canonical model IDs and invalidates limits on model changes', () => {
  const history = [
    event(1, 'agent.run.completed', { provider: 'claude', provider_event_type: 'result', model: 'claude-sonnet-4-6', usage: { input_tokens: 100 }, model_usage: { provider_alias: { canonicalModel: 'claude-sonnet-4-6', contextWindow: 1_000_000 } } }),
  ]
  expect(latestTokenUsage(history)?.modelContextWindow).toBe(1_000_000)
  history.push(event(2, 'agent.run.started', { provider: 'claude', provider_event_type: 'system/init', model: 'claude-haiku-4-5' }))
  expect(latestTokenUsage(history)).toMatchObject({ modelContextWindow: null, last: null })
})
