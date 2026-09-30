# Seen on any device

Run completion, failure, and cancellation pushes wait two seconds. If any
foreground, focused chat shows the end of the latest run's output during that
window, the client posts its session ID and exact terminal event sequence to
`POST /api/notifications/seen`. That completion's pushes are suppressed for
every subscription in this Threave instance.

The client does not acknowledge from merely receiving an event, selecting a
session, viewing files/settings, scrolling older history, or having a chat
covered by a dialog. The output end must be in the unobscured transcript
viewport. Tool-only output counts. A run without displayed output does not.
Acknowledgment does not need notification permission or a push subscription.

The server validates that the sequence names a persisted terminal event.
Acknowledgments are held in memory for two minutes, with at most 1,024 entries.
A server restart clears them. Already delivered pushes cannot be recalled;
a late acknowledgment stops subsequent deliveries. Network failures leave
normal notifications enabled, without client retries or error toasts.

Approval requests retain their existing per-device acknowledgment and one-second
grace. Explicit notification tests, badges, and session unread indicators are
unchanged; no silent pushes or presence heartbeats are introduced.

## Manual acceptance test

1. Enable push on two devices and open the same chat on a focused desktop.
2. Complete a run with the response end visible. Neither device should alert.
3. Scroll up, switch to Files/Settings, hide the tab, or move focus away.
   Complete another run. Both subscribed devices should receive the usual push.
4. Repeat with tool-only output, a failed run, and a cancelled run with output.
5. Verify an approval request still follows the existing notification behavior.
