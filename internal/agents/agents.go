package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

var ErrUnavailable = errors.New("agents: unavailable")

type Agent interface {
	Type() string
	Run(ctx context.Context, input AgentInput, emit EmitFunc) error
}

type AgentAction string

const (
	AgentActionMessage AgentAction = "message"
	AgentActionClear   AgentAction = "clear"
	AgentActionCompact AgentAction = "compact"
)

type Availability interface {
	Available() error
}

type OptionsProvider interface {
	Options(ctx context.Context) (Options, error)
}

type SkillProvider interface {
	Skills(ctx context.Context, query SkillQuery) (SkillCatalog, error)
}

type SkillQuery struct {
	Workdir     string
	ForceReload bool
}

type SkillCatalog struct {
	Skills   []Skill      `json:"skills"`
	Errors   []SkillError `json:"errors"`
	Revision string       `json:"revision,omitempty"`
}

type Skill struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	DisplayName      string `json:"display_name,omitempty"`
	ShortDescription string `json:"short_description,omitempty"`
	BrandColor       string `json:"brand_color,omitempty"`
	Path             string `json:"path"`
	Scope            string `json:"scope"`
	Enabled          bool   `json:"enabled"`
}

type SkillError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type SkillReference struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Options struct {
	DefaultModel       string                    `json:"default_model"`
	Models             []ModelOption             `json:"models"`
	CollaborationModes []CollaborationModeOption `json:"collaboration_modes"`
}

type ModelOption struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"display_name"`
	Description               string                  `json:"description"`
	Hidden                    bool                    `json:"hidden"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supported_reasoning_efforts"`
	DefaultReasoningEffort    string                  `json:"default_reasoning_effort"`
	ServiceTiers              []ModelServiceTier      `json:"service_tiers"`
	DefaultServiceTier        string                  `json:"default_service_tier"`
	IsDefault                 bool                    `json:"is_default"`
}

type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoning_effort"`
	Description     string `json:"description"`
}

type ModelServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CollaborationModeOption struct {
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type AgentInput struct {
	SessionID         string
	RunID             string
	ProviderSessionID string
	Action            AgentAction
	Message           string
	Workdir           string
	Environment       map[string]string
	Context           string
	Metadata          map[string]any
	Attachments       []Attachment
	Skills            []SkillReference
	UserInput         UserInputBroker
	Permissions       PermissionBroker
	Steering          SteeringBroker
}

// Steering is optional: adapters register an active-turn delivery function only
// when their provider can accept additional input without starting another run.
type SteeringInput struct {
	Message     string
	Attachments []Attachment
	Skills      []SkillReference
}

type SteeringBroker interface {
	RegisterSteering(context.Context, string, string, func(context.Context, SteeringInput) error) (func(), error)
}

// ProviderMessage returns the message sent to an agent provider. Context is
// deliberately kept separate from Message so orchestration can persist and
// display the original user-authored message while giving the provider trusted
// Threave runtime instructions.
func (input AgentInput) ProviderMessage() string {
	context := strings.TrimSpace(input.Context)
	if context == "" {
		return input.Message
	}

	message := "<threave_context>\n" + context + "\n</threave_context>"
	if input.Message != "" {
		message += "\n\n" + input.Message
	}
	return message
}

// ApplyEnvironment adds run-scoped environment values to cmd without changing
// the parent process environment. Callers intentionally use this only for agent
// runs, not availability or options probes.
func ApplyEnvironment(cmd *exec.Cmd, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}

	environment := cmd.Environ()
	positions := make(map[string]int, len(environment))
	for index, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			positions[key] = index
		}
	}

	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := overrides[key]
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("invalid environment variable %q", key)
		}
		entry := key + "=" + value
		if index, ok := positions[key]; ok {
			environment[index] = entry
			continue
		}
		positions[key] = len(environment)
		environment = append(environment, entry)
	}
	cmd.Env = environment
	return nil
}

type Attachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	DataURL   string `json:"data_url"`
	SizeBytes int64  `json:"size_bytes"`
}

type EmitFunc func(ctx context.Context, event AgentEvent) error

type AgentEvent struct {
	Type    string
	Role    string
	Status  string
	Payload any
}

type UserInputBroker interface {
	OpenUserInput(ctx context.Context, request UserInputRequest) (UserInputWaiter, error)
}

// AsyncUserInputBroker registers questions without blocking the agent's event loop.
// Delivery returns only after the provider acknowledges the active-turn input.
type AsyncUserInputBroker interface {
	OpenAsyncUserInput(ctx context.Context, request UserInputRequest, deliver func(context.Context, UserInputResponse) error) (UserInputWaiter, error)
}

type UserInputWaiter interface {
	Wait(ctx context.Context) (UserInputResponse, error)
	Close()
}

type UserInputRequest struct {
	SessionID         string
	RequestID         string
	Provider          string
	ProviderEventType string
	ProviderRequestID string
	ThreadID          string
	TurnID            string
	ItemID            string
	Delivery          string
	Questions         []UserInputQuestion
}

type UserInputQuestion struct {
	ID          string            `json:"id"`
	Header      string            `json:"header"`
	Question    string            `json:"question"`
	IsOther     bool              `json:"is_other"`
	IsSecret    bool              `json:"is_secret"`
	MultiSelect bool              `json:"multi_select,omitempty"`
	Options     []UserInputOption `json:"options"`
}

type UserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type UserInputResponse struct {
	Answers map[string]UserInputQuestionAnswer `json:"answers"`
}

type UserInputQuestionAnswer struct {
	Answers []string `json:"answers"`
}

type PermissionBroker interface {
	OpenPermission(ctx context.Context, request PermissionRequest) (PermissionWaiter, error)
}

type PermissionWaiter interface {
	Wait(ctx context.Context) (PermissionResponse, error)
	Close()
}

type PermissionRequest struct {
	SessionID         string             `json:"-"`
	RequestID         string             `json:"request_id"`
	Provider          string             `json:"provider"`
	ProviderEventType string             `json:"provider_event_type"`
	ProviderRequestID string             `json:"provider_request_id,omitempty"`
	ThreadID          string             `json:"thread_id,omitempty"`
	TurnID            string             `json:"turn_id,omitempty"`
	ItemID            string             `json:"item_id,omitempty"`
	Kind              string             `json:"kind"`
	Title             string             `json:"title"`
	Description       string             `json:"description,omitempty"`
	Reason            string             `json:"reason,omitempty"`
	Command           string             `json:"command,omitempty"`
	CWD               string             `json:"cwd,omitempty"`
	ToolName          string             `json:"tool_name,omitempty"`
	ToolInput         any                `json:"tool_input,omitempty"`
	Paths             []string           `json:"paths,omitempty"`
	Diff              string             `json:"diff,omitempty"`
	RequestedGrants   any                `json:"requested_grants,omitempty"`
	Options           []PermissionOption `json:"options"`
}

type PermissionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Decision    string `json:"decision"`
	Scope       string `json:"scope,omitempty"`
}

type PermissionResponse struct {
	OptionID string `json:"option_id"`
}
