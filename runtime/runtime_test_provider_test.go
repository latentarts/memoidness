package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/types"
)

type stubProvider struct {
	id string
}

func (p stubProvider) ID() string {
	return p.id
}

func (p stubProvider) SupportsStreaming() bool {
	return true
}

func (p stubProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-1",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "text",
				Text: "ack: " + last.Parts[0].Text,
			}},
		},
		StopReason: "stop",
	}, nil
}

func (p stubProvider) StreamCompletion(_ context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	if err := sink.Emit(events.Envelope{
		Type:    "message_delta",
		Payload: types.MessageDelta{MessageID: "assistant-1", Delta: "ack: "},
	}); err != nil {
		return types.ModelResponse{}, err
	}
	if err := sink.Emit(events.Envelope{
		Type:    "message_delta",
		Payload: types.MessageDelta{MessageID: "assistant-1", Delta: last.Parts[0].Text},
	}); err != nil {
		return types.ModelResponse{}, err
	}
	return p.Execute(context.Background(), req)
}

type toolLoopProvider struct{}

func (toolLoopProvider) ID() string { return "tool" }
func (toolLoopProvider) SupportsStreaming() bool { return false }
func (toolLoopProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return toolLoopProvider{}.Execute(ctx, req)
}
func (toolLoopProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		return types.ModelResponse{
			Assistant: &types.Message{
				ID:   "assistant-final",
				Role: "assistant",
				Parts: []types.MessagePart{{
					Kind: "text",
					Text: "done after tool",
				}},
			},
			StopReason: "stop",
		}, nil
	}
	args, _ := json.Marshal(types.ToolReadFileArgs{Path: "input.txt"})
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-call",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "tool_call",
				Call: &types.ToolCall{ID: "tool-1", Name: "read_file", Arguments: args},
			}},
		},
		ToolCalls: []types.ToolCall{{
			ID:        "tool-1",
			Name:      "read_file",
			Arguments: args,
		}},
		StopReason: "tool_calls",
	}, nil
}

type slowProvider struct{}

func (slowProvider) ID() string { return "slow" }
func (slowProvider) SupportsStreaming() bool { return false }
func (slowProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return slowProvider{}.Execute(ctx, req)
}
func (slowProvider) Execute(ctx context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	select {
	case <-time.After(250 * time.Millisecond):
		last := req.Messages[len(req.Messages)-1]
		return types.ModelResponse{
			Assistant: &types.Message{
				ID:   "assistant-slow",
				Role: "assistant",
				Parts: []types.MessagePart{{Kind: "text", Text: "ack: " + last.Parts[0].Text}},
			},
			StopReason: "stop",
		}, nil
	case <-ctx.Done():
		return types.ModelResponse{}, ctx.Err()
	}
}
