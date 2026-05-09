package runtime

import (
	"context"
	"testing"

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
