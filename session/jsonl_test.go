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
		WorkingDir:  "/workspace",
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

	recent, err := manager.ContinueRecent(context.Background(), Scope{WorkingDir: "/workspace"})
	if err != nil {
		t.Fatalf("continue recent: %v", err)
	}
	if recent.SessionID != "sess-1" {
		t.Fatalf("unexpected recent session: %s", recent.SessionID)
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
