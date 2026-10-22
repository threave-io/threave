package claude

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/threave-io/threave/internal/agents"
)

type terminalKind int

const (
	terminalNone terminalKind = iota
	terminalCompleted
	terminalFailed
)

type normalizedEvent struct {
	Event    agents.AgentEvent
	Terminal terminalKind
}

type normalizer struct {
	usageModel       string
	usageMessageID   string
	runStarted       bool
	currentMessageID string
	messageText      string
	turnActivity     bool
	terminal         bool
	terminalKind     terminalKind
	terminalError    string
	deferResult      bool
	toolCalls        map[string]*claudeToolCall
	toolBlockIDs     map[int]string
	toolInputDeltas  map[int]string
	thinkingBlockIDs map[int]string
	thinkingText     map[string]string
}

func newNormalizer() *normalizer {
	return &normalizer{
		toolCalls:        make(map[string]*claudeToolCall),
		toolBlockIDs:     make(map[int]string),
		toolInputDeltas:  make(map[int]string),
		thinkingBlockIDs: make(map[int]string),
		thinkingText:     make(map[string]string),
	}
}

func (n *normalizer) normalize(input *streamEvent) []normalizedEvent {
	if input == nil || n.terminal {
		return nil
	}

	switch input.Type {
	case "system":
		return n.normalizeSystem(input)
	case "stream_event":
		return n.normalizeAnthropicStreamEvent(input)
	case "assistant":
		return n.normalizeAssistant(input)
	case "user":
		return n.normalizeUser(input)
	case "result":
		return []normalizedEvent{n.normalizeResult(input)}
	case "rate_limit_event":
		payload := basePayload(input)
		payload["rate_limit_info"] = rawOrNil(input.RateLimitInfo)
		return []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
	default:
		return []normalizedEvent{n.unknown(input)}
	}
}

func (n *normalizer) normalizeSystem(input *streamEvent) []normalizedEvent {
	payload := basePayload(input)
	if input.Subtype != "" {
		payload["subtype"] = input.Subtype
	}
	if input.Status != "" {
		payload["status"] = input.Status
	}
	if input.CWD != "" {
		payload["cwd"] = input.CWD
	}
	if len(input.Tools) > 0 {
		payload["tools"] = append([]string(nil), input.Tools...)
	}
	if input.MCPServers != nil {
		payload["mcp_servers"] = rawOrNil(input.MCPServers)
	}
	if input.Model != "" {
		payload["model"] = input.Model
		if input.ParentToolUseID == "" {
			n.usageModel = input.Model
		}
	}

	if input.Subtype == "init" {
		return compact(n.syntheticRunStarted(input, payload))
	}
	if input.Subtype == "compact_boundary" && input.ParentToolUseID == "" {
		// A local /compact may finish without an assistant message or result text.
		n.turnActivity = true
	}
	return []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
}

func (n *normalizer) normalizeAnthropicStreamEvent(input *streamEvent) []normalizedEvent {
	providerEventType := stringAt(input.Event, "type")
	if providerEventType == "" {
		return []normalizedEvent{n.unknown(input)}
	}

	payload := basePayload(input)
	if input.ParentToolUseID == "" {
		n.turnActivity = true
	}
	payload["provider_event_type"] = providerEventType
	payload["raw_event"] = rawOrNil(input.Event)
	if input.ParentToolUseID != "" {
		payload["parent_tool_use_id"] = input.ParentToolUseID
	}

	switch providerEventType {
	case "message_start":
		if input.ParentToolUseID == "" {
			n.usageMessageID = stringAt(input.Event, "message", "id")
			if model := stringAt(input.Event, "message", "model"); model != "" {
				n.usageModel = model
			}
		}
		if usage := anyAt(input.Event, "message", "usage"); usage != nil {
			payload["usage"] = usage
		}
		n.messageText = ""
		if model := stringAt(input.Event, "message", "model"); model != "" {
			payload["model"] = model
		}
		if id := stringAt(input.Event, "message", "id"); id != "" {
			n.currentMessageID = id
			payload["message_id"] = id
		}
		return []normalizedEvent{{Event: agentEvent("agent.status.started", "assistant", "started", payload)}}
	case "content_block_delta":
		n.captureToolInputDelta(input)
		thinkingDelta := n.normalizeThinkingDelta(input)
		text := stringAt(input.Event, "delta", "text")
		if text == "" {
			return compact(
				normalizedEvent{Event: agentEvent("provider.claude.event", "system", "completed", payload)},
				thinkingDelta,
			)
		}
		n.messageText += text
		payload["text"] = text
		if n.currentMessageID != "" {
			payload["message_id"] = n.currentMessageID
		}
		payload["index"] = anyAt(input.Event, "index")
		return []normalizedEvent{{Event: agentEvent("agent.message.delta", "assistant", "delta", payload)}}
	case "message_delta":
		if input.ParentToolUseID == "" {
			if n.usageMessageID != "" {
				payload["message_id"] = n.usageMessageID
			}
			if n.usageModel != "" {
				payload["model"] = n.usageModel
			}
		}
		if stopReason := stringAt(input.Event, "delta", "stop_reason"); stopReason != "" {
			payload["stop_reason"] = stopReason
		}
		if usage := anyAt(input.Event, "usage"); usage != nil {
			payload["usage"] = usage
		}
		return []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
	case "content_block_start":
		return compact(
			normalizedEvent{Event: agentEvent("provider.claude.event", "system", "completed", payload)},
			n.normalizeThinkingStart(input),
			n.normalizeToolUseStart(input),
		)
	case "content_block_stop":
		return compact(
			normalizedEvent{Event: agentEvent("provider.claude.event", "system", "completed", payload)},
			n.normalizeThinkingStop(input),
			n.normalizeToolUseInputStop(input),
		)
	case "message_stop":
		return []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
	default:
		return []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
	}
}

func (n *normalizer) normalizeAssistant(input *streamEvent) []normalizedEvent {
	if input.ParentToolUseID == "" {
		n.turnActivity = true
	}
	payload := basePayload(input)
	payload["provider_event_type"] = "assistant"
	payload["raw_message"] = rawOrNil(input.Message)
	if usage := anyAt(input.Message, "usage"); usage != nil {
		payload["usage"] = usage
	}
	text := textFromAssistantMessage(input.Message)
	if text == "" && !assistantMessageHasToolUse(input.Message) {
		text = n.messageText
	}
	if text != "" {
		payload["text"] = text
	}
	if id := stringAt(input.Message, "id"); id != "" {
		payload["message_id"] = id
	}
	if model := stringAt(input.Message, "model"); model != "" {
		payload["model"] = model
	}
	events := []normalizedEvent{{Event: agentEvent("agent.message.completed", "assistant", "completed", payload)}}
	for _, tool := range toolUsesFromAssistantMessage(input.Message) {
		n.upsertToolCall(tool)
		if tool.Input != nil {
			events = append(events, normalizedEvent{Event: agentEvent("tool.call.delta", "assistant", "delta", n.toolPayload(input, tool))})
			if stored := n.toolCalls[tool.ID]; stored != nil {
				stored.InputEmitted = true
			}
		}
	}
	return events
}

func (n *normalizer) normalizeUser(input *streamEvent) []normalizedEvent {
	payload := basePayload(input)
	payload["provider_event_type"] = "user"
	payload["raw"] = rawOrNil(input.Raw)
	events := []normalizedEvent{{Event: agentEvent("provider.claude.event", "system", "completed", payload)}}
	for _, result := range toolResultsFromUser(input.Raw) {
		tool := n.toolCalls[result.ToolUseID]
		if tool == nil {
			tool = &claudeToolCall{ID: result.ToolUseID}
		}
		eventPayload := n.toolPayload(input, *tool)
		eventPayload["provider_event_type"] = "tool_result"
		if result.Output != "" {
			eventPayload["output"] = result.Output
			eventPayload["aggregated_output"] = result.Output
		}
		if result.RawOutput != nil {
			eventPayload["raw_output"] = result.RawOutput
		}
		if result.IsError {
			eventPayload["is_error"] = true
			if result.Output != "" {
				eventPayload["error"] = result.Output
			}
		}
		events = append(events, normalizedEvent{Event: agentEvent("tool.call.completed", "assistant", "completed", eventPayload)})
	}
	return events
}

func (n *normalizer) normalizeResult(input *streamEvent) normalizedEvent {
	payload := basePayload(input)
	payload["provider_event_type"] = "result"
	if input.Subtype != "" {
		payload["subtype"] = input.Subtype
	}
	if input.Origin != nil {
		payload["origin"] = rawOrNil(input.Origin)
	}
	if input.NumTurns > 0 {
		payload["num_turns"] = input.NumTurns
	}
	if input.TerminalReason != "" {
		payload["terminal_reason"] = input.TerminalReason
	}
	if n.usageModel != "" {
		payload["model"] = n.usageModel
	}
	payload["is_error"] = input.IsError
	if input.Result != "" {
		payload["text"] = input.Result
	}
	if input.StopReason != "" {
		payload["stop_reason"] = input.StopReason
	}
	if input.DurationMS > 0 {
		payload["duration_ms"] = input.DurationMS
	}
	if input.DurationAPIMS > 0 {
		payload["duration_api_ms"] = input.DurationAPIMS
	}
	if input.TotalCostUSD > 0 {
		payload["total_cost_usd"] = input.TotalCostUSD
	}
	if input.Usage != nil {
		payload["usage"] = rawOrNil(input.Usage)
	}
	if input.ModelUsage != nil {
		payload["model_usage"] = rawOrNil(input.ModelUsage)
	}
	if input.PermissionDenials != nil {
		payload["permission_denials"] = rawOrNil(input.PermissionDenials)
	}
	if stringAt(input.Origin, "kind") == "task-notification" {
		n.turnActivity = false
		n.currentMessageID = ""
		n.messageText = ""
		status := "completed"
		if input.IsError {
			status = "failed"
		}
		return normalizedEvent{Event: agentEvent("provider.claude.event", "system", status, payload)}
	}

	if input.IsError {
		message := firstNonEmpty(input.Result, "claude run failed")
		payload["error"] = message
		n.markTerminal(terminalFailed, message)
		return normalizedEvent{Event: agentEvent("agent.run.failed", "assistant", "failed", payload), Terminal: terminalFailed}
	}
	if n.deferResult {
		// Keep the run alive until Claude takes the queued input and responds.
		return normalizedEvent{Event: agentEvent("provider.claude.event", "system", "completed", payload)}
	}
	if strings.TrimSpace(input.Result) == "" && !n.turnActivity {
		message := "claude ended without responding"
		payload["error"] = message
		n.markTerminal(terminalFailed, message)
		return normalizedEvent{Event: agentEvent("agent.run.failed", "assistant", "failed", payload), Terminal: terminalFailed}
	}
	n.markTerminal(terminalCompleted, "")
	return normalizedEvent{Event: agentEvent("agent.run.completed", "assistant", "completed", payload), Terminal: terminalCompleted}
}

func (n *normalizer) syntheticRunStarted(input *streamEvent, payload map[string]any) normalizedEvent {
	if n.runStarted {
		return normalizedEvent{}
	}
	n.runStarted = true
	payload["provider_event_type"] = "system/init"
	if input.SessionID != "" {
		payload["provider_session_id"] = input.SessionID
	}
	return normalizedEvent{Event: agentEvent("agent.run.started", "assistant", "started", payload)}
}

type claudeToolCall struct {
	ID           string
	Name         string
	Input        map[string]any
	InputEmitted bool
}

type claudeToolResult struct {
	ToolUseID string
	Output    string
	IsError   bool
	RawOutput any
}

func (n *normalizer) normalizeThinkingStart(input *streamEvent) normalizedEvent {
	block, ok := mapAt(input.Event, "content_block")
	if !ok || stringFromMap(block, "type") != "thinking" {
		return normalizedEvent{}
	}
	index, ok := intAt(input.Event, "index")
	if !ok {
		return normalizedEvent{}
	}
	itemID := n.thinkingItemID(index)
	n.thinkingBlockIDs[index] = itemID
	payload := n.thinkingPayload(input, itemID)
	if text := stringFromMap(block, "thinking"); text != "" {
		payload["text"] = text
		n.thinkingText[itemID] += text
	}
	return normalizedEvent{Event: agentEvent("agent.thinking.started", "assistant", "started", payload)}
}

func (n *normalizer) normalizeThinkingDelta(input *streamEvent) normalizedEvent {
	index, ok := intAt(input.Event, "index")
	if !ok {
		return normalizedEvent{}
	}
	itemID := n.thinkingBlockIDs[index]
	if itemID == "" || stringAt(input.Event, "delta", "type") != "thinking_delta" {
		return normalizedEvent{}
	}
	payload := n.thinkingPayload(input, itemID)
	if text := stringAt(input.Event, "delta", "thinking"); text != "" {
		payload["text"] = text
		n.thinkingText[itemID] += text
	}
	return normalizedEvent{Event: agentEvent("agent.thinking.delta", "assistant", "delta", payload)}
}

func (n *normalizer) normalizeThinkingStop(input *streamEvent) normalizedEvent {
	index, ok := intAt(input.Event, "index")
	if !ok {
		return normalizedEvent{}
	}
	itemID := n.thinkingBlockIDs[index]
	if itemID == "" {
		return normalizedEvent{}
	}
	delete(n.thinkingBlockIDs, index)
	payload := n.thinkingPayload(input, itemID)
	if text := n.thinkingText[itemID]; text != "" {
		payload["text"] = text
	}
	delete(n.thinkingText, itemID)
	return normalizedEvent{Event: agentEvent("agent.thinking.completed", "assistant", "completed", payload)}
}

func (n *normalizer) thinkingPayload(input *streamEvent, itemID string) map[string]any {
	payload := basePayload(input)
	payload["provider_event_type"] = stringAt(input.Event, "type")
	payload["item_id"] = itemID
	payload["item_type"] = "thinking"
	payload["raw_event"] = rawOrNil(input.Event)
	if n.currentMessageID != "" {
		payload["message_id"] = n.currentMessageID
	}
	return payload
}

func (n *normalizer) thinkingItemID(index int) string {
	if n.currentMessageID != "" {
		return n.currentMessageID + ":thinking:" + strconv.Itoa(index)
	}
	return "thinking:" + strconv.Itoa(index)
}

func (n *normalizer) normalizeToolUseStart(input *streamEvent) normalizedEvent {
	block, ok := mapAt(input.Event, "content_block")
	if !ok || stringFromMap(block, "type") != "tool_use" {
		return normalizedEvent{}
	}

	tool := claudeToolCall{
		ID:    stringFromMap(block, "id"),
		Name:  stringFromMap(block, "name"),
		Input: mapFromMap(block, "input"),
	}
	if tool.ID == "" {
		return normalizedEvent{}
	}
	n.upsertToolCall(tool)
	if index, ok := intAt(input.Event, "index"); ok {
		n.toolBlockIDs[index] = tool.ID
	}
	return normalizedEvent{Event: agentEvent("tool.call.started", "assistant", "started", n.toolPayload(input, tool))}
}

func (n *normalizer) captureToolInputDelta(input *streamEvent) {
	index, ok := intAt(input.Event, "index")
	if !ok || n.toolBlockIDs[index] == "" {
		return
	}
	if stringAt(input.Event, "delta", "type") != "input_json_delta" {
		return
	}
	n.toolInputDeltas[index] += stringAt(input.Event, "delta", "partial_json")
}

func (n *normalizer) normalizeToolUseInputStop(input *streamEvent) normalizedEvent {
	index, ok := intAt(input.Event, "index")
	if !ok {
		return normalizedEvent{}
	}
	toolID := n.toolBlockIDs[index]
	if toolID == "" {
		return normalizedEvent{}
	}
	delete(n.toolBlockIDs, index)

	tool := n.toolCalls[toolID]
	if tool == nil {
		delete(n.toolInputDeltas, index)
		return normalizedEvent{}
	}
	if tool.Input == nil {
		tool.Input = parseJSONObject(n.toolInputDeltas[index])
	}
	delete(n.toolInputDeltas, index)
	if tool.Input == nil || tool.InputEmitted {
		return normalizedEvent{}
	}
	tool.InputEmitted = true
	return normalizedEvent{Event: agentEvent("tool.call.delta", "assistant", "delta", n.toolPayload(input, *tool))}
}

func (n *normalizer) upsertToolCall(tool claudeToolCall) {
	if tool.ID == "" {
		return
	}
	stored := n.toolCalls[tool.ID]
	if stored == nil {
		n.toolCalls[tool.ID] = &tool
		return
	}
	if tool.Name != "" {
		stored.Name = tool.Name
	}
	if tool.Input != nil {
		stored.Input = tool.Input
	}
}

func (n *normalizer) toolPayload(input *streamEvent, tool claudeToolCall) map[string]any {
	payload := basePayload(input)
	payload["provider_event_type"] = "tool_use"
	payload["tool_call_id"] = tool.ID
	payload["item_id"] = tool.ID
	if tool.Name != "" {
		payload["name"] = tool.Name
		payload["tool"] = tool.Name
		payload["kind"] = tool.Name
		payload["title"] = tool.Name
	}
	if tool.Input != nil {
		payload["raw_input"] = tool.Input
		if command := stringFromMap(tool.Input, "command"); command != "" {
			payload["command"] = command
		}
		if description := stringFromMap(tool.Input, "description"); description != "" {
			payload["description"] = description
		}
		if path := firstStringFromMap(tool.Input, "file_path", "filePath", "path"); path != "" {
			payload["path"] = path
		}
	}
	return payload
}

func (n *normalizer) unknown(input *streamEvent) normalizedEvent {
	payload := basePayload(input)
	payload["raw"] = rawOrNil(input.Raw)
	if input.Type != "" {
		payload["provider_event_type"] = input.Type
	}
	return normalizedEvent{Event: agentEvent("provider.claude.event", "system", "completed", payload)}
}

func (n *normalizer) markTerminal(kind terminalKind, message string) {
	if n.terminal {
		return
	}
	n.terminal = true
	n.terminalKind = kind
	n.terminalError = message
}

func agentEvent(eventType string, role string, status string, payload map[string]any) agents.AgentEvent {
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

func basePayload(input *streamEvent) map[string]any {
	payload := map[string]any{
		"provider": "claude",
	}
	if input.Type != "" {
		payload["provider_event_type"] = input.Type
	}
	if input.SessionID != "" {
		payload["provider_session_id"] = input.SessionID
	}
	if input.UUID != "" {
		payload["uuid"] = input.UUID
	}
	if input.ParentToolUseID != "" {
		payload["parent_tool_use_id"] = input.ParentToolUseID
	}
	return payload
}

func textFromAssistantMessage(raw json.RawMessage) string {
	var message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return ""
	}
	var parts []string
	for _, content := range message.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "")
}

func assistantMessageHasToolUse(raw json.RawMessage) bool {
	return len(toolUsesFromAssistantMessage(raw)) > 0
}

func toolUsesFromAssistantMessage(raw json.RawMessage) []claudeToolCall {
	var message struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil
	}
	tools := make([]claudeToolCall, 0)
	for _, content := range message.Content {
		if content.Type != "tool_use" || content.ID == "" {
			continue
		}
		tools = append(tools, claudeToolCall{
			ID:    content.ID,
			Name:  content.Name,
			Input: content.Input,
		})
	}
	return tools
}

func toolResultsFromUser(raw json.RawMessage) []claudeToolResult {
	root, ok := objectFromRaw(raw)
	if !ok {
		return nil
	}
	message, ok := root["message"].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := message["content"].([]any)
	if !ok {
		return nil
	}
	rawOutput := root["tool_use_result"]
	results := make([]claudeToolResult, 0)
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || stringFromMap(block, "type") != "tool_result" {
			continue
		}
		toolUseID := stringFromMap(block, "tool_use_id")
		if toolUseID == "" {
			continue
		}
		output := stringFromMap(block, "content")
		if output == "" {
			output = outputFromClaudeToolResult(rawOutput)
		}
		results = append(results, claudeToolResult{
			ToolUseID: toolUseID,
			Output:    output,
			IsError:   boolFromMap(block, "is_error"),
			RawOutput: rawOutput,
		})
	}
	return results
}

func outputFromClaudeToolResult(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		stdout := stringFromMap(typed, "stdout")
		stderr := stringFromMap(typed, "stderr")
		switch {
		case stdout != "" && stderr != "":
			return stdout + "\n" + stderr
		case stdout != "":
			return stdout
		default:
			return stderr
		}
	default:
		return ""
	}
}

func objectFromRaw(raw json.RawMessage) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

func mapAt(raw json.RawMessage, path ...string) (map[string]any, bool) {
	value := anyAt(raw, path...)
	object, ok := value.(map[string]any)
	return object, ok
}

func mapFromMap(object map[string]any, key string) map[string]any {
	value, ok := object[key].(map[string]any)
	if !ok || len(value) == 0 {
		return nil
	}
	return value
}

func parseJSONObject(value string) map[string]any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err != nil || len(object) == 0 {
		return nil
	}
	return object
}

func stringAt(raw json.RawMessage, path ...string) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	text, _ := value.(string)
	return text
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

func intAt(raw json.RawMessage, path ...string) (int, bool) {
	switch value := anyAt(raw, path...).(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

func stringFromMap(object map[string]any, key string) string {
	value, ok := object[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func firstStringFromMap(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromMap(object, key); value != "" {
			return value
		}
	}
	return ""
}

func boolFromMap(object map[string]any, key string) bool {
	value, _ := object[key].(bool)
	return value
}

func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(raw)
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
