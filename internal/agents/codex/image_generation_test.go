package codex

import (
	"encoding/json"
	"testing"
)

func TestImageGenerationNormalizesBinaryToolResult(t *testing.T) {
	n := newNormalizer()
	started := n.normalize("item/started", json.RawMessage(`{"threadId":"thread_1","turnId":"turn_1","item":{"type":"imageGeneration","id":"image_1","status":"inProgress"}}`))
	if len(started) != 1 || started[0].Event.Type != "tool.call.started" {
		t.Fatalf("expected visible started tool, got %#v", started)
	}
	completed := n.normalize("item/completed", json.RawMessage(`{"threadId":"thread_1","turnId":"turn_1","item":{"type":"imageGeneration","id":"image_1","status":"completed","result":"aW1hZ2U=","savedPath":"/private/image.png","failure":null}}`))
	if len(completed) != 1 || completed[0].Event.Type != "tool.call.completed" || completed[0].Event.Status != "completed" {
		t.Fatalf("expected completed image tool, got %#v", completed)
	}
	payload := completed[0].Event.Payload.(map[string]any)
	if payload["item_id"] != "image_1" || payload["tool"] != "Generate image" || payload["thread_id"] != "thread_1" || payload["turn_id"] != "turn_1" {
		t.Fatalf("missing lifecycle metadata: %#v", payload)
	}
	block := payload["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["type"] != "image" || block["mimeType"] != "image/png" || block["data"] != "aW1hZ2U=" || block["name"] != "Generated image.png" {
		t.Fatalf("expected binary content block, got %#v", block)
	}
}

func TestImageGenerationFailureDoesNotInventAnImage(t *testing.T) {
	events := newNormalizer().normalize("item/completed", json.RawMessage(`{"item":{"type":"imageGeneration","id":"image_1","status":"failed","result":null,"failure":{"message":"Generation unavailable"}}}`))
	if len(events) != 1 || events[0].Event.Type != "tool.call.completed" || events[0].Event.Status != "failed" {
		t.Fatalf("expected failed image tool, got %#v", events)
	}
	payload := events[0].Event.Payload.(map[string]any)
	if payload["error"] != "Generation unavailable" || payload["result"] != nil {
		t.Fatalf("expected error without image content, got %#v", payload)
	}
}
