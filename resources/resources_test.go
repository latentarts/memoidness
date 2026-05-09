package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemLoaderLoadsInstructionsInDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CODEX.md"), []byte("codex"), 0o644); err != nil {
		t.Fatalf("write codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	loaded, err := NewFilesystemLoader().Load(context.Background(), Scope{WorkingDir: root})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Instructions) != 2 {
		t.Fatalf("expected 2 instructions, got %d", len(loaded.Instructions))
	}
	if filepath.Base(loaded.Instructions[0].Path) != "AGENTS.md" {
		t.Fatalf("unexpected first instruction: %s", loaded.Instructions[0].Path)
	}
}

func TestFilesystemLoaderHonorsStopPaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte("agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	loaded, err := NewFilesystemLoader().Load(context.Background(), Scope{
		WorkingDir: root,
		StopPaths:  []string{path},
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Instructions) != 0 {
		t.Fatalf("expected no instructions, got %d", len(loaded.Instructions))
	}
	if len(loaded.Diagnostics) == 0 {
		t.Fatal("expected stop-path diagnostic")
	}
}
