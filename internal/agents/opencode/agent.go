package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/threave-io/threave/internal/agents"
)

const (
	Type                  = "opencode"
	defaultBinary         = "opencode"
	defaultInterruptGrace = 2 * time.Second
	maxJSONRPCLineBytes   = 64 * 1024 * 1024
	maxStderrLineBytes    = 1024 * 1024
)

type VersionChecker func(ctx context.Context, binary string) (string, error)

type Option func(*Agent)

type Agent struct {
	binary         string
	workspace      string
	interruptGrace time.Duration
	versionChecker VersionChecker

	availabilityMu  sync.Mutex
	availabilitySet bool
	availabilityErr error
	version         string
}

func New(options ...Option) *Agent {
	agent := &Agent{
		binary:         defaultBinary,
		interruptGrace: defaultInterruptGrace,
		versionChecker: defaultVersionChecker,
	}
	for _, option := range options {
		option(agent)
	}
	return agent
}

func WithBinary(binary string) Option {
	return func(agent *Agent) {
		if strings.TrimSpace(binary) != "" {
			agent.binary = strings.TrimSpace(binary)
		}
	}
}

func WithWorkspace(workspace string) Option {
	return func(agent *Agent) {
		agent.workspace = strings.TrimSpace(workspace)
	}
}

func WithInterruptGrace(grace time.Duration) Option {
	return func(agent *Agent) {
		if grace > 0 {
			agent.interruptGrace = grace
		}
	}
}

func WithVersionChecker(checker VersionChecker) Option {
	return func(agent *Agent) {
		if checker != nil {
			agent.versionChecker = checker
		}
	}
}

func (a *Agent) Type() string {
	return Type
}

func (a *Agent) Available() error {
	a.availabilityMu.Lock()
	availabilitySet := a.availabilitySet
	availabilityErr := a.availabilityErr
	a.availabilityMu.Unlock()
	if availabilitySet {
		return availabilityErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := a.CheckAvailability(ctx)
	return err
}

func (a *Agent) CheckAvailability(ctx context.Context) (string, error) {
	version, err := a.versionChecker(ctx, a.binary)
	if err != nil {
		err = fmt.Errorf("%w: opencode binary %q: %v", agents.ErrUnavailable, a.binary, err)
	}

	a.availabilityMu.Lock()
	defer a.availabilityMu.Unlock()
	a.availabilitySet = true
	a.version = version
	a.availabilityErr = err
	return version, err
}

func defaultVersionChecker(ctx context.Context, binary string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func normalizeModelList(output string) agents.Options {
	lines := strings.Split(output, "\n")
	models := make([]agents.ModelOption, 0, len(lines))
	seen := make(map[string]bool)
	for _, line := range lines {
		modelID := strings.TrimSpace(line)
		if modelID == "" || !strings.Contains(modelID, "/") || seen[modelID] {
			continue
		}
		seen[modelID] = true
		models = append(models, agents.ModelOption{
			ID:          modelID,
			Model:       modelID,
			DisplayName: displayNameForModel(modelID),
		})
	}

	defaultModel := ""
	for _, model := range models {
		if !strings.HasPrefix(model.Model, "opencode/") {
			defaultModel = model.Model
			break
		}
	}
	if defaultModel == "" && len(models) > 0 {
		defaultModel = models[0].Model
	}
	for index := range models {
		models[index].IsDefault = models[index].Model == defaultModel
	}

	return agents.Options{
		DefaultModel: defaultModel,
		Models:       models,
		CollaborationModes: []agents.CollaborationModeOption{
			{Name: "build", Mode: "build"},
			{Name: "plan", Mode: "plan"},
		},
	}
}

func displayNameForModel(modelID string) string {
	provider, model, ok := strings.Cut(modelID, "/")
	if !ok {
		return modelID
	}
	return providerDisplayName(provider) + "/" + modelDisplayName(model)
}

func providerDisplayName(provider string) string {
	if provider == "" {
		return "Provider"
	}
	switch strings.ToLower(provider) {
	case "openai":
		return "OpenAI"
	case "opencode":
		return "OpenCode Zen"
	default:
		return strings.ToUpper(provider[:1]) + provider[1:]
	}
}

func modelDisplayName(model string) string {
	if model == "" {
		return "Model"
	}
	parts := strings.Split(model, "-")
	for index, part := range parts {
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		switch {
		case upper == "GPT":
			parts[index] = "GPT"
		case strings.HasPrefix(part, "v") && len(part) > 1:
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		default:
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func (a *Agent) Options(ctx context.Context) (agents.Options, error) {
	if err := ctx.Err(); err != nil {
		return agents.Options{}, err
	}
	if err := a.Available(); err != nil {
		return agents.Options{}, err
	}

	output, err := exec.CommandContext(ctx, a.binary, "models").CombinedOutput()
	if err != nil {
		return agents.Options{}, fmt.Errorf("load opencode models: %w", err)
	}
	return normalizeModelList(string(output)), nil
}

func (a *Agent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Available(); err != nil {
		return err
	}
	if input.Action != "" && input.Action != agents.AgentActionMessage {
		return fmt.Errorf("unsupported opencode agent action %q", input.Action)
	}

	workdir, err := a.workdirForRun(input.Workdir)
	if err != nil {
		return err
	}

	cmd := a.command(workdir)
	if err := agents.ApplyEnvironment(cmd, input.Environment); err != nil {
		return fmt.Errorf("configure opencode environment: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create opencode stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create opencode stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create opencode stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start opencode acp: %w", err)
	}

	run := &acpRun{
		agent:             a,
		rpc:               newRPCClient(stdin),
		incoming:          readACP(stdout, stderr),
		process:           waitProcess(cmd),
		emit:              emit,
		normalizer:        newNormalizer(),
		sessionID:         input.SessionID,
		providerSessionID: strings.TrimSpace(input.ProviderSessionID),
		attachments:       input.Attachments,
		userInput:         input.UserInput,
		permissions:       input.Permissions,
		options:           runOptionsFromMetadata(input.Metadata),
	}
	return run.execute(ctx, input.ProviderMessage(), workdir)
}

func (a *Agent) workdirForRun(inputWorkdir string) (string, error) {
	workdir := strings.TrimSpace(inputWorkdir)
	if workdir == "" {
		workdir = a.workspace
	}
	if workdir == "" {
		var err error
		workdir, err = filepath.Abs(".")
		if err != nil {
			return "", fmt.Errorf("resolve current workspace: %w", err)
		}
	}
	if !filepath.IsAbs(workdir) {
		abs, err := filepath.Abs(workdir)
		if err != nil {
			return "", fmt.Errorf("resolve workspace %q: %w", workdir, err)
		}
		workdir = abs
	}
	return workdir, nil
}

func (a *Agent) command(workdir string) *exec.Cmd {
	// Set the process directory directly; newer OpenCode ACP versions reject --cwd.
	cmd := exec.Command(a.binary, "acp")
	cmd.Dir = workdir
	return cmd
}

type acpRun struct {
	agent             *Agent
	rpc               *rpcClient
	incoming          <-chan incomingMessage
	process           *processState
	emit              agents.EmitFunc
	normalizer        *normalizer
	sessionID         string
	providerSessionID string
	attachments       []agents.Attachment
	userInput         agents.UserInputBroker
	permissions       agents.PermissionBroker
	options           openCodeRunOptions
	modelConfigID     string

	stateMu    sync.Mutex
	acpSession string
}

type openCodeRunOptions struct {
	Model            string
	PlanningMode     bool
	PermissionPolicy string
}

func runOptionsFromMetadata(metadata map[string]any) openCodeRunOptions {
	rawOptions, ok := metadata["opencode_options"].(map[string]any)
	if !ok {
		return openCodeRunOptions{}
	}
	return openCodeRunOptions{
		Model:            stringMetadataValue(rawOptions, "model"),
		PlanningMode:     boolMetadataValue(rawOptions, "planning_mode"),
		PermissionPolicy: effectivePermissionPolicy(rawOptions),
	}
}

func effectivePermissionPolicy(options map[string]any) string {
	policy := stringMetadataValue(options, "permission_policy")
	if policy == "" {
		return "deny"
	}
	return policy
}

func (r *acpRun) execute(ctx context.Context, message string, workdir string) error {
	cancelWatchDone := r.watchCancellation(ctx)
	defer close(cancelWatchDone)
	defer r.stop()

	if err := r.initialize(ctx); err != nil {
		return err
	}
	if err := r.setupSession(ctx, workdir); err != nil {
		return err
	}
	if err := r.configureSession(ctx); err != nil {
		return err
	}
	if err := r.prompt(ctx, message); err != nil {
		return err
	}
	return r.terminalReturn(ctx)
}

func (r *acpRun) initialize(ctx context.Context) error {
	id, err := r.rpc.sendRequest("initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "threave",
			"title":   "Threave",
			"version": "0.0.0",
		},
		"capabilities": map[string]any{},
	})
	if err != nil {
		return err
	}

	response, err := r.awaitResponse(ctx, id)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("opencode initialize failed: %s", response.Error.Message)
	}
	return nil
}

func (r *acpRun) setupSession(ctx context.Context, workdir string) error {
	method := "session/new"
	params := map[string]any{
		"cwd":        workdir,
		"mcpServers": []any{},
	}
	if r.providerSessionID != "" {
		method = "session/resume"
		params["sessionId"] = r.providerSessionID
	}

	id, err := r.rpc.sendRequest(method, params)
	if err != nil {
		return err
	}
	response, err := r.awaitResponse(ctx, id)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("opencode %s failed: %s", method, response.Error.Message)
	}

	sessionID := r.providerSessionID
	if sessionID == "" {
		sessionID = stringAt(response.Result, "sessionId")
	}
	if sessionID == "" {
		return fmt.Errorf("opencode %s response missing sessionId", method)
	}
	r.setACPSession(sessionID)
	r.modelConfigID = modelConfigOptionID(response.Result)

	return r.emit(ctx, agents.AgentEvent{
		Type:   "agent.run.started",
		Role:   "assistant",
		Status: "started",
		Payload: map[string]any{
			"provider":            "opencode",
			"provider_event_type": method,
			"provider_session_id": sessionID,
		},
	})
}

func (r *acpRun) configureSession(ctx context.Context) error {
	if r.options.Model != "" {
		if err := r.setModel(ctx, r.options.Model); err != nil {
			return err
		}
	}
	if r.options.PlanningMode {
		if err := r.setMode(ctx, "plan"); err != nil {
			return err
		}
	}
	return nil
}

func (r *acpRun) setModel(ctx context.Context, model string) error {
	method := "session/set_model"
	params := map[string]any{
		"sessionId": r.getACPSession(),
		"modelId":   model,
	}
	if r.modelConfigID != "" {
		method = "session/set_config_option"
		params = map[string]any{"sessionId": r.getACPSession(), "configId": r.modelConfigID, "value": model}
	}
	id, err := r.rpc.sendRequest(method, params)
	if err != nil {
		return err
	}
	response, err := r.awaitResponse(ctx, id)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("opencode %s failed: %s", method, response.Error.Message)
	}
	return nil
}

// ACP config selectors supersede the legacy model method when advertised by
// session setup. Use the returned ID rather than assuming a provider's ID.
func modelConfigOptionID(result json.RawMessage) string {
	var response struct {
		ConfigOptions []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Type     string `json:"type"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(result, &response) != nil {
		return ""
	}
	for _, option := range response.ConfigOptions {
		if option.Type == "select" && (option.Category == "model" || option.ID == "model") {
			return option.ID
		}
	}
	return ""
}

func (r *acpRun) setMode(ctx context.Context, mode string) error {
	id, err := r.rpc.sendRequest("session/set_mode", map[string]any{
		"sessionId": r.getACPSession(),
		"modeId":    mode,
	})
	if err != nil {
		return err
	}
	response, err := r.awaitResponse(ctx, id)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("opencode session/set_mode failed: %s", response.Error.Message)
	}
	return nil
}

func (r *acpRun) prompt(ctx context.Context, message string) error {
	prompt, err := promptContent(message, r.attachments)
	if err != nil {
		return err
	}
	id, err := r.rpc.sendRequest("session/prompt", map[string]any{
		"sessionId": r.getACPSession(),
		"prompt":    prompt,
	})
	if err != nil {
		return err
	}

	response, err := r.awaitResponse(ctx, id)
	if err != nil {
		return err
	}
	if response.Error != nil {
		normalized := r.normalizer.failed(response.Error.Message, response.Raw)
		if emitErr := r.emitEvent(ctx, normalized); emitErr != nil {
			return emitErr
		}
		return response.Error
	}

	stopReason := stringAt(response.Result, "stopReason")
	if stopReason == "" {
		stopReason = "end_turn"
	}
	for _, event := range r.normalizer.completed(stopReason, response.Result) {
		if err := r.emitEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func promptContent(message string, attachments []agents.Attachment) ([]map[string]any, error) {
	content := make([]map[string]any, 0, 1+len(attachments))
	if strings.TrimSpace(message) != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": message,
		})
	}
	for _, attachment := range attachments {
		data, mimeType, ok := strings.Cut(strings.TrimSpace(attachment.DataURL), ",")
		if !ok || !strings.HasPrefix(data, "data:") {
			return nil, fmt.Errorf("opencode attachment %q must be a data URL", attachment.Name)
		}
		mediaType := strings.TrimPrefix(data, "data:")
		mediaType = strings.TrimSuffix(mediaType, ";base64")
		if mediaType == "" {
			mediaType = attachment.MediaType
		}
		if mediaType == "" {
			return nil, fmt.Errorf("opencode attachment %q is missing media type", attachment.Name)
		}
		if _, err := base64.StdEncoding.DecodeString(mimeType); err != nil {
			return nil, fmt.Errorf("opencode attachment %q has invalid base64 data: %w", attachment.Name, err)
		}
		content = append(content, map[string]any{
			"type":     "image",
			"mimeType": mediaType,
			"data":     mimeType,
		})
	}
	return content, nil
}

func (r *acpRun) awaitResponse(ctx context.Context, requestID string) (*rpcMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.process.done:
			return nil, r.processExitBeforeResponse(requestID)
		case incoming, ok := <-r.incoming:
			if !ok {
				return nil, fmt.Errorf("opencode acp closed before response %s", requestID)
			}
			response, matched, err := r.handleIncoming(ctx, incoming, requestID)
			if err != nil {
				return nil, err
			}
			if matched {
				return response, nil
			}
		}
	}
}

func (r *acpRun) handleIncoming(ctx context.Context, incoming incomingMessage, responseID string) (*rpcMessage, bool, error) {
	if incoming.Stderr != "" {
		return nil, false, r.emit(ctx, agents.AgentEvent{
			Type:   "agent.log.delta",
			Role:   "system",
			Status: "delta",
			Payload: map[string]any{
				"provider": "opencode",
				"text":     incoming.Stderr,
			},
		})
	}
	if incoming.ParseErr != nil {
		event := agents.AgentEvent{
			Type:   "provider.opencode.parse_error",
			Role:   "system",
			Status: "failed",
			Payload: map[string]any{
				"provider":            "opencode",
				"provider_event_type": "parse_error",
				"line":                incoming.ParseErr.Line,
				"error":               incoming.ParseErr.Err.Error(),
				"raw":                 incoming.ParseErr.Raw,
			},
		}
		if err := r.emit(ctx, event); err != nil {
			return nil, false, err
		}
		return nil, false, incoming.ParseErr
	}
	if incoming.ReadErr != nil {
		return nil, false, incoming.ReadErr
	}

	message := incoming.Message
	if message == nil {
		return nil, false, nil
	}
	if message.Method != "" && len(message.ID) > 0 {
		if err := r.handleServerRequest(ctx, message); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if message.Method != "" {
		for _, normalized := range r.normalizer.normalize(message.Method, message.Params) {
			if err := r.emitEvent(ctx, normalized); err != nil {
				return nil, false, err
			}
		}
		return nil, false, nil
	}
	if len(message.ID) > 0 {
		return message, message.idKey() == responseID, nil
	}

	return nil, false, r.emitEvent(ctx, unknown("provider.opencode.event", "unknown", message.Raw))
}

func (r *acpRun) handleServerRequest(ctx context.Context, message *rpcMessage) error {
	switch message.Method {
	case "session/request_permission":
		return r.handlePermissionRequest(ctx, message)
	default:
		if err := r.emit(ctx, agents.AgentEvent{
			Type:   "provider.opencode.request",
			Role:   "system",
			Status: "started",
			Payload: map[string]any{
				"provider":            "opencode",
				"provider_event_type": message.Method,
				"raw":                 json.RawMessage(message.Raw),
			},
		}); err != nil {
			return err
		}
		return r.rpc.sendError(message.ID, -32601, fmt.Sprintf("unsupported opencode request %q", message.Method))
	}
}

func (r *acpRun) handlePermissionRequest(ctx context.Context, message *rpcMessage) error {
	options := permissionOptions(message.Params)
	if r.options.PermissionPolicy == "deny" {
		return r.sendAutomaticPermissionResponse(message, options, "deny")
	}
	if r.options.PermissionPolicy == "bypass" {
		return r.sendAutomaticPermissionResponse(message, options, "allow")
	}
	if r.permissions == nil {
		return r.rpc.sendResponse(message.ID, map[string]any{
			"outcome": map[string]any{"outcome": "cancelled"},
		})
	}

	requestID := message.idKey()
	request := permissionRequest(message.Params, r.sessionID, requestID, r.getACPSession(), options)
	waiter, err := r.permissions.OpenPermission(ctx, request)
	if err != nil {
		return err
	}
	defer waiter.Close()
	if err := r.emit(ctx, agents.AgentEvent{Type: "agent.permission.requested", Role: "assistant", Status: "started", Payload: request}); err != nil {
		return err
	}
	response, err := waiter.Wait(ctx)
	if err != nil {
		return r.rpc.sendResponse(message.ID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	return r.sendSelectedPermission(message, response.OptionID)
}

func permissionRequest(raw json.RawMessage, sessionID string, requestID string, threadID string, options []agents.PermissionOption) agents.PermissionRequest {
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	toolCall, _ := params["toolCall"].(map[string]any)
	title := stringFromMap(toolCall, "title")
	if title == "" {
		title = "OpenCode permission"
	}
	return agents.PermissionRequest{
		SessionID: sessionID,
		RequestID: requestID, Provider: "opencode", ProviderEventType: "session/request_permission",
		ProviderRequestID: requestID, ThreadID: threadID, Kind: firstNonEmpty(stringFromMap(toolCall, "kind"), "tool"),
		Title: title, Description: permissionQuestion(raw), ToolName: title, ToolInput: toolCall["rawInput"], Options: options,
	}
}

func (r *acpRun) sendSelectedPermission(message *rpcMessage, optionID string) error {
	if strings.TrimSpace(optionID) == "" {
		return r.rpc.sendResponse(message.ID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	return r.rpc.sendResponse(message.ID, map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": optionID},
	})
}

func (r *acpRun) sendAutomaticPermissionResponse(message *rpcMessage, options []agents.PermissionOption, decision string) error {
	for _, preferredScope := range []string{"session", "once", ""} {
		for _, option := range options {
			if option.Decision == decision && (preferredScope == "" || option.Scope == preferredScope) {
				return r.sendSelectedPermission(message, option.ID)
			}
		}
	}
	return r.sendSelectedPermission(message, "")
}

func permissionOptions(raw json.RawMessage) []agents.PermissionOption {
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	rawOptions, _ := params["options"].([]any)
	options := make([]agents.PermissionOption, 0, len(rawOptions))
	for _, rawOption := range rawOptions {
		option, ok := rawOption.(map[string]any)
		if !ok {
			continue
		}
		label := firstNonEmpty(stringFromMap(option, "name"), stringFromMap(option, "optionId"))
		kind := stringFromMap(option, "kind")
		description := kind
		if optionID := stringFromMap(option, "optionId"); optionID != "" && optionID != label {
			description = firstNonEmpty(description, optionID)
		}
		if label != "" {
			decision := "allow"
			if strings.Contains(strings.ToLower(kind), "reject") || strings.Contains(strings.ToLower(kind), "deny") {
				decision = "deny"
			}
			scope := "once"
			if strings.Contains(strings.ToLower(kind), "always") {
				scope = "session"
			}
			options = append(options, agents.PermissionOption{ID: stringFromMap(option, "optionId"), Label: label, Description: description, Decision: decision, Scope: scope})
		}
	}
	return options
}

func permissionQuestion(raw json.RawMessage) string {
	title := stringAt(raw, "toolCall", "title")
	if title == "" {
		return "Allow OpenCode to continue?"
	}
	return "Allow OpenCode to run: " + title
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringMetadataValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func boolMetadataValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func (r *acpRun) emitEvent(ctx context.Context, normalized normalizedEvent) error {
	if normalized.Event.Type == "" {
		return nil
	}
	if err := r.emit(ctx, normalized.Event); err != nil {
		return err
	}
	return nil
}

func (r *acpRun) watchCancellation(ctx context.Context) chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if sessionID := r.getACPSession(); sessionID != "" {
				_ = r.rpc.sendNotification("session/cancel", map[string]any{"sessionId": sessionID})
			}
		case <-done:
		}
	}()
	return done
}

func (r *acpRun) terminalReturn(ctx context.Context) error {
	switch r.normalizer.terminalKind {
	case terminalCompleted:
		return nil
	case terminalCancelled:
		return context.Canceled
	case terminalFailed:
		if r.normalizer.terminalError != "" {
			return errors.New(r.normalizer.terminalError)
		}
		return errors.New("opencode run failed")
	default:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

func (r *acpRun) processExitBeforeResponse(requestID string) error {
	if err := r.process.err(); err != nil {
		return fmt.Errorf("opencode acp exited before response %s: %w", requestID, err)
	}
	return fmt.Errorf("opencode acp exited before response %s", requestID)
}

func (r *acpRun) stop() {
	_ = r.rpc.Close()
	if _, ok := r.process.waitTimeout(r.agent.interruptGrace); ok {
		return
	}
	r.process.kill()
	_, _ = r.process.waitTimeout(r.agent.interruptGrace)
}

func (r *acpRun) setACPSession(sessionID string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.acpSession = sessionID
}

func (r *acpRun) getACPSession() string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.acpSession
}

type incomingMessage struct {
	Message  *rpcMessage
	Stderr   string
	ParseErr *ParseError
	ReadErr  error
}

type ParseError struct {
	Line int
	Raw  string
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse opencode JSON-RPC line %d: %v", e.Line, e.Err)
}

func readACP(stdout io.Reader, stderr io.Reader) <-chan incomingMessage {
	incoming := make(chan incomingMessage, 128)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanJSONRPC(stdout, incoming)
	}()
	go func() {
		defer wg.Done()
		scanStderr(stderr, incoming)
	}()
	go func() {
		wg.Wait()
		close(incoming)
	}()
	return incoming
}

func scanJSONRPC(reader io.Reader, incoming chan<- incomingMessage) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONRPCLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		message, err := parseRPCMessage(raw)
		if err != nil {
			incoming <- incomingMessage{ParseErr: &ParseError{Line: line, Raw: string(raw), Err: err}}
			continue
		}
		incoming <- incomingMessage{Message: message}
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, os.ErrClosed) {
			incoming <- incomingMessage{ReadErr: fmt.Errorf("read opencode stdout: %w", err)}
		}
	}
}

func scanStderr(reader io.Reader, incoming chan<- incomingMessage) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStderrLineBytes)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		incoming <- incomingMessage{Stderr: line}
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, os.ErrClosed) {
			incoming <- incomingMessage{ReadErr: fmt.Errorf("read opencode stderr: %w", err)}
		}
	}
}

type rpcClient struct {
	mu     sync.Mutex
	writer io.WriteCloser
	nextID int64
}

func newRPCClient(writer io.WriteCloser) *rpcClient {
	return &rpcClient{writer: writer}
}

func (c *rpcClient) sendRequest(method string, params any) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	return id, c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
		"params":  params,
	})
}

func (c *rpcClient) sendNotification(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *rpcClient) sendResponse(id json.RawMessage, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (c *rpcClient) sendError(id json.RawMessage, code int, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func (c *rpcClient) writeLocked(message map[string]any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.writer.Write(raw)
	return err
}

func (c *rpcClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writer.Close()
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return e.Message
}

func parseRPCMessage(raw []byte) (*rpcMessage, error) {
	var message rpcMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, err
	}
	message.Raw = append([]byte(nil), raw...)
	return &message, nil
}

func (m *rpcMessage) idKey() string {
	return strings.Trim(string(m.ID), `"`)
}

type processState struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu      sync.Mutex
	waitErr error
}

func waitProcess(cmd *exec.Cmd) *processState {
	state := &processState{
		cmd:  cmd,
		done: make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		state.mu.Lock()
		state.waitErr = err
		state.mu.Unlock()
		close(state.done)
	}()
	return state
}

func (p *processState) waitTimeout(timeout time.Duration) (error, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.err(), true
	case <-timer.C:
		return nil, false
	}
}

func (p *processState) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *processState) kill() {
	if p.cmd.Process == nil {
		return
	}
	select {
	case <-p.done:
		return
	default:
		_ = p.cmd.Process.Kill()
	}
}
