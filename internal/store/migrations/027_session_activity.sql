ALTER TABLE sessions ADD COLUMN last_activity_at DATETIME;

UPDATE sessions
SET last_activity_at = (
  SELECT created_at FROM events
  WHERE events.session_id = sessions.id
    AND (events.type GLOB 'user.*' OR events.type GLOB 'agent.*'
         OR events.type GLOB 'tool.*' OR events.type GLOB 'file.change.*')
  ORDER BY seq DESC LIMIT 1
);

CREATE INDEX idx_sessions_activity ON sessions(pinned_at DESC, last_activity_at DESC, created_at DESC, id DESC);
