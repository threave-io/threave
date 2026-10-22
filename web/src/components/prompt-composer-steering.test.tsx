import type { ComponentProps } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PromptComposer } from '@/components/prompt-composer'
import { clearAPIRequestCachesForTest } from '@/lib/api'

beforeEach(() => {
  window.localStorage.clear()
  clearAPIRequestCachesForTest()
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], skills: [], errors: [], models: [], collaboration_modes: [] })))
})
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear() })

function setup(overrides: Partial<ComponentProps<typeof PromptComposer>> = {}, text = 'Change direction') {
  const props: ComponentProps<typeof PromptComposer> = {
    sessionID: 's', agentType: 'codex', sessionStatus: 'running', steeringRunID: 'run1', disabled: true, disabledReason: 'Running',
    onSubmit: vi.fn(async () => undefined), onCancel: vi.fn(async () => undefined), onError: vi.fn(), ...overrides,
  }
  const view = render(<PromptComposer {...props} />)
  fireEvent.change(screen.getByLabelText('Prompt'), { target: { value: text } })
  return { props, view }
}

test.each(['codex', 'claude'] as const)('%s Send now passes the run ID without settings, queueing, or cancelling', async (agentType) => {
  const { props } = setup({ agentType })
  fireEvent.click(screen.getByRole('button', { name: 'Send now' }))
  await waitFor(() => expect(props.onSubmit).toHaveBeenCalledExactlyOnceWith('Change direction', undefined, [], false, [], undefined, 'run1'))
  expect(props.onCancel).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
})

test.each(['metaKey', 'ctrlKey'])('%s+Enter sends now while Enter and Cmd/Ctrl+Shift+Enter still queue', async (modifier) => {
  const { props } = setup()
  const prompt = screen.getByLabelText('Prompt')
  fireEvent.keyDown(prompt, { key: 'Enter', [modifier]: true })
  await waitFor(() => expect(props.onSubmit).toHaveBeenCalledTimes(1))
  expect((props.onSubmit as ReturnType<typeof vi.fn>).mock.calls[0][6]).toBe('run1')
  await waitFor(() => expect(prompt).toBeEnabled())
  for (const keys of [{}, { [modifier]: true, shiftKey: true }]) {
    fireEvent.change(prompt, { target: { value: 'Next' } })
    fireEvent.keyDown(prompt, { key: 'Enter', ...keys })
    await waitFor(() => expect(prompt).toBeEnabled())
  }
  expect(props.onSubmit).toHaveBeenCalledTimes(3)
  const calls = (props.onSubmit as ReturnType<typeof vi.fn>).mock.calls
  expect(calls.slice(1).every(call => call[3] === true && call[6] === undefined)).toBe(true)
})

test.each([{ offline: true }, { submissionBlocked: true }, { steeringRunID: '' }, { hasPendingUserInput: true }])('Send now is disabled with %j', (override) => {
  const { props } = setup(override)
  expect(screen.getByRole('button', { name: 'Send now' })).toBeDisabled()
  fireEvent.keyDown(screen.getByLabelText('Prompt'), { key: 'Enter', ctrlKey: true })
  expect(props.onSubmit).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Prompt')).toHaveValue('Change direction')
})

test.each([{ sessionStatus: 'idle' as const }, { agentType: 'pi' as const }])('Send now is not offered for %j', (override) => {
  setup(override)
  expect(screen.queryByRole('button', { name: 'Send now' })).not.toBeInTheDocument()
})

test('failure before durable preparation preserves the draft and blocks rapid duplicates', async () => {
  let reject!: (error: Error) => void
  const onSubmit = vi.fn(() => new Promise<void>((_, no) => { reject = no }))
  const { props } = setup({ onSubmit, prepareBeforeClear: true })
  const button = screen.getByRole('button', { name: 'Send now' })
  fireEvent.click(button)
  fireEvent.click(button)
  expect(onSubmit).toHaveBeenCalledOnce()
  expect(screen.getByLabelText('Prompt')).toHaveValue('Change direction')
  await act(async () => reject(new Error('Storage full')))
  expect(props.onError).toHaveBeenCalledWith('Storage full')
  expect(screen.getByLabelText('Prompt')).toHaveValue('Change direction')
})

test.each(['codex', 'claude'] as const)('%s Send now supports images and selected skills without applying next-run model settings', async (agentType) => {
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], models: [], collaboration_modes: [], errors: [], skills: [{ name: 'review', path: '/review/SKILL.md', enabled: true, scope: 'user', description: 'Review' }] })))
  window.localStorage.setItem('gorchestra.session-composer.s', JSON.stringify({ selectedSkills: [{ name: 'review', path: '/review/SKILL.md' }] }))
  const { props } = setup({ agentType }, 'Change direction $review ')
  await userEvent.setup().upload(screen.getByLabelText('Image attachments'), new File(['image'], 'example.png', { type: 'image/png' }))
  await screen.findByAltText('example.png')
  fireEvent.click(screen.getByRole('button', { name: 'Send now' }))
  await waitFor(() => expect(props.onSubmit).toHaveBeenCalled())
  expect((props.onSubmit as ReturnType<typeof vi.fn>).mock.calls[0]).toEqual([
    'Change direction $review', undefined, [expect.objectContaining({ name: 'example.png', data_url: 'data:image/png;base64,aW1hZ2U=' })], false,
    [{ name: 'review', path: '/review/SKILL.md' }], undefined, 'run1',
  ])
})

test('IME confirmation does not send or queue', () => {
  const { props } = setup()
  fireEvent.keyDown(screen.getByLabelText('Prompt'), { key: 'Enter', ctrlKey: true, isComposing: true })
  expect(props.onSubmit).not.toHaveBeenCalled()
})
