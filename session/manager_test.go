package session

import (
	"context"
	"testing"
	"time"

	"github.com/latentarts/memoidness/types"
)

func TestInMemoryManagerForkCloneAndNavigate(t *testing.T) {
	manager := NewInMemoryManager()
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
