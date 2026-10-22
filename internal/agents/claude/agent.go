package claude

import (
	"bufio"
	"context"
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
	Type                  = "claude"
	defaultBinary         = "claude"
	defaultInterruptGrace = 2 * time.Second
)

type VersionChecker func(ctx context.Context, binary string) (string, error)

type Option func(*Agent)

type Agent struct {
	binary         string
	model          string
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

func WithModel(model string) Option {
	return func(agent *Agent) {
		agent.model = strings.TrimSpace(model)
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
		err = fmt.Errorf("%w: claude binary %q: %v", agents.ErrUnavailable, a.binary, err)
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

func (a *Agent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Available(); err != nil {
		return err
	}
	if input.Action != "" && input.Action != agents.AgentActionMessage && input.Action != agents.AgentActionCompact {
		return fmt.Errorf("unsupported claude agent action %q", input.Action)
	}
	if input.Action == agents.AgentActionCompact && strings.TrimSpace(input.ProviderSessionID) == "" {
		return errors.New("claude compaction requires an existing provider session")
	}

	workdir, err := a.workdirForRun(input.Workdir)
	if err != nil {
		return err
	}

	options := runOptionsFromMetadata(input.Metadata)
	message := messageWithSkills(input.ProviderMessage(), input.Skills)
	attachments := input.Attachments
	if input.Action == agents.AgentActionCompact {
		// Slash commands must be the entire input, without runtime context,
		// skill instructions, or attachments that would turn them into a prompt.
		message = "/compact"
		attachments = nil
	}
	content, err := userMessageContent(message, attachments)
	if err != nil {
		return err
	}
	cmd := a.commandWithOptions(strings.TrimSpace(input.ProviderSessionID), workdir, options)
	steering := input.Steering
	if input.Action == agents.AgentActionCompact {
		steering = nil
	}
	if steering != nil {
		cmd.Args = append(cmd.Args, "--replay-user-messages")
	}
	if err := agents.ApplyEnvironment(cmd, input.Environment); err != nil {
		return fmt.Errorf("configure claude environment: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create claude stdin pipe: %w", err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create claude stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create claude stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outputCtx, cancelOutput := context.WithCancel(runCtx)
	output := startStream(outputCtx, stdout, stderr)
	process := waitProcess(cmd, output.done)
	// A large image may fill stdin before execute reaches its select loop.
	// Cancellation must be able to unblock that write even if Claude stops reading.
	go func() {
		select {
		case <-runCtx.Done():
			process.kill()
		case <-process.done:
		}
	}()
	run := &streamRun{
		agent:          a,
		incoming:       output.incoming,
		process:        process,
		cancelOutput:   cancelOutput,
		emit:           emit,
		normalizer:     newNormalizer(),
		stdin:          stdin,
		sessionID:      input.SessionID,
		content:        content,
		userInput:      input.UserInput,
		permissions:    input.Permissions,
		options:        options,
		steeringBroker: steering,
		runID:          input.RunID,
	}
	err = run.execute(runCtx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
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

func (a *Agent) command(providerSessionID string, workdir string) *exec.Cmd {
	return a.commandWithOptions(providerSessionID, workdir, claudeRunOptions{})
}

func (a *Agent) commandWithOptions(providerSessionID string, workdir string, options claudeRunOptions) *exec.Cmd {
	// Keep image input available in every mode, and questions where the mode permits.
	// Ordinary tool approvals still follow the selected permission policy.
	args := []string{"--output-format", "stream-json", "--verbose", "--include-partial-messages", "--input-format", "stream-json", "--permission-prompt-tool", "stdio", "-p"}
	model := a.model
	if options.Model != "" {
		model = options.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if options.Effort != "" {
		args = append(args, "--effort", options.Effort)
	}
	if options.PermissionMode != "" {
		args = append(args, "--permission-mode", options.PermissionMode)
	} else if options.permissionPolicy() == "deny" {
		args = append(args, "--permission-mode", "dontAsk")
	} else if options.permissionPolicy() == "bypass" {
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	if options.permissionPolicy() == "bypass" {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	if providerSessionID != "" {
		args = append(args, "--resume", providerSessionID)
	}
	cmd := exec.Command(a.binary, args...)
	cmd.Dir = workdir
	return cmd
}

type claudeRunOptions struct {
	RunDangerously   bool
	Model            string
	Effort           string
	PermissionMode   string
	PermissionPolicy string
}

func runOptionsFromMetadata(metadata map[string]any) claudeRunOptions {
	rawOptions, ok := metadata["claude_options"].(map[string]any)
	if !ok {
		return claudeRunOptions{}
	}
	return claudeRunOptions{
		RunDangerously:   boolMetadataValue(rawOptions, "run_dangerously"),
		Model:            stringMetadataValue(rawOptions, "model"),
		Effort:           stringMetadataValue(rawOptions, "effort"),
		PermissionMode:   stringMetadataValue(rawOptions, "permission_mode"),
		PermissionPolicy: stringMetadataValue(rawOptions, "permission_policy"),
	}
}

func (o claudeRunOptions) permissionPolicy() string {
	if o.PermissionPolicy != "" {
		return o.PermissionPolicy
	}
	if o.RunDangerously {
		return "bypass"
	}
	return "deny"
}

func stringMetadataValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func boolMetadataValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

type streamRun struct {
	steeringBroker agents.SteeringBroker
	runID          string
	steering       *claudeSteering
	agent          *Agent
	incoming       <-chan incomingMessage
	process        *processState
	cancelOutput   context.CancelFunc
	emit           agents.EmitFunc
	normalizer     *normalizer
	stdin          io.WriteCloser
	writeMu        sync.Mutex
	sessionID      string
	content        any
	userInput      agents.UserInputBroker
	permissions    agents.PermissionBroker
	options        claudeRunOptions
	controls       map[string]*pendingControl
	controlResults chan controlResult
	controlWaiters sync.WaitGroup
}

func (r *streamRun) startInteractive() error {
	requestID := fmt.Sprintf("gorchestra-init-%d", time.Now().UnixNano())
	if err := r.writeJSON(map[string]any{"type": "control_request", "request_id": requestID, "request": map[string]any{"subtype": "initialize", "hooks": nil}}); err != nil {
		return err
	}
	return r.writeJSON(map[string]any{
		"type":               "user",
		"session_id":         "",
		"message":            map[string]any{"role": "user", "content": r.content},
		"parent_tool_use_id": nil,
		"origin":             map[string]any{"kind": "human"},
	})
}

func (r *streamRun) writeJSON(value any) error {
	if r.stdin == nil {
		return errors.New("claude stdin is unavailable")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if _, err := r.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write claude control response: %w", err)
	}
	return nil
}

func (r *streamRun) execute(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.steering = newClaudeSteering()
	r.controls = make(map[string]*pendingControl)
	r.controlResults = make(chan controlResult)
	defer func() {
		cancel()
		r.steering.close()
		r.cancelOutput()
		for _, pending := range r.controls {
			pending.cancel()
			pending.close()
		}
		_ = r.stdin.Close()
		r.steering.writers.Wait()
		r.stop()
		r.controlWaiters.Wait()
	}()
	if err := r.startInteractive(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case request := <-r.steering.requests:
			r.startSteeringWrite(ctx, request)
		case update := <-r.steering.updates:
			if err := r.finishSteeringUpdate(ctx, update); err != nil {
				return err
			}
			if r.normalizer.terminal {
				return r.terminalReturn()
			}
		case result := <-r.controlResults:
			if err := r.finishControl(result); err != nil {
				return err
			}
		case <-r.process.done:
			if r.normalizer.terminal {
				return r.terminalReturn()
			}
			return r.awaitTerminalAfterProcessExit(ctx)
		case incoming, ok := <-r.incoming:
			if !ok {
				if r.normalizer.terminal {
					return r.terminalReturn()
				}
				if r.process.isDone() {
					return r.processExitBeforeTerminal()
				}
				if _, ok := r.process.waitTimeout(r.agent.interruptGrace); ok {
					return r.processExitBeforeTerminal()
				}
				return fmt.Errorf("claude stream output closed before terminal event")
			}
			if err := r.handleIncoming(ctx, incoming); err != nil {
				return err
			}
			if r.normalizer.terminal {
				return r.terminalReturn()
			}
		}
	}
}

func (r *streamRun) awaitTerminalAfterProcessExit(ctx context.Context) error {
	timer := time.NewTimer(r.agent.interruptGrace)
	defer timer.Stop()

	for {
		if r.normalizer.terminal {
			return r.terminalReturn()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case incoming, ok := <-r.incoming:
			if !ok {
				return r.processExitBeforeTerminal()
			}
			if err := r.handleIncoming(ctx, incoming); err != nil {
				return err
			}
			if r.normalizer.terminal {
				return r.terminalReturn()
			}
		case <-timer.C:
			return r.processExitBeforeTerminal()
		}
	}
}

func (r *streamRun) handleIncoming(ctx context.Context, incoming incomingMessage) error {
	if incoming.ParseErr != nil {
		payload := map[string]any{
			"provider": "claude",
			"error":    incoming.ParseErr.Err.Error(),
			"line":     incoming.ParseErr.Line,
			"raw":      incoming.ParseErr.Raw,
		}
		return r.emit(ctx, agentEvent("provider.claude.parse_error", "system", "failed", payload))
	}
	if incoming.ReadErr != nil {
		return incoming.ReadErr
	}
	if incoming.Stderr != "" {
		payload := map[string]any{
			"provider": "claude",
			"text":     incoming.Stderr,
		}
		return r.emit(ctx, agentEvent("agent.log.delta", "system", "delta", payload))
	}
	if incoming.Event == nil {
		return nil
	}
	if incoming.Event.Type == "control_request" {
		return r.handleControlRequest(ctx, incoming.Event)
	}
	if incoming.Event.Type == "control_cancel_request" {
		return r.cancelControl(ctx, incoming.Event)
	}
	if err := r.handleSteeringEvent(ctx, incoming.Event); err != nil {
		return err
	}
	if incoming.Event.Type == "user" && incoming.Event.IsReplay {
		// Replays acknowledge input already persisted by the shared submission
		// flow. Do not duplicate prompt text or image data into provider events.
		return nil
	}
	for _, normalized := range r.normalizer.normalize(incoming.Event) {
		if err := r.emit(ctx, normalized.Event); err != nil {
			return err
		}
	}
	return nil
}

func sessionPermissionSuggestions(suggestions []any) []any {
	result := make([]any, 0, len(suggestions))
	for _, raw := range suggestions {
		if suggestion, ok := raw.(map[string]any); ok {
			copy := make(map[string]any, len(suggestion)+1)
			for key, value := range suggestion {
				copy[key] = value
			}
			copy["destination"] = "session"
			result = append(result, copy)
		}
	}
	return result
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func (r *streamRun) terminalReturn() error {
	switch r.normalizer.terminalKind {
	case terminalCompleted:
		return nil
	case terminalFailed:
		if r.normalizer.terminalError != "" {
			return fmt.Errorf("%s", r.normalizer.terminalError)
		}
		return fmt.Errorf("claude run failed")
	default:
		return nil
	}
}

func (r *streamRun) processExitBeforeTerminal() error {
	if err := r.process.err(); err != nil {
		return fmt.Errorf("claude exited before terminal event: %w", err)
	}
	return fmt.Errorf("claude exited before terminal event")
}

func (r *streamRun) stop() {
	if _, ok := r.process.waitTimeout(r.agent.interruptGrace); ok {
		return
	}
	r.process.kill()
	_, _ = r.process.waitTimeout(r.agent.interruptGrace)
}

type incomingMessage struct {
	Event    *streamEvent
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
	return fmt.Sprintf("parse claude stream JSON line %d: %v", e.Line, e.Err)
}

type streamOutput struct {
	incoming <-chan incomingMessage
	done     <-chan struct{}
}

func readStream(ctx context.Context, stdout io.Reader, stderr io.Reader) <-chan incomingMessage {
	return startStream(ctx, stdout, stderr).incoming
}

func startStream(ctx context.Context, stdout io.Reader, stderr io.Reader) streamOutput {
	incoming := make(chan incomingMessage, 128)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanStream(ctx, stdout, incoming)
	}()
	go func() {
		defer wg.Done()
		scanStderr(ctx, stderr, incoming)
	}()
	go func() {
		wg.Wait()
		close(incoming)
		close(done)
	}()
	return streamOutput{incoming: incoming, done: done}
}

func scanStream(ctx context.Context, reader io.Reader, incoming chan<- incomingMessage) {
	scanner := bufio.NewScanner(reader)
	// Replayed inputs can contain eight 5 MiB attachments encoded as base64.
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		event, err := parseStreamEvent([]byte(raw))
		if err != nil {
			if !sendIncoming(ctx, incoming, incomingMessage{ParseErr: &ParseError{Line: line, Raw: raw, Err: err}}) {
				return
			}
			continue
		}
		if !sendIncoming(ctx, incoming, incomingMessage{Event: event}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, os.ErrClosed) {
			sendIncoming(ctx, incoming, incomingMessage{ReadErr: fmt.Errorf("read claude stdout: %w", err)})
		}
	}
}

func scanStderr(ctx context.Context, reader io.Reader, incoming chan<- incomingMessage) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		if !sendIncoming(ctx, incoming, incomingMessage{Stderr: line}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, os.ErrClosed) {
			sendIncoming(ctx, incoming, incomingMessage{ReadErr: fmt.Errorf("read claude stderr: %w", err)})
		}
	}
}

func sendIncoming(ctx context.Context, incoming chan<- incomingMessage, message incomingMessage) bool {
	select {
	case incoming <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func parseStreamEvent(raw []byte) (*streamEvent, error) {
	var event streamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	if event.Type == "user" && event.IsReplay {
		event.Message = nil
		return &event, nil
	}
	event.Raw = append([]byte(nil), raw...)
	return &event, nil
}

type streamEvent struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype,omitempty"`
	Status            string          `json:"status,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
	ParentToolUseID   string          `json:"parent_tool_use_id,omitempty"`
	UUID              string          `json:"uuid,omitempty"`
	IsReplay          bool            `json:"isReplay,omitempty"`
	CWD               string          `json:"cwd,omitempty"`
	Tools             []string        `json:"tools,omitempty"`
	MCPServers        json.RawMessage `json:"mcp_servers,omitempty"`
	Model             string          `json:"model,omitempty"`
	Message           json.RawMessage `json:"message,omitempty"`
	Event             json.RawMessage `json:"event,omitempty"`
	Result            string          `json:"result,omitempty"`
	IsError           bool            `json:"is_error,omitempty"`
	StopReason        string          `json:"stop_reason,omitempty"`
	DurationMS        float64         `json:"duration_ms,omitempty"`
	DurationAPIMS     float64         `json:"duration_api_ms,omitempty"`
	TotalCostUSD      float64         `json:"total_cost_usd,omitempty"`
	Usage             json.RawMessage `json:"usage,omitempty"`
	ModelUsage        json.RawMessage `json:"modelUsage,omitempty"`
	RateLimitInfo     json.RawMessage `json:"rate_limit_info,omitempty"`
	PermissionDenials json.RawMessage `json:"permission_denials,omitempty"`
	Origin            json.RawMessage `json:"origin,omitempty"`
	NumTurns          int             `json:"num_turns,omitempty"`
	TerminalReason    string          `json:"terminal_reason,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

type processState struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu      sync.Mutex
	waitErr error
}

func waitProcess(cmd *exec.Cmd, outputDone <-chan struct{}) *processState {
	state := &processState{
		cmd:  cmd,
		done: make(chan struct{}),
	}
	go func() {
		<-outputDone
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

func (p *processState) isDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *processState) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *processState) kill() {
	if p.cmd.Process == nil || p.isDone() {
		return
	}
	_ = p.cmd.Process.Kill()
}
