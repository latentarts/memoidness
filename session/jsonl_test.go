package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/latentarts/memoidness/types"
)

func TestJSONLManagerCreateAppendOpenAndContinue(t *testing.T) {
	root := t.TempDir()
	manager, err := NewJSONLManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	record, err := manager.New(context.Background(), RecordOptions{
		SessionID:   "sess-1",
		Scope:       testScope("/workspace"),
		Mode:        types.ModeRef{ID: "implementation"},
		Model:       types.ModelRef{ID: "gpt-test"},
		Persistence: types.PersistenceModeSession,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	entry := types.SessionEntry{
		ID:   "entry-1",
		Kind: "message",
		Message: &types.Message{
			ID:   "msg-1",
			Role: "user",
			Parts: []types.MessagePart{{
				Kind: "text",
				Text: "hello",
			}},
		},
		At: time.Now().UTC(),
	}
	if err := manager.Append(context.Background(), record.Ref(), entry); err != nil {
		t.Fatalf("append: %v", err)
	}

	opened, err := manager.Open(context.Background(), types.SessionRef{ID: "sess-1"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(opened.Entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(opened.Entries))
	}
	if opened.Scope.Principal.ID != "principal-1" || opened.Scope.Workspace.Ref.ID != "workspace-1" {
		t.Fatalf("unexpected scope: %+v", opened.Scope)
	}
	if opened.Mode.ID != "implementation" {
		t.Fatalf("unexpected mode: %+v", opened.Mode)
	}

	recent, err := manager.ContinueRecent(context.Background(), Scope{
		Principal: "principal-1",
		Workspace: "workspace-1",
	})
	if err != nil {
		t.Fatalf("continue recent: %v", err)
	}
	if recent.SessionID != "sess-1" {
		t.Fatalf("unexpected recent session: %s", recent.SessionID)
	}
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

func TestJSONLManagerMalformedEntry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "broken.jsonl")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatalf("write broken session: %v", err)
	}

	manager, err := NewJSONLManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.Open(context.Background(), types.SessionRef{ID: "broken"}); err == nil {
		t.Fatal("expected malformed session error")
	}
}

func TestJSONLManagerForkCloneAndNavigate(t *testing.T) {
	root := t.TempDir()
	manager, err := NewJSONLManager(root)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	record, err := manager.New(context.Background(), RecordOptions{
		SessionID:   "sess-1",
		Scope:       testScope("/workspace"),
		Mode:        types.ModeRef{ID: "implementation"},
		Model:       types.ModelRef{ID: "gpt-test"},
		Persistence: types.PersistenceModeSession,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	entry1 := types.SessionEntry{ID: "entry-1", Kind: "message", At: time.Now().UTC()}
	entry2 := types.SessionEntry{ID: "entry-2", Kind: "message", At: time.Now().UTC()}
	if err := manager.Append(context.Background(), record.Ref(), entry1); err != nil {
		t.Fatalf("append entry1: %v", err)
	}
	if err := manager.Append(context.Background(), record.Ref(), entry2); err != nil {
		t.Fatalf("append entry2: %v", err)
	}

	forked, err := manager.Fork(context.Background(), record.Ref(), types.EntryRef{ID: "entry-1"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if len(forked.Entries) != 2 || forked.Entries[0].Kind != "branch_marker" || forked.Entries[1].ID != "entry-1" {
		t.Fatalf("unexpected forked entries: %+v", forked.Entries)
	}

	cloned, err := manager.Clone(context.Background(), record.Ref())
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(cloned.Entries) != 3 || cloned.Entries[0].Kind != "branch_marker" || cloned.Entries[2].ID != "entry-2" {
		t.Fatalf("unexpected cloned entries: %+v", cloned.Entries)
	}

	navigated, err := manager.Navigate(context.Background(), record.Ref(), types.EntryRef{ID: "entry-1"})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if len(navigated.Entries) != 1 || navigated.Entries[0].ID != "entry-1" {
		t.Fatalf("unexpected navigated entries: %+v", navigated.Entries)
	}
}
