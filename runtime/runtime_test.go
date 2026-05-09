package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/provider"
	"github.com/latentarts/memoidness/types"
)

func TestValidateRequiresProviderRegistry(t *testing.T) {
	rt := New(Config{})
	if err := rt.Validate(context.Background()); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestPromptExecutesSingleTurn(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})

	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "ack: hello" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestPromptStreamingEmitsDeltaEvents(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{Model: types.ModelRef{ID: "test-model"}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	sess.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		mu.Lock()
		seen = append(seen, envelope.Type)
		mu.Unlock()
	})

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{Stream: true})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "ack: hello" {
		t.Fatalf("unexpected output: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !contains(seen, "message_delta") {
		t.Fatalf("expected message_delta event, got %v", seen)
	}
}

func TestPromptToolLoopExecutesBuiltinTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	rt := New(Config{
		Providers: provider.NewStaticRegistry("tool", toolLoopProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
		},
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model:      types.ModelRef{ID: "test-model"},
		WorkingDir: root,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "read it"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "done after tool" {
		t.Fatalf("unexpected final output: %q", got)
	}
	if got := len(result.Snapshot.Messages); got < 4 {
		t.Fatalf("expected persisted tool loop history, got %d messages", got)
	}
}

func TestAbortCancelsRunningPrompt(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("slow", slowProvider{}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{Model: types.ModelRef{ID: "test-model"}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := sess.Abort(context.Background()); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected prompt cancellation error")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
