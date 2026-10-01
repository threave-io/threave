-- Re-rank existing sessions by their latest finished turn, including failed or cancelled turns.
UPDATE sessions
SET last_activity_at = (
  SELECT created_at FROM events
  WHERE events.session_id = sessions.id
    AND events.type IN ('agent.run.completed', 'agent.run.failed', 'agent.run.cancelled')
  ORDER BY seq DESC LIMIT 1
);
