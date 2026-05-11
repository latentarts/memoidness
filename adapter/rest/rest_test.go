package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/types"
)

func TestNewRequiresService(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected config validation error")
	}
}

func TestCreateSessionRoute(t *testing.T) {
	svc := &stubService{
		createSession: func(_ context.Context, req service.CreateSessionRequest) (types.SessionSnapshot, error) {
			if req.Options.Scope.Principal.ID != "principal-1" {
				t.Fatalf("unexpected principal: %+v", req.Options.Scope)
			}
			if req.Options.Scope.Workspace.Ref.ID != "workspace-1" {
				t.Fatalf("unexpected workspace: %+v", req.Options.Scope)
			}
			return testSnapshot(), nil
		},
	}
	adapter, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	body := mustJSON(t, createSessionRequest{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref: types.WorkspaceRef{ID: "workspace-1"},
			},
		},
		Model: types.ModelRef{ProviderID: "stub", ID: "gpt-test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	adapter.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var snapshot types.SessionSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if snapshot.SessionID != "session-1" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestPromptRoute(t *testing.T) {
	svc := &stubService{
		prompt: func(_ context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
			if ref.ID != "session-1" || ref.Principal != "principal-1" || ref.Workspace != "workspace-1" {
				t.Fatalf("unexpected ref: %+v", ref)
			}
			if input.Text != "inspect repo" || !opts.Stream {
				t.Fatalf("unexpected prompt args: %+v %+v", input, opts)
			}
			return types.RunResult{
				FinalOutput: types.Message{
					ID:   "assistant-1",
					Role: "assistant",
					Parts: []types.MessagePart{{
						Kind: "text",
						Text: "ok",
					}},
				},
			}, nil
		},
	}
	adapter, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	body := mustJSON(t, promptRequest{
		Input:  types.UserInput{Text: "inspect repo"},
		Stream: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/sessions/session-1/prompt?principal=principal-1&workspace=workspace-1", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	adapter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var result types.RunResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestEventsRouteStreamsSSE(t *testing.T) {
	svc := &stubService{}
	eventsReady := make(chan struct{}, 1)
	svc.subscribe = func(ctx context.Context, ref types.SessionRef, listener events.Listener) (func(), error) {
		if ref.ID != "session-1" {
			t.Fatalf("unexpected session ref: %+v", ref)
		}
		go func() {
			<-eventsReady
			listener(events.Envelope{
				ID:        "ev-1",
				Type:      "message_delta",
				Principal: "principal-1",
				Workspace: "workspace-1",
				SessionID: "session-1",
				Payload:   types.MessageDelta{MessageID: "msg-1", Delta: "hello"},
			})
		}()
		return func() {}, nil
	}
	adapter, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sessions/session-1/events?principal=principal-1&workspace=workspace-1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		adapter.ServeHTTP(rec, req)
		close(done)
	}()

	eventsReady <- struct{}{}
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event stream")
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "data:") || !strings.Contains(body, "\"hello\"") {
		t.Fatalf("unexpected sse body: %q", body)
	}
}

func TestSnapshotRoute(t *testing.T) {
	svc := &stubService{
		snapshot: func(_ context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
			if ref.ID != "session-1" {
				t.Fatalf("unexpected ref: %+v", ref)
			}
			return testSnapshot(), nil
		},
	}
	adapter, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sessions/session-1?principal=principal-1&workspace=workspace-1", nil)
	rec := httptest.NewRecorder()
	adapter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
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
	if s.continueRecent != nil {
		return s.continueRecent(ctx, req)
	}
	return testSnapshot(), nil
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	buf, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return buf
}
