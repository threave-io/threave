package codex

import (
	"encoding/json"
	"strings"

	"github.com/threave-io/threave/internal/agents"
)

type terminalKind int

const (
	terminalNone terminalKind = iota
	terminalCompleted
	terminalFailed
	terminalCancelled
)

type normalizedEvent struct {
	Event    agents.AgentEvent
	Terminal terminalKind
}

type normalizer struct {
	runStarted    bool
	turnStarted   bool
	terminal      bool
	terminalKind  terminalKind
	terminalError string
}

func newNormalizer() *normalizer {
	return &normalizer{}
}

func (n *normalizer) normalize(method string, params json.RawMessage) []normalizedEvent {
	if n.terminal {
		return nil
	}

	switch method {
	case "thread/started":
		threadID := stringAt(params, "thread", "id")
		return compact(n.syntheticRunStarted(method, threadID))
	case "turn/started":
		threadID := stringAt(params, "threadId")
		turnID := stringAt(params, "turn", "id")
		return compact(n.syntheticTurnStarted(method, threadID, turnID))
	case "item/agentMessage/delta":
		payload := basePayload(method, params)
		payload["text"] = stringAt(params, "delta")
		return []normalizedEvent{{Event: event("agent.message.delta", "assistant", "delta", payload)}}
	case "item/commandExecution/outputDelta":
		payload := basePayload(method, params)
		payload["text"] = stringAt(params, "delta")
		return []normalizedEvent{{Event: event("tool.call.delta", "assistant", "delta", payload)}}
	case "item/fileChange/outputDelta":
		payload := basePayload(method, params)
		payload["text"] = stringAt(params, "delta")
		return []normalizedEvent{{Event: event("file.change.delta", "assistant", "delta", payload)}}
	case "item/fileChange/patchUpdated":
		payload := basePayload(method, params)
		payload["changes"] = anyAt(params, "changes")
		payload["paths"] = changePaths(payload["changes"])
		return []normalizedEvent{{Event: event("file.change.delta", "assistant", "delta", payload)}}
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		payload := basePayload(method, params)
		payload["text"] = stringAt(params, "delta")
		return []normalizedEvent{{Event: event("agent.thinking.delta", "assistant", "delta", payload)}}
	case "item/plan/delta":
		payload := basePayload(method, params)
		payload["text"] = stringAt(params, "delta")
		return []normalizedEvent{{Event: event("agent.plan.delta", "assistant", "delta", payload)}}
	case "item/started":
		return n.normalizeItemLifecycle(method, params, true)
	case "item/completed":
		return n.normalizeItemLifecycle(method, params, false)
	case "turn/completed":
		return compact(n.normalizeTurnCompleted(method, params))
	case "thread/tokenUsage/updated":
		return []normalizedEvent{n.normalizeTokenUsage(method, params)}
	case "error":
		message := firstNonEmpty(stringAt(params, "error", "message"), "codex error")
		payload := basePayload(method, params)
		payload["error"] = message
		payload["text"] = message
		if details := stringAt(params, "error", "additionalDetails"); details != "" {
			payload["details"] = details
		}
		if codexErrorInfo := anyAt(params, "error", "codexErrorInfo"); codexErrorInfo != nil {
			payload["codex_error_info"] = codexErrorInfo
		}
		status := "failed"
		if willRetry, ok := anyAt(params, "willRetry").(bool); ok {
			payload["will_retry"] = willRetry
			if willRetry {
				status = "delta"
			}
		}
		return []normalizedEvent{{Event: event("agent.log.delta", "system", status, payload)}}
	case "warning", "guardianWarning", "deprecationNotice", "configWarning":
		payload := basePayload(method, params)
		payload["text"] = warningText(params)
		return []normalizedEvent{{Event: event("agent.log.delta", "system", "delta", payload)}}
	default:
		return []normalizedEvent{n.unknown("provider.codex.event", method, params)}
	}
}

func (n *normalizer) normalizeTokenUsage(method string, params json.RawMessage) normalizedEvent {
	payload := basePayload(method, params)
	payload["raw"] = json.RawMessage(params)
	threadID := stringAt(params, "threadId")
	totalTokens := anyAt(params, "tokenUsage", "total", "totalTokens")
	if threadID != "" && totalTokens != nil {
		payload["usage"] = map[string]any{
			"context_id":   threadID,
			"total_tokens": totalTokens,
		}
	}
	return normalizedEvent{Event: event("provider.codex.event", "system", "completed", payload)}
}

func (n *normalizer) normalizeItemLifecycle(method string, params json.RawMessage, started bool) []normalizedEvent {
	item := mapAt(params, "item")
	itemType := stringFromMap(item, "type")
	if itemType == "" {
		return []normalizedEvent{n.unknown("provider.codex.event", method, params)}
	}

	payload := basePayload(method, params)
	payload["item_type"] = itemType
	if itemID := stringFromMap(item, "id"); itemID != "" {
		payload["item_id"] = itemID
	}

	if started {
		switch itemType {
		case "reasoning":
			copyItemFields(payload, item)
			return []normalizedEvent{{Event: event("agent.thinking.started", "assistant", "started", payload)}}
		case "commandExecution", "mcpToolCall", "dynamicToolCall", "webSearch", "collabAgentToolCall":
			copyItemFields(payload, item)
			canonicalizeToolItem(payload, item)
			return []normalizedEvent{{Event: event("tool.call.started", "assistant", "started", payload)}}
		case "imageGeneration":
			payload["tool"] = "Generate image"
			return []normalizedEvent{{Event: event("tool.call.started", "assistant", "started", payload)}}
		case "fileChange":
			copyItemFields(payload, item)
			payload["paths"] = changePaths(item["changes"])
			return []normalizedEvent{{Event: event("file.change.started", "assistant", "started", payload)}}
		default:
			return nil
		}
	}

	copyItemFields(payload, item)
	switch itemType {
	case "agentMessage":
		payload["text"] = stringFromMap(item, "text")
		if stringFromMap(item, "delivery") == "async" {
			questions := asyncQuestions(item)
			if len(questions) > 0 && stringFromMap(item, "id") != "" && stringAt(params, "threadId") != "" && stringAt(params, "turnId") != "" {
				payload["delivery"] = "async"
				payload["request_id"] = stringFromMap(item, "id")
				payload["questions"] = questions
				return []normalizedEvent{{Event: event("agent.input.requested", "assistant", "started", payload)}}
			}
		}
		return []normalizedEvent{{Event: event("agent.message.completed", "assistant", "completed", payload)}}
	case "reasoning":
		payload["text"] = reasoningText(item)
		return []normalizedEvent{{Event: event("agent.thinking.completed", "assistant", "completed", payload)}}
	case "plan":
		payload["text"] = stringFromMap(item, "text")
		return []normalizedEvent{{Event: event("agent.plan.completed", "assistant", "completed", payload)}}
	case "commandExecution", "mcpToolCall", "dynamicToolCall", "webSearch", "collabAgentToolCall":
		canonicalizeToolItem(payload, item)
		return []normalizedEvent{{Event: event("tool.call.completed", "assistant", eventStatusFromItem(item), payload)}}
	case "imageGeneration":
		// Use the same binary content contract as other tools so persistence
		// externalizes the image and replay only carries its metadata.
		payload["tool"] = "Generate image"
		delete(payload, "result")
		if data := stringFromMap(item, "result"); data != "" {
			payload["result"] = map[string]any{"content": []any{map[string]any{
				"type": "image", "mimeType": "image/png", "data": data, "name": "Generated image.png",
			}}}
		}
		status := eventStatusFromItem(item)
		if failure := item["failure"]; failure != nil {
			status = "failed"
			payload["error"] = firstNonEmpty(toolErrorMessage(failure), "Image generation failed")
		}
		return []normalizedEvent{{Event: event("tool.call.completed", "assistant", status, payload)}}
	case "fileChange":
		payload["paths"] = changePaths(item["changes"])
		return []normalizedEvent{{Event: event("file.change.completed", "assistant", eventStatusFromItem(item), payload)}}
	default:
		return []normalizedEvent{n.unknown("provider.codex.event", method, params)}
	}
}

func (n *normalizer) normalizeTurnCompleted(method string, params json.RawMessage) normalizedEvent {
	status := stringAt(params, "turn", "status")
	payload := basePayload(method, params)
	payload["turn_status"] = status
	if turnID := stringAt(params, "turn", "id"); turnID != "" {
		payload["turn_id"] = turnID
	}
	if duration := anyAt(params, "turn", "durationMs"); duration != nil {
		payload["duration_ms"] = duration
	}

	switch status {
	case "completed":
		n.markTerminal(terminalCompleted, "")
		return normalizedEvent{Event: event("agent.run.completed", "assistant", "completed", payload), Terminal: terminalCompleted}
	case "interrupted":
		n.markTerminal(terminalCancelled, "codex run interrupted")
		return normalizedEvent{Event: event("agent.run.cancelled", "assistant", "cancelled", payload), Terminal: terminalCancelled}
	case "failed":
		message := firstNonEmpty(stringAt(params, "turn", "error", "message"), "codex turn failed")
		payload["error"] = message
		n.markTerminal(terminalFailed, message)
		return normalizedEvent{Event: event("agent.run.failed", "assistant", "failed", payload), Terminal: terminalFailed}
	default:
		return n.unknown("provider.codex.event", method, params)
	}
}

func (n *normalizer) syntheticRunStarted(providerEventType string, threadID string) normalizedEvent {
	if n.runStarted {
		return normalizedEvent{}
	}
	n.runStarted = true
	payload := map[string]any{
		"provider":            "codex",
		"provider_event_type": providerEventType,
	}
	if threadID != "" {
		payload["thread_id"] = threadID
	}
	return normalizedEvent{Event: event("agent.run.started", "assistant", "started", payload)}
}

func (n *normalizer) syntheticTurnStarted(providerEventType string, threadID string, turnID string) normalizedEvent {
	if n.turnStarted {
		return normalizedEvent{}
	}
	n.turnStarted = true
	payload := map[string]any{
		"provider":            "codex",
		"provider_event_type": providerEventType,
	}
	if threadID != "" {
		payload["thread_id"] = threadID
	}
	if turnID != "" {
		payload["turn_id"] = turnID
	}
	return normalizedEvent{Event: event("agent.status.started", "assistant", "started", payload)}
}

func (n *normalizer) unknown(eventType string, providerEventType string, raw json.RawMessage) normalizedEvent {
	payload := map[string]any{
		"provider":            "codex",
		"provider_event_type": providerEventType,
		"raw":                 json.RawMessage(raw),
	}
	return normalizedEvent{Event: event(eventType, "system", "completed", payload)}
}

func (n *normalizer) markTerminal(kind terminalKind, message string) {
	if n.terminal {
		return
	}
	n.terminal = true
	n.terminalKind = kind
	n.terminalError = message
}

func event(eventType string, role string, status string, payload map[string]any) agents.AgentEvent {
	return agents.AgentEvent{
		Type:    eventType,
		Role:    role,
		Status:  status,
		Payload: payload,
	}
}

func compact(events ...normalizedEvent) []normalizedEvent {
	compacted := make([]normalizedEvent, 0, len(events))
	for _, event := range events {
		if event.Event.Type != "" {
			compacted = append(compacted, event)
		}
	}
	return compacted
}

func basePayload(method string, params json.RawMessage) map[string]any {
	payload := map[string]any{
		"provider":            "codex",
		"provider_event_type": method,
	}
	for _, pair := range []struct {
		payloadKey string
		path       []string
	}{
		{"thread_id", []string{"threadId"}},
		{"turn_id", []string{"turnId"}},
		{"item_id", []string{"itemId"}},
		{"thread_id", []string{"thread", "id"}},
		{"turn_id", []string{"turn", "id"}},
	} {
		if _, exists := payload[pair.payloadKey]; exists {
			continue
		}
		if value := stringAt(params, pair.path...); value != "" {
			payload[pair.payloadKey] = value
		}
	}
	return payload
}

func copyItemFields(payload map[string]any, item map[string]any) {
	for _, key := range []string{
		"command",
		"cwd",
		"processId",
		"source",
		"status",
		"aggregatedOutput",
		"exitCode",
		"durationMs",
		"server",
		"tool",
		"namespace",
		"arguments",
		"result",
		"error",
		"query",
		"action",
		"changes",
		"success",
	} {
		if value, ok := item[key]; ok && value != nil {
			payload[snake(key)] = value
		}
	}
}

func canonicalizeToolItem(payload map[string]any, item map[string]any) {
	if arguments, ok := item["arguments"].(map[string]any); ok {
		payload["raw_input"] = arguments
		for _, key := range []string{"command", "cwd", "query", "path", "file", "filePath", "file_path"} {
			if _, exists := payload[snake(key)]; exists {
				continue
			}
			if value, exists := arguments[key]; exists && value != nil {
				payload[snake(key)] = value
			}
		}
	}

	if result, exists := item["result"]; exists && result != nil {
		if output := toolResultText(result); output != "" {
			payload["output"] = output
			payload["aggregated_output"] = output
		}
	}

	if rawError, exists := item["error"]; exists && rawError != nil {
		if message := toolErrorMessage(rawError); message != "" {
			payload["raw_error"] = rawError
			payload["error"] = message
		}
	}
}

func toolResultText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	result, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	if structured, ok := result["structuredContent"].(map[string]any); ok {
		if output, ok := structured["output"].(string); ok && output != "" {
			return output
		}
	}

	parts := make([]string, 0)
	if content, ok := result["content"].([]any); ok {
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			switch stringFromMap(block, "type") {
			case "text":
				if text := stringFromMap(block, "text"); text != "" {
					parts = append(parts, text)
				}
			case "resource":
				if resource, ok := block["resource"].(map[string]any); ok {
					if text := stringFromMap(resource, "text"); text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}

	structured, exists := result["structuredContent"]
	if !exists || structured == nil {
		return ""
	}
	encoded, err := json.MarshalIndent(structured, "", "  ")
	if err != nil || string(encoded) == "{}" || string(encoded) == "null" {
		return ""
	}
	return string(encoded)
}

func toolErrorMessage(value any) string {
	if message, ok := value.(string); ok {
		return message
	}
	errorObject, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"message", "error", "details"} {
		if message, ok := errorObject[key].(string); ok && message != "" {
			return message
		}
	}
	return ""
}

func eventStatusFromItem(item map[string]any) string {
	status := stringFromMap(item, "status")
	switch status {
	case "failed", "declined", "aborted", "timedOut":
		return "failed"
	case "inProgress", "in_progress":
		return "started"
	default:
		return "completed"
	}
}

func warningText(raw json.RawMessage) string {
	for _, path := range [][]string{
		{"message"},
		{"summary"},
		{"details"},
		{"error", "message"},
	} {
		if text := stringAt(raw, path...); text != "" {
			return text
		}
	}
	return "Codex warning"
}

func reasoningText(item map[string]any) string {
	parts := make([]string, 0)
	parts = append(parts, stringsFromAny(item["summary"])...)
	parts = append(parts, stringsFromAny(item["content"])...)
	return strings.Join(parts, "\n")
}

func changePaths(value any) []string {
	changes, ok := value.([]any)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		object, ok := change.(map[string]any)
		if !ok {
			continue
		}
		if path := stringFromMap(object, "path"); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func anyAt(raw json.RawMessage, path ...string) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func mapAt(raw json.RawMessage, path ...string) map[string]any {
	value := anyAt(raw, path...)
	object, _ := value.(map[string]any)
	return object
}

func stringFromMap(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func stringsFromAny(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	stringsOut := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			stringsOut = append(stringsOut, text)
		}
	}
	return stringsOut
}

func snake(key string) string {
	var builder strings.Builder
	for index, r := range key {
		if index > 0 && r >= 'A' && r <= 'Z' {
			builder.WriteByte('_')
		}
		builder.WriteRune(r)
	}
	return strings.ToLower(builder.String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
