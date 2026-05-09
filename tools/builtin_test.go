package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/types"
)

func TestReadFileToolAllowedAndBlocked(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	call := types.ToolCall{ID: "call-1", Arguments: mustJSON(t, types.ToolReadFileArgs{Path: path})}
	env := Env{WorkingDir: root, Policy: policy.SessionPolicy{Runtime: policy.RuntimePolicy{
		Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
	}}}

	result, err := (ReadFileTool{}).Execute(context.Background(), call, env)
	if err != nil {
		t.Fatalf("execute read: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %s", result.Status)
	}

	env.Policy.Runtime.Filesystem.ReadableRoots = []string{filepath.Join(root, "other")}
	result, err = (ReadFileTool{}).Execute(context.Background(), call, env)
	if err != nil {
		t.Fatalf("execute blocked read: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected error status, got %s", result.Status)
	}
}

func TestWriteFileToolAndProcessTool(t *testing.T) {
	root := t.TempDir()
	writeCall := types.ToolCall{ID: "call-write", Arguments: mustJSON(t, types.ToolWriteFileArgs{
		Path: "out.txt",
		Text: "hello",
	})}
	events := make([]types.ToolProgress, 0)
	env := Env{
		WorkingDir: root,
		Policy: policy.SessionPolicy{Runtime: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{WritableRoots: []string{root}},
			Process:    policy.ProcessPolicy{AllowedCommands: []string{"sh -c"}},
		}},
		Emit: func(progress types.ToolProgress) error {
			events = append(events, progress)
			return nil
		},
	}

	result, err := (WriteFileTool{}).Execute(context.Background(), writeCall, env)
	if err != nil {
		t.Fatalf("execute write: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %s", result.Status)
	}

	processCall := types.ToolCall{ID: "call-exec", Arguments: mustJSON(t, types.ToolExecArgs{
		Command: []string{"sh", "-c", "printf hello"},
	})}
	result, err = (ProcessTool{}).Execute(context.Background(), processCall, env)
	if err != nil {
		t.Fatalf("execute process: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %s", result.Status)
	}
	if len(events) == 0 {
		t.Fatal("expected streamed process events")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}
