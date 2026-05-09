package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/provider"
	"github.com/latentarts/memoidness/resources"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/tools"
	"github.com/latentarts/memoidness/types"
)

var (
	ErrInvalidConfig      = errors.New("invalid runtime config")
	ErrUnknownTool        = errors.New("unknown tool")
	ErrSessionNotRunning  = errors.New("session is not running")
	ErrToolBudgetExceeded = errors.New("tool budget exceeded")
)

type Config struct {
	Providers      provider.Registry
	ResourceLoader resources.Loader
	SessionManager session.Manager
	ToolRegistry   tools.Registry
	Policy         policy.RuntimePolicy
	Observer       events.Listener
}

type Runtime interface {
	Validate(ctx context.Context) error
	NewSession(ctx context.Context, opts SessionOptions) (Session, error)
	OpenSession(ctx context.Context, ref types.SessionRef) (Session, error)
}

type SessionOptions struct {
	SessionID     string
	WorkingDir    string
	Model         types.ModelRef
	Instructions  []types.InstructionSource
	ContextRoots  []string
	DiscoveryMode types.DiscoveryMode
	Limits        types.SessionLimits
	Persistence   types.PersistenceMode
}

type Session interface {
	Prompt(ctx context.Context, input types.UserInput, opts types.PromptOptions) (types.RunResult, error)
	Steer(ctx context.Context, input types.UserInput) error
	FollowUp(ctx context.Context, input types.UserInput) error
	Abort(ctx context.Context) error
	Compact(ctx context.Context, opts types.CompactOptions) (types.CompactResult, error)
	Subscribe(listener events.Listener) func()
	Snapshot() types.SessionSnapshot
}

type StandardRuntime struct {
	cfg Config
}

func New(cfg Config) *StandardRuntime {
	if cfg.ResourceLoader == nil {
		cfg.ResourceLoader = resources.NoopLoader{}
	}
	if cfg.SessionManager == nil {
		cfg.SessionManager = session.NewInMemoryManager()
	}
	if cfg.ToolRegistry == nil {
		cfg.ToolRegistry = tools.NewStaticRegistry()
	}
	return &StandardRuntime{cfg: cfg}
}

func (r *StandardRuntime) Validate(context.Context) error {
	if r.cfg.Providers == nil {
		return fmt.Errorf("%w: providers are required", ErrInvalidConfig)
	}
	if r.cfg.ResourceLoader == nil {
		return fmt.Errorf("%w: resource loader is required", ErrInvalidConfig)
	}
	if r.cfg.SessionManager == nil {
		return fmt.Errorf("%w: session manager is required", ErrInvalidConfig)
	}
	if r.cfg.ToolRegistry == nil {
		return fmt.Errorf("%w: tool registry is required", ErrInvalidConfig)
	}
	return nil
}

func (r *StandardRuntime) NewSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := r.Validate(ctx); err != nil {
		return nil, err
	}

	record, err := r.cfg.SessionManager.New(ctx, session.RecordOptions{
		SessionID:   opts.SessionID,
		WorkingDir:  opts.WorkingDir,
		Model:       opts.Model,
		Persistence: opts.Persistence,
	})
	if err != nil {
		return nil, err
	}

	return &standardSession{
		runtime: r,
		opts:    opts,
		record:  record,
	}, nil
}

func (r *StandardRuntime) OpenSession(ctx context.Context, ref types.SessionRef) (Session, error) {
	if err := r.Validate(ctx); err != nil {
		return nil, err
	}

	record, err := r.cfg.SessionManager.Open(ctx, ref)
	if err != nil {
		return nil, err
	}

	return &standardSession{
		runtime: r,
		opts: SessionOptions{
			SessionID:   record.SessionID,
			WorkingDir:  record.WorkingDir,
			Model:       record.Model,
			Persistence: record.Persistence,
		},
		record: record,
	}, nil
}

type standardSession struct {
	runtime *StandardRuntime
	opts    SessionOptions

	mu         sync.Mutex
	record     *session.Record
	loaded     resources.Loaded
	listeners  []events.Listener
	runCancel  context.CancelFunc
	emittedIDs uint64
}

func (s *standardSession) Prompt(ctx context.Context, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
	s.mu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	s.runCancel = cancel
	s.mu.Unlock()
	defer s.clearRun()

	loaded, err := s.runtime.cfg.ResourceLoader.Load(runCtx, resources.Scope{
		WorkingDir:   s.opts.WorkingDir,
		ContextRoots: s.opts.ContextRoots,
		Mode:         s.opts.DiscoveryMode,
		StopPaths:    s.runtime.cfg.Policy.Resources.StopPaths,
	})
	if err != nil {
		return types.RunResult{}, err
	}
	s.loaded = loaded

	providerInstance, err := s.resolveProvider()
	if err != nil {
		return types.RunResult{}, err
	}

	turnID := s.newID("turn")
	if err := s.emit("agent_start", turnID, map[string]any{"model": s.opts.Model.ID}); err != nil {
		return types.RunResult{}, err
	}
	if err := s.emit("turn_start", turnID, nil); err != nil {
		return types.RunResult{}, err
	}

	userMessage := types.Message{
		ID:           s.newID("msg"),
		Role:         "user",
		ParentTurnID: turnID,
		Parts:        buildUserParts(input),
	}
	if err := s.appendMessage(runCtx, userMessage); err != nil {
		return types.RunResult{}, err
	}

	request := types.ModelRequest{
		Model:        s.opts.Model,
		Instructions: append(append([]types.InstructionSource{}, loaded.Instructions...), s.opts.Instructions...),
		Messages:     s.messages(),
		VisibleTools: s.runtime.cfg.ToolRegistry.Definitions(),
		Limits:       s.opts.Limits,
		Streaming:    opts.Stream,
	}

	if opts.Stream && providerInstance.SupportsStreaming() {
		if err := providerInstance.StreamCompletion(runCtx, request, sinkFunc(func(ev events.RuntimeEvent) error {
			return s.dispatch(ev)
		})); err != nil {
			return types.RunResult{}, err
		}
	}

	response, err := s.executeUntilTerminal(runCtx, providerInstance, request, turnID)
	if err != nil {
		return types.RunResult{}, err
	}

	if err := s.emit("turn_end", turnID, map[string]any{"stop_reason": response.StopReason}); err != nil {
		return types.RunResult{}, err
	}
	if err := s.emit("agent_end", turnID, nil); err != nil {
		return types.RunResult{}, err
	}

	var finalOutput types.Message
	if response.Assistant != nil {
		finalOutput = *response.Assistant
	}

	return types.RunResult{
		FinalOutput: finalOutput,
		Snapshot:    s.Snapshot(),
		Usage:       response.Usage,
		ToolCalls:   response.ToolCalls,
		Diagnostics: loaded.Diagnostics,
	}, nil
}

func (s *standardSession) Steer(context.Context, types.UserInput) error {
	return ErrSessionNotRunning
}

func (s *standardSession) FollowUp(context.Context, types.UserInput) error {
	return ErrSessionNotRunning
}

func (s *standardSession) Abort(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel == nil {
		return ErrSessionNotRunning
	}
	s.runCancel()
	return nil
}

func (s *standardSession) Compact(ctx context.Context, opts types.CompactOptions) (types.CompactResult, error) {
	messages := s.messages()
	keep := opts.KeepRecentMessages
	if keep <= 0 || keep > len(messages) {
		keep = len(messages)
	}

	compacted := len(messages) - keep
	if compacted < 0 {
		compacted = 0
	}

	result := types.CompactResult{
		Summary: types.Message{
			ID:   s.newID("summary"),
			Role: "system",
			Parts: []types.MessagePart{{
				Kind: "summary",
				Text: fmt.Sprintf("Compacted %d earlier messages", compacted),
			}},
		},
	}

	entry := types.SessionEntry{
		ID:      s.newID("entry"),
		Kind:    "summary",
		Summary: &result.Summary,
		At:      time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		return types.CompactResult{}, err
	}
	s.record.Entries = append(s.record.Entries, entry)
	return result, nil
}

func (s *standardSession) Subscribe(listener events.Listener) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listeners = append(s.listeners, listener)
	idx := len(s.listeners) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if idx < len(s.listeners) {
			s.listeners[idx] = nil
		}
	}
}

func (s *standardSession) Snapshot() types.SessionSnapshot {
	return types.SessionSnapshot{
		SessionID: s.record.SessionID,
		Messages:  s.messages(),
		BranchID:  s.record.BranchID,
	}
}

func (s *standardSession) clearRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runCancel = nil
}

func (s *standardSession) resolveProvider() (provider.Provider, error) {
	if s.opts.Model.ProviderID != "" {
		return s.runtime.cfg.Providers.Get(s.opts.Model.ProviderID)
	}
	return s.runtime.cfg.Providers.Default()
}

func (s *standardSession) executeUntilTerminal(ctx context.Context, p provider.Provider, request types.ModelRequest, turnID string) (types.ModelResponse, error) {
	toolCalls := 0
	for {
		response, err := p.Execute(ctx, request)
		if err != nil {
			return types.ModelResponse{}, err
		}

		if response.Assistant != nil {
			response.Assistant.ParentTurnID = turnID
			if err := s.appendMessage(ctx, *response.Assistant); err != nil {
				return types.ModelResponse{}, err
			}
		}

		if len(response.ToolCalls) == 0 {
			return response, nil
		}

		for _, call := range response.ToolCalls {
			call.TurnID = ensureTurnID(call.TurnID, turnID)
			toolCalls++
			if request.Limits.MaxToolCalls > 0 && toolCalls > request.Limits.MaxToolCalls {
				return types.ModelResponse{}, ErrToolBudgetExceeded
			}

			if err := s.appendToolCall(ctx, call); err != nil {
				return types.ModelResponse{}, err
			}
			result, err := s.executeTool(ctx, call)
			if err != nil {
				return types.ModelResponse{}, err
			}
			if err := s.appendToolResult(ctx, result); err != nil {
				return types.ModelResponse{}, err
			}
		}

		request.Messages = s.messages()
	}
}

func (s *standardSession) executeTool(ctx context.Context, call types.ToolCall) (types.ToolResult, error) {
	if err := s.emit("tool_execution_start", call.TurnID, call); err != nil {
		return types.ToolResult{}, err
	}

	tool, ok := s.runtime.cfg.ToolRegistry.Lookup(call.Name)
	if !ok {
		return types.ToolResult{}, fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)
	}

	result, err := tool.Execute(ctx, call, tools.Env{
		WorkingDir: s.opts.WorkingDir,
		Policy: policy.SessionPolicy{
			Runtime:    s.runtime.cfg.Policy,
			WorkingDir: s.opts.WorkingDir,
		},
	})
	if err != nil {
		return types.ToolResult{}, err
	}

	if err := s.emit("tool_execution_end", call.TurnID, result); err != nil {
		return types.ToolResult{}, err
	}
	return result, nil
}

func (s *standardSession) appendMessage(ctx context.Context, message types.Message) error {
	entry := types.SessionEntry{
		ID:      s.newID("entry"),
		Kind:    "message",
		Message: &message,
		At:      time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		return err
	}
	s.record.Entries = append(s.record.Entries, entry)
	return nil
}

func (s *standardSession) appendToolCall(ctx context.Context, call types.ToolCall) error {
	entry := types.SessionEntry{
		ID:       s.newID("entry"),
		Kind:     "tool_call",
		ToolCall: &call,
		At:       time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		return err
	}
	s.record.Entries = append(s.record.Entries, entry)
	return nil
}

func (s *standardSession) appendToolResult(ctx context.Context, result types.ToolResult) error {
	toolMessage := types.Message{
		ID:   s.newID("msg"),
		Role: "tool",
		Parts: []types.MessagePart{{
			Kind:   "tool_result",
			Result: &result,
		}},
	}
	if err := s.appendMessage(ctx, toolMessage); err != nil {
		return err
	}

	entry := types.SessionEntry{
		ID:         s.newID("entry"),
		Kind:       "tool_result",
		ToolResult: &result,
		At:         time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		return err
	}
	s.record.Entries = append(s.record.Entries, entry)
	return nil
}

func (s *standardSession) messages() []types.Message {
	messages := make([]types.Message, 0, len(s.record.Entries))
	for _, entry := range s.record.Entries {
		if entry.Kind == "message" && entry.Message != nil {
			messages = append(messages, *entry.Message)
		}
	}
	return messages
}

func (s *standardSession) emit(kind, turnID string, payload any) error {
	return s.dispatch(events.Envelope{
		ID:        s.newID("evt"),
		Type:      kind,
		SessionID: s.record.SessionID,
		TurnID:    turnID,
		At:        time.Now().UTC(),
		Payload:   payload,
	})
}

func (s *standardSession) dispatch(ev events.RuntimeEvent) error {
	if s.runtime.cfg.Observer != nil {
		s.runtime.cfg.Observer(ev)
	}

	s.mu.Lock()
	listeners := append([]events.Listener(nil), s.listeners...)
	s.mu.Unlock()

	for _, listener := range listeners {
		if listener != nil {
			listener(ev)
		}
	}
	return nil
}

func (s *standardSession) newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddUint64(&s.emittedIDs, 1))
}

type sinkFunc func(events.RuntimeEvent) error

func (f sinkFunc) Emit(ev events.RuntimeEvent) error {
	return f(ev)
}

func buildUserParts(input types.UserInput) []types.MessagePart {
	if len(input.Parts) > 0 {
		return append([]types.MessagePart(nil), input.Parts...)
	}
	return []types.MessagePart{{
		Kind: "text",
		Text: input.Text,
	}}
}

func ensureTurnID(turnID, fallback string) string {
	if turnID != "" {
		return turnID
	}
	return fallback
}

func JSONPayload(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
