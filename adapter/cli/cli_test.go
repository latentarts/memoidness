package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

func TestNewRequiresService(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected config validation error")
	}
}

func TestCreateCommandCallsServiceAndRendersSnapshot(t *testing.T) {
	svc := &stubService{
		createSession: func(_ context.Context, req service.CreateSessionRequest) (types.SessionSnapshot, error) {
			if req.Options.Scope.Principal.ID != "principal-1" {
				t.Fatalf("unexpected principal: %+v", req.Options.Scope)
			}
			if req.Options.Scope.Workspace.Ref.ID != "workspace-1" {
				t.Fatalf("unexpected workspace: %+v", req.Options.Scope)
			}
			if req.Options.Model.ID != "gpt-test" || req.Options.Model.ProviderID != "stub" {
				t.Fatalf("unexpected model: %+v", req.Options.Model)
			}
			return testSnapshot(), nil
		},
	}
	var stdout bytes.Buffer
	adapter, err := New(Config{
		Service:          svc,
		Stdout:           &stdout,
		DefaultPrincipal: "principal-1",
		DefaultWorkspace: "workspace-1",
		DefaultModel:     types.ModelRef{ProviderID: "stub", ID: "gpt-test"},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if err := adapter.Run(context.Background(), []string{"create"}); err != nil {
		t.Fatalf("run create: %v", err)
	}
	if got := stdout.String(); !containsText(got, "session: session-1") || !containsText(got, "workspace: workspace-1") {
		t.Fatalf("unexpected create output: %q", got)
	}
}

func TestPromptCommandStreamsDeltasAndToolProgress(t *testing.T) {
	svc := &stubService{}
	svc.subscribe = func(_ context.Context, ref types.SessionRef, listener events.Listener) (func(), error) {
		svc.listener = listener
		return func() {}, nil
	}
	svc.prompt = func(_ context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
		if ref.ID != "session-1" || input.Text != "inspect repo" || !opts.Stream {
			t.Fatalf("unexpected prompt args: %+v %+v %+v", ref, input, opts)
		}
		svc.listener(events.Envelope{
			Type:    "message_delta",
			Payload: types.MessageDelta{MessageID: "msg-1", Delta: "hello "},
		})
		svc.listener(events.Envelope{
			Type:    "tool_execution_update",
			Payload: types.ToolProgress{CallID: "call-1", Stream: "stdout", Text: "running\n"},
		})
		svc.listener(events.Envelope{
			Type:    "message_delta",
			Payload: types.MessageDelta{MessageID: "msg-1", Delta: "world"},
		})
		return types.RunResult{
			FinalOutput: types.Message{
				ID:   "assistant-1",
				Role: "assistant",
				Parts: []types.MessagePart{{
					Kind: "text",
					Text: "hello world",
				}},
			},
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	adapter, err := New(Config{
		Service:          svc,
		Stdout:           &stdout,
		Stderr:           &stderr,
		DefaultPrincipal: "principal-1",
		DefaultWorkspace: "workspace-1",
		DefaultStream:    true,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if err := adapter.Run(context.Background(), []string{"prompt", "--session", "session-1", "inspect", "repo"}); err != nil {
		t.Fatalf("run prompt: %v", err)
	}
	if got := stdout.String(); got != "hello world\n" {
		t.Fatalf("unexpected streamed stdout: %q", got)
	}
	if got := stderr.String(); !containsText(got, "[tool call-1 stdout] running") {
		t.Fatalf("unexpected streamed stderr: %q", got)
	}
}

func TestPromptCommandPrintsFinalOutputWithoutStreaming(t *testing.T) {
	svc := &stubService{
		prompt: func(_ context.Context, _ types.SessionRef, _ types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
			if opts.Stream {
				t.Fatalf("expected non-streaming prompt")
			}
			return types.RunResult{
				FinalOutput: types.Message{
					ID:   "assistant-1",
					Role: "assistant",
					Parts: []types.MessagePart{{
						Kind: "text",
						Text: "plain output",
					}},
				},
			}, nil
		},
	}
	var stdout bytes.Buffer
	adapter, err := New(Config{
		Service:          svc,
		Stdout:           &stdout,
		DefaultPrincipal: "principal-1",
		DefaultWorkspace: "workspace-1",
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if err := adapter.Run(context.Background(), []string{"prompt", "--session", "session-1", "--stream=false", "inspect"}); err != nil {
		t.Fatalf("run prompt: %v", err)
	}
	if got := stdout.String(); got != "plain output\n" {
		t.Fatalf("unexpected non-streamed output: %q", got)
	}
}

func TestContinueCommandUsesScopeAwareLookup(t *testing.T) {
	svc := &stubService{
		continueRecent: func(_ context.Context, req service.ContinueRecentRequest) (types.SessionSnapshot, error) {
			if req.Scope != (session.Scope{Principal: "principal-1", Workspace: "workspace-1"}) {
				t.Fatalf("unexpected continue scope: %+v", req.Scope)
			}
			return testSnapshot(), nil
		},
	}
	adapter, err := New(Config{
		Service:          svc,
		Stdout:           &bytes.Buffer{},
		DefaultPrincipal: "principal-1",
		DefaultWorkspace: "workspace-1",
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if err := adapter.Run(context.Background(), []string{"continue"}); err != nil {
		t.Fatalf("run continue: %v", err)
	}
}

type stubService struct {
	createSession  func(context.Context, service.CreateSessionRequest) (types.SessionSnapshot, error)
	openSession    func(context.Context, types.SessionRef) (types.SessionSnapshot, error)
	continueRecent func(context.Context, service.ContinueRecentRequest) (types.SessionSnapshot, error)
	prompt         func(context.Context, types.SessionRef, types.UserInput, types.PromptOptions) (types.RunResult, error)
	steer          func(context.Context, types.SessionRef, types.UserInput) error
	followUp       func(context.Context, types.SessionRef, types.UserInput) error
	abort          func(context.Context, types.SessionRef) error
	fork           func(context.Context, types.SessionRef, types.EntryRef) (types.SessionSnapshot, error)
	clone          func(context.Context, types.SessionRef) (types.SessionSnapshot, error)
	navigate       func(context.Context, types.SessionRef, types.EntryRef) (types.SessionSnapshot, error)
	promoteSkill   func(context.Context, types.SessionRef, types.SkillPromotionRequest) (types.SkillPromotionResult, error)
	snapshot       func(context.Context, types.SessionRef) (types.SessionSnapshot, error)
	setMode        func(context.Context, types.SessionRef, types.ModeRef) (types.SessionSnapshot, error)
	subscribe      func(context.Context, types.SessionRef, events.Listener) (func(), error)
	listener       events.Listener
}

func (s *stubService) CreateSession(ctx context.Context, req service.CreateSessionRequest) (types.SessionSnapshot, error) {
	return s.createSession(ctx, req)
}

func (s *stubService) OpenSession(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	if s.openSession != nil {
		return s.openSession(ctx, ref)
	}
	return testSnapshot(), nil
}

func (s *stubService) ContinueRecent(ctx context.Context, req service.ContinueRecentRequest) (types.SessionSnapshot, error) {
	return s.continueRecent(ctx, req)
}

func (s *stubService) Prompt(ctx context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
	return s.prompt(ctx, ref, input, opts)
}

func (s *stubService) Steer(ctx context.Context, ref types.SessionRef, input types.UserInput) error {
	if s.steer != nil {
		return s.steer(ctx, ref, input)
	}
	return nil
}

func (s *stubService) FollowUp(ctx context.Context, ref types.SessionRef, input types.UserInput) error {
	if s.followUp != nil {
		return s.followUp(ctx, ref, input)
	}
	return nil
}

func (s *stubService) Abort(ctx context.Context, ref types.SessionRef) error {
	if s.abort != nil {
		return s.abort(ctx, ref)
	}
	return nil
}

func (s *stubService) Fork(ctx context.Context, ref types.SessionRef, at types.EntryRef) (types.SessionSnapshot, error) {
	if s.fork != nil {
		return s.fork(ctx, ref, at)
	}
	return testSnapshot(), nil
}

func (s *stubService) Clone(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	if s.clone != nil {
		return s.clone(ctx, ref)
	}
	return testSnapshot(), nil
}

func (s *stubService) Navigate(ctx context.Context, ref types.SessionRef, target types.EntryRef) (types.SessionSnapshot, error) {
	if s.navigate != nil {
		return s.navigate(ctx, ref, target)
	}
	return testSnapshot(), nil
}

func (s *stubService) PromoteSkill(ctx context.Context, ref types.SessionRef, req types.SkillPromotionRequest) (types.SkillPromotionResult, error) {
	if s.promoteSkill != nil {
		return s.promoteSkill(ctx, ref, req)
	}
	return types.SkillPromotionResult{}, nil
}

func (s *stubService) Snapshot(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	if s.snapshot != nil {
		return s.snapshot(ctx, ref)
	}
	return testSnapshot(), nil
}

func (s *stubService) SetMode(ctx context.Context, ref types.SessionRef, mode types.ModeRef) (types.SessionSnapshot, error) {
	if s.setMode != nil {
		return s.setMode(ctx, ref, mode)
	}
	return testSnapshot(), nil
}

func (s *stubService) Subscribe(ctx context.Context, ref types.SessionRef, listener events.Listener) (func(), error) {
	if s.subscribe != nil {
		return s.subscribe(ctx, ref, listener)
	}
	s.listener = listener
	return func() {}, nil
}

func testSnapshot() types.SessionSnapshot {
	return types.SessionSnapshot{
		SessionID: "session-1",
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref: types.WorkspaceRef{ID: "workspace-1"},
			},
		},
		Mode:     types.ModeRef{ID: "implementation"},
		BranchID: "main",
	}
}

func containsText(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
