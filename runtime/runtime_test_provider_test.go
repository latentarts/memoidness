package runtime

import (
	"context"

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
	return false
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

func (p stubProvider) StreamCompletion(context.Context, types.ModelRequest, events.Sink) error {
	return nil
}
