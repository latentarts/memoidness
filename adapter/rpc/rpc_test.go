package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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

func TestCreateSessionRequestResponse(t *testing.T) {
	svc := &stubService{
		createSession: func(_ context.Context, req service.CreateSessionRequest) (types.SessionSnapshot, error) {
			if req.Options.Scope.Principal.ID != "principal-1" {
				t.Fatalf("unexpected principal: %+v", req.Options.Scope)
			}
			return testSnapshot(), nil
		},
	}
	server, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	client, srv := net.Pipe()
	defer client.Close()
	defer srv.Close()
	reader := bufio.NewReader(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, srv)
	}()

	if err := json.NewEncoder(client).Encode(RequestEnvelope{
		ID:     "req-1",
		Method: "create_session",
		Params: mustRaw(t, createSessionParams{
			Scope: types.SessionScope{
				Principal: types.PrincipalRef{ID: "principal-1"},
				Workspace: types.WorkspaceSpec{
					Ref: types.WorkspaceRef{ID: "workspace-1"},
				},
			},
			Model: types.ModelRef{ProviderID: "stub", ID: "gpt-test"},
		}),
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	resp := readEnvelope(t, client, reader)
	if resp.ID != "req-1" || resp.Type != "response" {
		t.Fatalf("unexpected response envelope: %+v", resp)
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	svc := &stubService{}
	eventReady := make(chan struct{}, 1)
	svc.subscribe = func(ctx context.Context, ref types.SessionRef, listener events.Listener) (func(), error) {
		if ref.ID != "session-1" {
			t.Fatalf("unexpected ref: %+v", ref)
		}
		go func() {
			<-eventReady
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
	server, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	client, srv := net.Pipe()
	defer client.Close()
	defer srv.Close()
	reader := bufio.NewReader(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, srv)
	}()

	if err := json.NewEncoder(client).Encode(RequestEnvelope{
		ID:     "sub-1",
		Method: "subscribe",
		Params: mustRaw(t, subscribeParams{
			Ref: types.SessionRef{ID: "session-1", Principal: "principal-1", Workspace: "workspace-1"},
		}),
	}); err != nil {
		t.Fatalf("encode subscribe request: %v", err)
	}

	ack := readEnvelope(t, client, reader)
	if ack.Type != "response" {
		t.Fatalf("unexpected subscribe ack: %+v", ack)
	}

	eventReady <- struct{}{}
	event := readEnvelope(t, client, reader)
	if event.Type != "event" {
		t.Fatalf("unexpected event envelope: %+v", event)
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected event payload: %#v", event.Payload)
	}
	if payload["Type"] != "message_delta" && payload["type"] != "message_delta" {
		t.Fatalf("unexpected event payload contents: %#v", payload)
	}
}

func TestPromptRequestResponse(t *testing.T) {
	svc := &stubService{
		prompt: func(_ context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
			if ref.ID != "session-1" || input.Text != "inspect repo" || !opts.Stream {
				t.Fatalf("unexpected prompt args: %+v %+v %+v", ref, input, opts)
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
	server, err := New(Config{Service: svc})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	client, srv := net.Pipe()
	defer client.Close()
	defer srv.Close()
	reader := bufio.NewReader(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.Serve(ctx, srv)
	}()

	if err := json.NewEncoder(client).Encode(RequestEnvelope{
		ID:     "req-2",
		Method: "prompt",
		Params: mustRaw(t, promptParams{
			Ref:   types.SessionRef{ID: "session-1", Principal: "principal-1", Workspace: "workspace-1"},
			Input: types.UserInput{Text: "inspect repo"},
			Opts:  types.PromptOptions{Stream: true},
		}),
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	resp := readEnvelope(t, client, reader)
	if resp.Type != "response" || resp.ID != "req-2" {
		t.Fatalf("unexpected prompt response: %+v", resp)
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

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return encoded
}

func readEnvelope(t *testing.T, conn net.Conn, reader *bufio.Reader) ResponseEnvelope {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var env ResponseEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("decode envelope: %v line=%s", err, string(line))
	}
	return env
}
