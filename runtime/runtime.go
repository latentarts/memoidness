package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/latentarts/memoidness/capabilities"
	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/mcp"
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
	ErrCapabilityDenied   = errors.New("capability denied")
	ErrSkillNotFound      = errors.New("skill not found")
)

type Config struct {
	Providers      provider.Registry
	Capabilities   capabilities.Registry
	MCPRegistry    mcp.Registry
	ResourceLoader resources.Loader
	SkillPromoter  resources.Promoter
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
	Scope         types.SessionScope
	Mode          types.ModeRef
	Model         types.ModelRef
	ToolAllowlist []string
	Instructions  []types.InstructionSource
	DiscoveryMode types.DiscoveryMode
	Limits        types.SessionLimits
	Persistence   types.PersistenceMode
}

type Session interface {
	Prompt(ctx context.Context, input types.UserInput, opts types.PromptOptions) (types.RunResult, error)
	Steer(ctx context.Context, input types.UserInput) error
	FollowUp(ctx context.Context, input types.UserInput) error
	Abort(ctx context.Context) error
	Fork(ctx context.Context, at types.EntryRef) (Session, error)
	Clone(ctx context.Context) (Session, error)
	Navigate(ctx context.Context, target types.EntryRef) error
	SetMode(ctx context.Context, mode types.ModeRef) error
	SpawnSubagent(ctx context.Context, req types.SubagentRequest) (types.SubagentResult, error)
	PromoteSkill(ctx context.Context, req types.SkillPromotionRequest) (types.SkillPromotionResult, error)
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
	if cfg.Capabilities == nil {
		plugins := append(capabilities.DefaultPlugins(), capabilities.DefaultResourcePlugins()...)
		cfg.Capabilities = capabilities.NewStaticRegistry(types.ModeRef{ID: "implementation"}, plugins...)
	}
	if cfg.SessionManager == nil {
		cfg.SessionManager = session.NewInMemoryManager()
	}
	if cfg.SkillPromoter == nil {
		cfg.SkillPromoter = resources.NewWorkspacePromoter()
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
	if r.cfg.Capabilities == nil {
		return fmt.Errorf("%w: capabilities registry is required", ErrInvalidConfig)
	}
	if r.cfg.SessionManager == nil {
		return fmt.Errorf("%w: session manager is required", ErrInvalidConfig)
	}
	if r.cfg.SkillPromoter == nil {
		return fmt.Errorf("%w: skill promoter is required", ErrInvalidConfig)
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
	resolved, err := r.cfg.Capabilities.Resolve(ctx, opts.Scope, opts.Mode)
	if err != nil {
		return nil, err
	}
	if opts.Mode.ID == "" {
		opts.Mode = resolved.Mode
	}
	record, err := r.cfg.SessionManager.New(ctx, session.RecordOptions{
		SessionID:   opts.SessionID,
		Scope:       opts.Scope,
		Mode:        opts.Mode,
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
		Scope:       record.Scope,
		Mode:        record.Mode,
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
	active     bool

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

type queuedInputCommand struct {
	input    types.UserInput
	kind     string
	accepted chan error
}

type abortCommand struct {
	reply chan error
}

type activePrompt struct {
	cancel context.CancelFunc
	reply  chan promptResult
	done   chan promptResult
	mu     sync.Mutex
	queue  []queuedInput
}

type queuedInput struct {
	input types.UserInput
	kind  string
}

type runResolution struct {
	capabilities capabilities.Resolved
	mcp          mcp.Resolved
	tools        tools.Registry
	allowedTools map[string]struct{}
}

func (q queuedInput) role() string {
	switch q.kind {
	case "steer":
		return "developer"
	default:
		return "user"
	}
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

func (s *standardSession) Steer(ctx context.Context, input types.UserInput) error {
	return s.enqueueActiveInput(ctx, input, "steer")
}

func (s *standardSession) FollowUp(ctx context.Context, input types.UserInput) error {
	return s.enqueueActiveInput(ctx, input, "follow_up")
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

func (s *standardSession) Fork(ctx context.Context, at types.EntryRef) (Session, error) {
	if err := s.ensureIdle(); err != nil {
		return nil, err
	}
	resolvedAt, err := s.resolveEntryTarget(at)
	if err != nil {
		return nil, err
	}
	record, err := s.runtime.cfg.SessionManager.Fork(ctx, s.record.Ref(), resolvedAt)
	if err != nil {
		return nil, err
	}
	if err := s.emit("session_fork", "", map[string]any{
		"child_session": record.Ref(),
		"branch_id":     record.BranchID,
		"at":            resolvedAt.ID,
	}); err != nil {
		return nil, err
	}
	return newStandardSession(s.runtime, s.sessionOptionsFor(record), record), nil
}

func (s *standardSession) Clone(ctx context.Context) (Session, error) {
	if err := s.ensureIdle(); err != nil {
		return nil, err
	}
	record, err := s.runtime.cfg.SessionManager.Clone(ctx, s.record.Ref())
	if err != nil {
		return nil, err
	}
	if err := s.emit("session_clone", "", map[string]any{
		"child_session": record.Ref(),
		"branch_id":     record.BranchID,
	}); err != nil {
		return nil, err
	}
	return newStandardSession(s.runtime, s.sessionOptionsFor(record), record), nil
}

func (s *standardSession) Navigate(ctx context.Context, target types.EntryRef) error {
	if err := s.ensureIdle(); err != nil {
		return err
	}
	resolvedTarget, err := s.resolveEntryTarget(target)
	if err != nil {
		return err
	}
	record, err := s.runtime.cfg.SessionManager.Navigate(ctx, s.record.Ref(), resolvedTarget)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.record = record
	s.opts = s.sessionOptionsFor(record)
	s.loaded = resources.Loaded{}
	s.loadedOnce = false
	s.mu.Unlock()
	return s.emit("session_navigate", "", map[string]any{
		"session":   record.Ref(),
		"branch_id": record.BranchID,
		"target":    resolvedTarget.ID,
	})
}

func (s *standardSession) enqueueActiveInput(ctx context.Context, input types.UserInput, kind string) error {
	accepted := make(chan error, 1)
	select {
	case s.cmdCh <- queuedInputCommand{input: input, kind: kind, accepted: accepted}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-accepted:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *standardSession) SetMode(ctx context.Context, mode types.ModeRef) error {
	if mode.ID == "" {
		return ErrUnsupportedControl
	}
	resolved, err := s.runtime.cfg.Capabilities.Resolve(ctx, s.record.Scope, mode)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.opts.Mode = resolved.Mode
	s.record.Mode = resolved.Mode
	entry := types.SessionEntry{
		ID:   s.newID("entry"),
		Kind: "mode_transition",
		Mode: &resolved.Mode,
		At:   time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		s.mu.Unlock()
		return err
	}
	s.record.Entries = append(s.record.Entries, entry)
	s.mu.Unlock()
	return s.emit("mode_change_end", "", resolved.Mode)
}

func (s *standardSession) SpawnSubagent(ctx context.Context, req types.SubagentRequest) (types.SubagentResult, error) {
	parentResolved, err := s.runtime.resolveRun(ctx, s.record.Scope, s.record.Mode, s.opts.ToolAllowlist)
	if err != nil {
		return types.SubagentResult{}, err
	}
	if !parentResolved.capabilities.AllowSubagents {
		_ = s.emit("capability_denial", "", map[string]any{
			"kind": "subagent",
			"mode": s.record.Mode.ID,
		})
		return types.SubagentResult{}, ErrCapabilityDenied
	}
	scope := s.record.Scope
	if req.Scope != nil {
		if err := validateChildScope(scope, *req.Scope); err != nil {
			_ = s.emit("capability_denial", "", map[string]any{
				"kind":  "subagent_scope",
				"error": err.Error(),
			})
			return types.SubagentResult{}, err
		}
		scope = *req.Scope
	}
	mode := s.record.Mode
	if req.Mode != nil && req.Mode.ID != "" {
		mode = *req.Mode
	}
	model := s.record.Model
	if req.Model != nil && req.Model.ID != "" {
		model = *req.Model
	}
	child, err := s.runtime.NewSession(ctx, SessionOptions{
		SessionID:     req.SessionID,
		Scope:         scope,
		Mode:          mode,
		Model:         model,
		ToolAllowlist: append([]string(nil), req.ToolAllowlist...),
		DiscoveryMode: s.opts.DiscoveryMode,
		Limits:        s.opts.Limits,
		Persistence:   s.opts.Persistence,
	})
	if err != nil {
		return types.SubagentResult{}, err
	}
	childSnapshot := child.Snapshot()
	childRef := types.SessionRef{
		ID:        childSnapshot.SessionID,
		Principal: childSnapshot.Scope.Principal.ID,
		Workspace: childSnapshot.Scope.Workspace.Ref.ID,
	}
	linkEntry := types.SessionEntry{
		ID:            s.newID("entry"),
		Kind:          "subagent_link",
		Subagent:      &childRef,
		ToolAllowlist: append([]string(nil), req.ToolAllowlist...),
		At:            time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), linkEntry); err != nil {
		return types.SubagentResult{}, err
	}
	s.mu.Lock()
	s.record.Entries = append(s.record.Entries, linkEntry)
	s.mu.Unlock()
	if err := s.emit("subagent_start", "", map[string]any{
		"child_session": childRef,
		"mode":          mode.ID,
		"tool_allowlist": req.ToolAllowlist,
	}); err != nil {
		return types.SubagentResult{}, err
	}
	run, err := child.Prompt(ctx, req.Input, req.Options)
	if err != nil {
		return types.SubagentResult{}, err
	}
	parentRef := s.record.Ref()
	if stdChild, ok := child.(*standardSession); ok {
		parentEntry := types.SessionEntry{
			ID:            stdChild.newID("entry"),
			Kind:          "subagent_parent",
			ParentSession: &parentRef,
			ToolAllowlist: append([]string(nil), req.ToolAllowlist...),
			At:            time.Now().UTC(),
		}
		if appendErr := stdChild.runtime.cfg.SessionManager.Append(ctx, stdChild.record.Ref(), parentEntry); appendErr == nil {
			stdChild.mu.Lock()
			stdChild.record.Entries = append(stdChild.record.Entries, parentEntry)
			stdChild.mu.Unlock()
		}
	}
	if err := s.emit("subagent_end", "", map[string]any{
		"child_session": childRef,
		"status":        "ok",
	}); err != nil {
		return types.SubagentResult{}, err
	}
	return types.SubagentResult{
		SessionRef: childRef,
		Run:        run,
	}, nil
}

func (s *standardSession) PromoteSkill(ctx context.Context, req types.SkillPromotionRequest) (types.SkillPromotionResult, error) {
	target := req.Target
	if target == "" {
		target = types.SkillPromotionTargetWorkspace
	}
	s.mu.RLock()
	index := -1
	var skill resources.SkillResource
	for i, loadedSkill := range s.loaded.Skills {
		if loadedSkill.Name == req.Name && loadedSkill.Ephemeral {
			index = i
			skill = loadedSkill
			break
		}
	}
	s.mu.RUnlock()
	if index < 0 {
		return types.SkillPromotionResult{}, fmt.Errorf("%w: %s", ErrSkillNotFound, req.Name)
	}
	promoted, err := s.runtime.cfg.SkillPromoter.Promote(ctx, s.record.Scope, skill, target)
	if err != nil {
		return types.SkillPromotionResult{}, err
	}
	s.mu.Lock()
	s.loaded.Skills[index].Ephemeral = false
	s.loaded.Skills[index].Source = promoted.Source
	s.loaded.Skills[index].Path = promoted.Path
	entry := types.SessionEntry{
		ID:             s.newID("entry"),
		Kind:           "skill_promotion",
		SkillPromotion: &promoted,
		At:             time.Now().UTC(),
	}
	if err := s.runtime.cfg.SessionManager.Append(ctx, s.record.Ref(), entry); err != nil {
		s.mu.Unlock()
		return types.SkillPromotionResult{}, err
	}
	s.record.Entries = append(s.record.Entries, entry)
	s.mu.Unlock()
	if err := s.emit("skill_promotion", "", promoted); err != nil {
		return types.SkillPromotionResult{}, err
	}
	return promoted, nil
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
		Scope:     s.record.Scope,
		Mode:      s.record.Mode,
		Messages:  s.messagesLocked(),
		BranchID:  s.record.BranchID,
	}
}

func (s *standardSession) ensureIdle() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active {
		return ErrSessionBusy
	}
	return nil
}

func (s *standardSession) sessionOptionsFor(record *session.Record) SessionOptions {
	return SessionOptions{
		SessionID:     record.SessionID,
		Scope:         record.Scope,
		Mode:          record.Mode,
		Model:         record.Model,
		ToolAllowlist: append([]string(nil), s.opts.ToolAllowlist...),
		Instructions:  append([]types.InstructionSource(nil), s.opts.Instructions...),
		DiscoveryMode: s.opts.DiscoveryMode,
		Limits:        s.opts.Limits,
		Persistence:   record.Persistence,
	}
}

func (s *standardSession) resolveEntryTarget(ref types.EntryRef) (types.EntryRef, error) {
	if ref.ID == "" {
		return ref, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.record.Entries {
		if entry.ID == ref.ID {
			return ref, nil
		}
		if entry.Message != nil && entry.Message.ID == ref.ID {
			return types.EntryRef{ID: entry.ID}, nil
		}
		if entry.ToolCall != nil && entry.ToolCall.ID == ref.ID {
			return types.EntryRef{ID: entry.ID}, nil
		}
		if entry.ToolResult != nil && entry.ToolResult.CallID == ref.ID {
			return types.EntryRef{ID: entry.ID}, nil
		}
	}
	return types.EntryRef{}, fmt.Errorf("%w: entry %s", session.ErrSessionNotFound, ref.ID)
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
				s.mu.Lock()
				s.active = true
				s.mu.Unlock()
				go func(activeRef *activePrompt) {
					result, err := s.runPrompt(runCtx, req.input, req.opts, activeRef)
					doneCh <- promptResult{result: result, err: err}
				}(active)
			case abortCommand:
				if active == nil {
					req.reply <- ErrSessionNotRunning
					continue
				}
				active.cancel()
				req.reply <- nil
			case queuedInputCommand:
				if active == nil {
					req.accepted <- ErrSessionNotRunning
					continue
				}
				active.mu.Lock()
				active.queue = append(active.queue, queuedInput{input: req.input, kind: req.kind})
				pending := len(active.queue)
				active.mu.Unlock()
				if err := s.emit("queue_update", "", map[string]any{
					"action":  "enqueued",
					"kind":    req.kind,
					"pending": pending,
				}); err != nil {
					req.accepted <- err
					continue
				}
				req.accepted <- nil
			}
		case result := <-done:
			active.reply <- result
			s.mu.Lock()
			s.active = false
			s.mu.Unlock()
			active = nil
		}
	}
}

func (s *standardSession) runPrompt(ctx context.Context, input types.UserInput, opts types.PromptOptions, active *activePrompt) (types.RunResult, error) {
	resolved, err := s.runtime.resolveRun(ctx, s.record.Scope, s.record.Mode, s.opts.ToolAllowlist)
	if err != nil {
		s.emitError("capability_resolve_failed", "", err)
		return types.RunResult{}, err
	}
	if err := s.emit("capability_resolution", "", resolved.capabilities); err != nil {
		return types.RunResult{}, err
	}
	if len(resolved.mcp.Servers) > 0 {
		if err := s.appendMCPResolution(ctx, resolved.mcp.Servers); err != nil {
			return types.RunResult{}, err
		}
		if err := s.emit("mcp_server_resolution", "", resolved.mcp.Servers); err != nil {
			return types.RunResult{}, err
		}
	}
	loaded, err := s.loadResources(ctx, input, resolved)
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
		VisibleTools: resolved.tools.DefinitionsFor(resolved.allowedTools),
		Limits:       s.opts.Limits,
		Streaming:    opts.Stream,
	}

	response, err := s.executeUntilTerminal(ctx, providerInstance, request, turnID, opts.Stream, resolved, active.dequeueAll)
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

func (s *standardSession) loadResources(ctx context.Context, input types.UserInput, resolved runResolution) (resources.Loaded, error) {
	s.mu.RLock()
	if s.loadedOnce {
		loaded := s.loaded
		s.mu.RUnlock()
		return loaded, nil
	}
	s.mu.RUnlock()

	loader := resources.NewCompositeLoader(s.runtime.cfg.ResourceLoader, append(resolved.capabilities.Resources, resourceProviders(resolved.mcp.Providers)...)...)
	loaded, err := loader.Load(ctx, resources.Scope{
		Scope:        s.opts.Scope,
		ContextRoots: s.opts.Scope.Workspace.ContextRoots,
		Mode:         s.opts.DiscoveryMode,
		StopPaths:    s.runtime.cfg.Policy.Resources.StopPaths,
		TaskHint:     input.Text,
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

func (r *StandardRuntime) resolveRun(ctx context.Context, scope types.SessionScope, mode types.ModeRef, toolAllowlist []string) (runResolution, error) {
	resolvedCapabilities, err := r.cfg.Capabilities.Resolve(ctx, scope, mode)
	if err != nil {
		return runResolution{}, err
	}
	resolvedMCP := mcp.Resolved{}
	if r.cfg.MCPRegistry != nil {
		resolvedMCP, err = r.cfg.MCPRegistry.Resolve(ctx, scope, resolvedCapabilities.Mode)
		if err != nil {
			return runResolution{}, err
		}
	}
	allowedTools := copyAllowedTools(resolvedCapabilities.AllowedTools)
	for _, tool := range resolvedMCP.Tools {
		allowedTools[tool.Definition().Name] = struct{}{}
	}
	if len(toolAllowlist) > 0 {
		allowedTools = intersectAllowedTools(allowedTools, toolAllowlist)
	}
	return runResolution{
		capabilities: resolvedCapabilities,
		mcp:          resolvedMCP,
		tools:        tools.NewCompositeRegistry(r.cfg.ToolRegistry, resolvedMCP.Tools...),
		allowedTools: allowedTools,
	}, nil
}

func (s *standardSession) resolveProvider() (provider.Provider, error) {
	if s.opts.Model.ProviderID != "" {
		return s.runtime.cfg.Providers.Get(s.opts.Model.ProviderID)
	}
	return s.runtime.cfg.Providers.Default()
}

func (s *standardSession) executeUntilTerminal(ctx context.Context, p provider.Provider, request types.ModelRequest, turnID string, stream bool, resolved runResolution, queued func() []queuedInput) (types.ModelResponse, error) {
	toolCalls := 0
	for _, server := range resolved.mcp.Servers {
		if err := s.emit("mcp_server_session_start", turnID, server); err != nil {
			return types.ModelResponse{}, err
		}
	}
	defer func() {
		for _, server := range resolved.mcp.Servers {
			_ = s.emit("mcp_server_session_end", turnID, server)
		}
	}()
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
			pending := queued()
			if len(pending) > 0 {
				if err := s.appendQueuedInputs(ctx, turnID, pending, &request); err != nil {
					return types.ModelResponse{}, err
				}
				stream = false
				continue
			}
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
			result, err := s.executeTool(ctx, call, resolved)
			if err != nil {
				return types.ModelResponse{}, err
			}
			if err := s.appendToolResult(ctx, result); err != nil {
				return types.ModelResponse{}, err
			}
		}
		pending := queued()
		if len(pending) > 0 {
			if err := s.appendQueuedInputs(ctx, turnID, pending, &request); err != nil {
				return types.ModelResponse{}, err
			}
		}
		request.Messages = s.messages()
		stream = false
	}
}

func (s *standardSession) executeTool(ctx context.Context, call types.ToolCall, resolved runResolution) (types.ToolResult, error) {
	if err := s.emit("tool_execution_start", call.TurnID, call); err != nil {
		return types.ToolResult{}, err
	}
	if _, ok := resolved.allowedTools[call.Name]; !ok {
		result := types.ToolResult{
			CallID: call.ID,
			Status: "error",
			Error: &types.Diagnostic{
				Severity: "error",
				Code:     "capability_denied",
				Message:  fmt.Sprintf("%v: %s in mode %s", ErrCapabilityDenied, call.Name, resolved.capabilities.Mode.ID),
			},
		}
		if err := s.emit("capability_denial", call.TurnID, map[string]any{
			"tool": call.Name,
			"mode": resolved.capabilities.Mode.ID,
		}); err != nil {
			return types.ToolResult{}, err
		}
		if err := s.emit("tool_execution_end", call.TurnID, result); err != nil {
			return types.ToolResult{}, err
		}
		return result, nil
	}
	tool, ok := resolved.tools.Lookup(call.Name)
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
		WorkingDir: s.opts.Scope.Workspace.WorkingDir,
		Policy: policy.SessionPolicy{
			Runtime:    s.runtime.cfg.Policy,
			WorkingDir: s.opts.Scope.Workspace.WorkingDir,
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

func (s *standardSession) appendMCPResolution(ctx context.Context, servers []types.MCPServerDescriptor) error {
	entry := types.SessionEntry{
		ID:         s.newID("entry"),
		Kind:       "mcp_server_resolution",
		MCPServers: append([]types.MCPServerDescriptor(nil), servers...),
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

func (s *standardSession) appendQueuedInputs(ctx context.Context, turnID string, pending []queuedInput, request *types.ModelRequest) error {
	if err := s.emit("queue_update", turnID, map[string]any{
		"action":  "drained",
		"count":   len(pending),
		"pending": 0,
		"kinds":   queuedKinds(pending),
	}); err != nil {
		return err
	}
	for _, item := range pending {
		message := types.Message{
			ID:           s.newID("msg"),
			Role:         item.role(),
			ParentTurnID: turnID,
			Parts:        buildUserParts(item.input),
			ProviderMeta: map[string]any{"queue_kind": item.kind},
		}
		if err := s.appendMessage(ctx, message); err != nil {
			return err
		}
		if err := s.emit("message_complete", turnID, message); err != nil {
			return err
		}
	}
	request.Messages = s.messages()
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
		Principal: s.record.Scope.Principal.ID,
		Workspace: s.record.Scope.Workspace.Ref.ID,
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
		if envelope.Principal == "" {
			envelope.Principal = s.record.Scope.Principal.ID
		}
		if envelope.Workspace == "" {
			envelope.Workspace = s.record.Scope.Workspace.Ref.ID
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

func (a *activePrompt) dequeueAll() []queuedInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) == 0 {
		return nil
	}
	items := append([]queuedInput(nil), a.queue...)
	a.queue = nil
	return items
}

func queuedKinds(items []queuedInput) []string {
	kinds := make([]string, 0, len(items))
	for _, item := range items {
		kinds = append(kinds, item.kind)
	}
	return kinds
}

func copyAllowedTools(src map[string]struct{}) map[string]struct{} {
	dst := make(map[string]struct{}, len(src))
	for name := range src {
		dst[name] = struct{}{}
	}
	return dst
}

func intersectAllowedTools(src map[string]struct{}, allowlist []string) map[string]struct{} {
	if len(allowlist) == 0 {
		return src
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		if _, ok := src[name]; ok {
			allowed[name] = struct{}{}
		}
	}
	return allowed
}

func validateChildScope(parent, child types.SessionScope) error {
	if child.Principal.ID != "" && child.Principal.ID != parent.Principal.ID {
		return fmt.Errorf("%w: subagent principal must match parent", ErrCapabilityDenied)
	}
	if child.Workspace.Ref.ID != "" && child.Workspace.Ref.ID != parent.Workspace.Ref.ID {
		return fmt.Errorf("%w: subagent workspace must match parent", ErrCapabilityDenied)
	}
	if child.Workspace.WorkingDir == "" || parent.Workspace.WorkingDir == "" {
		return nil
	}
	parentDir, err := filepath.Abs(parent.Workspace.WorkingDir)
	if err != nil {
		return err
	}
	childDir, err := filepath.Abs(child.Workspace.WorkingDir)
	if err != nil {
		return err
	}
	if childDir == parentDir || filepath.Dir(childDir) == parentDir || len(childDir) > len(parentDir) && childDir[:len(parentDir)] == parentDir && childDir[len(parentDir)] == filepath.Separator {
		return nil
	}
	return fmt.Errorf("%w: subagent working directory must stay within parent workspace", ErrCapabilityDenied)
}

func resourceProviders(providers []mcp.Provider) []resources.Provider {
	loaded := make([]resources.Provider, 0, len(providers))
	for _, provider := range providers {
		loaded = append(loaded, provider)
	}
	return loaded
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
