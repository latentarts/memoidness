package service

import (
	"context"
	"testing"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/provider"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

func TestServiceCreatePromptForkAndNavigate(t *testing.T) {
	manager := session.NewInMemoryManager()
	rt := runtime.New(runtime.Config{
		Providers:      provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		SessionManager: manager,
	})
	svc := New(rt, manager)

	snapshot, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		Options: runtime.SessionOptions{
			Model: types.ModelRef{ID: "test-model"},
			Scope: testScope(t.TempDir()),
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ref := types.SessionRef{
		ID:        snapshot.SessionID,
		Principal: snapshot.Scope.Principal.ID,
		Workspace: snapshot.Scope.Workspace.Ref.ID,
	}

	first, err := svc.Prompt(context.Background(), ref, types.UserInput{Text: "one"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	second, err := svc.Prompt(context.Background(), ref, types.UserInput{Text: "two"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	forked, err := svc.Fork(context.Background(), ref, types.EntryRef{ID: first.Snapshot.Messages[0].ID})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.SessionID == second.Snapshot.SessionID {
		t.Fatalf("expected forked session id to differ, got %q", forked.SessionID)
	}
	if len(forked.Messages) != 1 {
		t.Fatalf("expected forked message count 1, got %d", len(forked.Messages))
	}

	navigated, err := svc.Navigate(context.Background(), ref, types.EntryRef{ID: first.Snapshot.Messages[0].ID})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if len(navigated.Messages) != 1 {
		t.Fatalf("expected navigated message count 1, got %d", len(navigated.Messages))
	}
}

type stubProvider struct {
	id string
}

func (p stubProvider) ID() string { return p.id }
func (p stubProvider) SupportsStreaming() bool { return false }
func (p stubProvider) StreamCompletion(context.Context, types.ModelRequest, events.Sink) (types.ModelResponse, error) {
	panic("unexpected streaming call")
}
func (p stubProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "text",
				Text: "ack: " + last.Parts[0].Text,
			}},
		},
		StopReason: "stop",
	}, nil
}

func testScope(root string) types.SessionScope {
	return types.SessionScope{
		Principal: types.PrincipalRef{ID: "principal-1"},
		Workspace: types.WorkspaceSpec{
			Ref:        types.WorkspaceRef{ID: "workspace-1"},
			Kind:       "local",
			WorkingDir: root,
		},
	}
}
