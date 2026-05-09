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
	ErrSessionBusy        = errors.New("session is busy")
	ErrToolBudgetExceeded = errors.New("tool budget exceeded")
	ErrUnsupportedControl = errors.New("unsupported session control")
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
		cfg.ResourceLoader = resources.NewFilesystemLoader()
	}
	if cfg.SessionManager == nil {
		cfg.SessionManager = session.NewInMemoryManager()
	}
	if cfg.ToolRegistry == nil {
		builtins := tools.Builtins()
		cfg.ToolRegistry = tools.NewStaticRegistry(builtins...)
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
	return newStandardSession(r, opts, record), nil
}

func (r *StandardRuntime) OpenSession(ctx context.Context, ref types.SessionRef) (Session, error) {
	if err := r.Validate(ctx); err != nil {
		return nil, err
	}
	record, err := r.cfg.SessionManager.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	return newStandardSession(r, SessionOptions{
		SessionID:   record.SessionID,
		WorkingDir:  record.WorkingDir,
		Model:       record.Model,
		Persistence: record.Persistence,
	}, record), nil
}

type standardSession struct {
	runtime *StandardRuntime
	opts    SessionOptions

	mu         sync.RWMutex
	record     *session.Record
	loaded     resources.Loaded
	loadedOnce bool
	listeners  []events.Listener
	emittedIDs uint64

	cmdCh chan any
}

type promptCommand struct {
	ctx   context.Context
	input types.UserInput
	opts  types.PromptOptions
	reply chan promptResult
}

type promptResult struct {
	result types.RunResult
	err    error
}

type abortCommand struct {
	reply chan error
}

type activePrompt struct {
	cancel context.CancelFunc
	reply  chan promptResult
	done   chan promptResult
}

func newStandardSession(runtime *StandardRuntime, opts SessionOptions, record *session.Record) *standardSession {
	s := &standardSession{
		runtime: runtime,
		opts:    opts,
		record:  record,
		cmdCh:   make(chan any),
	}
	go s.loop()
	return s
}

func (s *standardSession) Prompt(ctx context.Context, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
	reply := make(chan promptResult, 1)
	cmd := promptCommand{ctx: ctx, input: input, opts: opts, reply: reply}
	select {
	case s.cmdCh <- cmd:
	case <-ctx.Done():
		return types.RunResult{}, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.result, result.err
	case <-ctx.Done():
		return types.RunResult{}, ctx.Err()
	}
}

func (s *standardSession) Steer(context.Context, types.UserInput) error {
	return ErrUnsupportedControl
}

func (s *standardSession) FollowUp(context.Context, types.UserInput) error {
	return ErrUnsupportedControl
}

func (s *standardSession) Abort(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case s.cmdCh <- abortCommand{reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *standardSession) Compact(ctx context.Context, opts types.CompactOptions) (types.CompactResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := s.messagesLocked()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return types.SessionSnapshot{
		SessionID: s.record.SessionID,
		Messages:  s.messagesLocked(),
		BranchID:  s.record.BranchID,
	}
}

func (s *standardSession) loop() {
	var active *activePrompt
	for {
		var done <-chan promptResult
		if active != nil {
			done = active.done
		}
		select {
		case cmd := <-s.cmdCh:
			switch req := cmd.(type) {
			case promptCommand:
				if active != nil {
					req.reply <- promptResult{err: ErrSessionBusy}
					continue
				}
				runCtx, cancel := context.WithCancel(req.ctx)
				doneCh := make(chan promptResult, 1)
				active = &activePrompt{
					cancel: cancel,
					reply:  req.reply,
					done:   doneCh,
				}
				go func() {
					result, err := s.runPrompt(runCtx, req.input, req.opts)
					doneCh <- promptResult{result: result, err: err}
				}()
			case abortCommand:
				if active == nil {
					req.reply <- ErrSessionNotRunning
					continue
				}
				active.cancel()
				req.reply <- nil
			}
		case result := <-done:
			active.reply <- result
			active = nil
		}
	}
}

func (s *standardSession) runPrompt(ctx context.Context, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
	loaded, err := s.loadResources(ctx)
	if err != nil {
		s.emitError("resource_load_failed", "", err)
		return types.RunResult{}, err
	}

	providerInstance, err := s.resolveProvider()
	if err != nil {
		s.emitError("provider_resolve_failed", "", err)
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
	if err := s.appendMessage(ctx, userMessage); err != nil {
		s.emitError("session_append_failed", turnID, err)
		return types.RunResult{}, err
	}
	if err := s.emit("message_complete", turnID, userMessage); err != nil {
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

	response, err := s.executeUntilTerminal(ctx, providerInstance, request, turnID, opts.Stream)
	if err != nil {
		s.emitError("turn_failed", turnID, err)
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

func (s *standardSession) loadResources(ctx context.Context) (resources.Loaded, error) {
	s.mu.RLock()
	if s.loadedOnce {
		loaded := s.loaded
		s.mu.RUnlock()
		return loaded, nil
	}
	s.mu.RUnlock()

	loaded, err := s.runtime.cfg.ResourceLoader.Load(ctx, resources.Scope{
		WorkingDir:   s.opts.WorkingDir,
		ContextRoots: s.opts.ContextRoots,
		Mode:         s.opts.DiscoveryMode,
		StopPaths:    s.runtime.cfg.Policy.Resources.StopPaths,
	})
	if err != nil {
		return resources.Loaded{}, err
	}

	s.mu.Lock()
	s.loaded = loaded
	s.loadedOnce = true
	s.mu.Unlock()
	return loaded, nil
}

func (s *standardSession) resolveProvider() (provider.Provider, error) {
	if s.opts.Model.ProviderID != "" {
		return s.runtime.cfg.Providers.Get(s.opts.Model.ProviderID)
	}
	return s.runtime.cfg.Providers.Default()
}

func (s *standardSession) executeUntilTerminal(ctx context.Context, p provider.Provider, request types.ModelRequest, turnID string, stream bool) (types.ModelResponse, error) {
	toolCalls := 0
	for {
		var response types.ModelResponse
		var err error
		if stream && p.SupportsStreaming() {
			response, err = p.StreamCompletion(ctx, request, sinkFunc(func(ev events.RuntimeEvent) error {
				return s.dispatch(turnID, ev)
			}))
		} else {
			response, err = p.Execute(ctx, request)
		}
		if err != nil {
			return types.ModelResponse{}, err
		}

		if response.Assistant != nil {
			response.Assistant.ParentTurnID = turnID
			if err := s.appendMessage(ctx, *response.Assistant); err != nil {
				return types.ModelResponse{}, err
			}
			if err := s.emit("message_complete", turnID, *response.Assistant); err != nil {
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
		stream = false
	}
}

func (s *standardSession) executeTool(ctx context.Context, call types.ToolCall) (types.ToolResult, error) {
	if err := s.emit("tool_execution_start", call.TurnID, call); err != nil {
		return types.ToolResult{}, err
	}
	tool, ok := s.runtime.cfg.ToolRegistry.Lookup(call.Name)
	if !ok {
		result := types.ToolResult{
			CallID: call.ID,
			Status: "error",
			Error: &types.Diagnostic{
				Severity: "error",
				Code:     "tool_unknown",
				Message:  fmt.Sprintf("%v: %s", ErrUnknownTool, call.Name),
			},
		}
		if err := s.emit("tool_execution_end", call.TurnID, result); err != nil {
			return types.ToolResult{}, err
		}
		return result, nil
	}
	result, err := tool.Execute(ctx, call, tools.Env{
		WorkingDir: s.opts.WorkingDir,
		Policy: policy.SessionPolicy{
			Runtime:    s.runtime.cfg.Policy,
			WorkingDir: s.opts.WorkingDir,
		},
		Emit: func(progress types.ToolProgress) error {
			return s.emit("tool_execution_update", call.TurnID, progress)
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
	s.mu.Lock()
	s.record.Entries = append(s.record.Entries, entry)
	s.mu.Unlock()
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
	s.mu.Lock()
	s.record.Entries = append(s.record.Entries, entry)
	s.mu.Unlock()
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
	s.mu.Lock()
	s.record.Entries = append(s.record.Entries, entry)
	s.mu.Unlock()
	return nil
}

func (s *standardSession) messages() []types.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messagesLocked()
}

func (s *standardSession) messagesLocked() []types.Message {
	messages := make([]types.Message, 0, len(s.record.Entries))
	for _, entry := range s.record.Entries {
		if entry.Kind == "message" && entry.Message != nil {
			messages = append(messages, *entry.Message)
		}
	}
	return messages
}

func (s *standardSession) emit(kind, turnID string, payload any) error {
	return s.dispatch(turnID, events.Envelope{
		ID:        s.newID("evt"),
		Type:      kind,
		SessionID: s.record.SessionID,
		TurnID:    turnID,
		At:        time.Now().UTC(),
		Payload:   payload,
	})
}

func (s *standardSession) emitError(code, turnID string, err error) {
	_ = s.emit("error", turnID, types.RuntimeError{
		Code:    code,
		Message: err.Error(),
	})
}

func (s *standardSession) dispatch(turnID string, ev events.RuntimeEvent) error {
	envelope, ok := ev.(events.Envelope)
	if ok {
		if envelope.SessionID == "" {
			envelope.SessionID = s.record.SessionID
		}
		if envelope.TurnID == "" {
			envelope.TurnID = turnID
		}
		if envelope.ID == "" {
			envelope.ID = s.newID("evt")
		}
		if envelope.At.IsZero() {
			envelope.At = time.Now().UTC()
		}
		ev = envelope
	}
	if s.runtime.cfg.Observer != nil {
		s.runtime.cfg.Observer(ev)
	}
	s.mu.RLock()
	listeners := append([]events.Listener(nil), s.listeners...)
	s.mu.RUnlock()
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
