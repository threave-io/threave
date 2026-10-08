-- Older Codex image-generation completions were retained as debug events.
-- Promote them to the binary tool-result contract without changing their
-- identity or sequence. Event maintenance can then externalize their bytes.
UPDATE events
SET type = 'tool.call.completed',
    role = 'assistant',
    status = CASE
      WHEN json_extract(payload_json, '$.raw.item.failure') IS NOT NULL
        OR json_extract(payload_json, '$.raw.item.status') IN ('failed', 'declined', 'aborted', 'timedOut')
      THEN 'failed' ELSE 'completed' END,
    payload_json = json_set(
      json_remove(payload_json, '$.raw'),
      '$.item_type', 'imageGeneration',
      '$.item_id', json_extract(payload_json, '$.raw.item.id'),
      '$.thread_id', json_extract(payload_json, '$.raw.threadId'),
      '$.turn_id', json_extract(payload_json, '$.raw.turnId'),
      '$.tool', 'Generate image',
      '$.error', CASE WHEN json_extract(payload_json, '$.raw.item.failure') IS NOT NULL
        THEN COALESCE(json_extract(payload_json, '$.raw.item.failure.message'), 'Image generation failed')
        ELSE NULL END,
      '$.result', CASE
        WHEN json_type(payload_json, '$.raw.item.result') = 'text'
          AND length(json_extract(payload_json, '$.raw.item.result')) > 0
        THEN json_object('content', json_array(json_object(
          'type', 'image', 'mimeType', 'image/png', 'name', 'Generated image.png',
          'data', json_extract(payload_json, '$.raw.item.result')
        )))
        ELSE json_object('content', json_array()) END
    )
WHERE type = 'provider.codex.event'
  AND json_extract(payload_json, '$.provider_event_type') = 'item/completed'
  AND json_extract(payload_json, '$.raw.item.type') = 'imageGeneration';
