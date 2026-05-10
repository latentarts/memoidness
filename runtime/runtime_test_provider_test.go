package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/resources"
	"github.com/latentarts/memoidness/tools"
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

type visibleToolsProvider struct {
	seen *[]string
}

func (p visibleToolsProvider) ID() string { return "visible-tools" }
func (p visibleToolsProvider) SupportsStreaming() bool { return false }
func (p visibleToolsProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return p.Execute(ctx, req)
}
func (p visibleToolsProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	tools := make([]string, 0, len(req.VisibleTools))
	for _, tool := range req.VisibleTools {
		tools = append(tools, tool.Name)
	}
	*p.seen = append((*p.seen)[:0], tools...)
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-visible",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "text",
				Text: "ok",
			}},
		},
		StopReason: "stop",
	}, nil
}

type forbiddenToolProvider struct{}

func (forbiddenToolProvider) ID() string { return "forbidden-tool" }
func (forbiddenToolProvider) SupportsStreaming() bool { return false }
func (forbiddenToolProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return forbiddenToolProvider{}.Execute(ctx, req)
}
func (forbiddenToolProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		return types.ModelResponse{
			Assistant: &types.Message{
				ID:   "assistant-after-denial",
				Role: "assistant",
				Parts: []types.MessagePart{{
					Kind: "text",
					Text: "tool denied handled",
				}},
			},
			StopReason: "stop",
		}, nil
	}
	args, _ := json.Marshal(types.ToolWriteFileArgs{Path: "out.txt", Text: "hello"})
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-denied-call",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "tool_call",
				Call: &types.ToolCall{ID: "tool-denied", Name: "write_file", Arguments: args},
			}},
		},
		ToolCalls: []types.ToolCall{{
			ID:        "tool-denied",
			Name:      "write_file",
			Arguments: args,
		}},
		StopReason: "tool_calls",
	}, nil
}

type queuedInputProvider struct{}

func (queuedInputProvider) ID() string { return "queued-input" }
func (queuedInputProvider) SupportsStreaming() bool { return false }
func (queuedInputProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return queuedInputProvider{}.Execute(ctx, req)
}
func (queuedInputProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		time.Sleep(100 * time.Millisecond)
		for i := len(req.Messages) - 1; i >= 0; i-- {
			msg := req.Messages[i]
			if msg.Role == "developer" && msg.ProviderMeta != nil && msg.ProviderMeta["queue_kind"] == "steer" {
				return types.ModelResponse{
					Assistant: &types.Message{
						ID:   "assistant-steered",
						Role: "assistant",
						Parts: []types.MessagePart{{
							Kind: "text",
							Text: "steered: " + msg.Parts[0].Text,
						}},
					},
					StopReason: "stop",
				}, nil
			}
			if msg.Role == "user" && msg.ProviderMeta != nil && msg.ProviderMeta["queue_kind"] == "follow_up" {
				return types.ModelResponse{
					Assistant: &types.Message{
						ID:   "assistant-queued",
						Role: "assistant",
						Parts: []types.MessagePart{{
							Kind: "text",
							Text: "queued: " + msg.Parts[0].Text,
						}},
					},
					StopReason: "stop",
				}, nil
			}
		}
		return types.ModelResponse{
			Assistant: &types.Message{
				ID:   "assistant-no-queue",
				Role: "assistant",
				Parts: []types.MessagePart{{Kind: "text", Text: "no queued input"}},
			},
			StopReason: "stop",
		}, nil
	}
	args, _ := json.Marshal(types.ToolReadFileArgs{Path: "input.txt"})
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-queued-call",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "tool_call",
				Call: &types.ToolCall{ID: "tool-q", Name: "read_file", Arguments: args},
			}},
		},
		ToolCalls: []types.ToolCall{{
			ID:        "tool-q",
			Name:      "read_file",
			Arguments: args,
		}},
		StopReason: "tool_calls",
	}, nil
}

type mcpToolLoopProvider struct{}

func (mcpToolLoopProvider) ID() string { return "mcp-tool-loop" }
func (mcpToolLoopProvider) SupportsStreaming() bool { return false }
func (mcpToolLoopProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	return mcpToolLoopProvider{}.Execute(ctx, req)
}
func (mcpToolLoopProvider) Execute(_ context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		return types.ModelResponse{
			Assistant: &types.Message{
				ID:   "assistant-after-mcp-tool",
				Role: "assistant",
				Parts: []types.MessagePart{{
					Kind: "text",
					Text: "done after mcp tool",
				}},
			},
			StopReason: "stop",
		}, nil
	}
	args, _ := json.Marshal(map[string]string{"text": "hello from mcp"})
	return types.ModelResponse{
		Assistant: &types.Message{
			ID:   "assistant-mcp-call",
			Role: "assistant",
			Parts: []types.MessagePart{{
				Kind: "tool_call",
				Call: &types.ToolCall{ID: "mcp-tool-1", Name: "mcp_echo", Arguments: args},
			}},
		},
		ToolCalls: []types.ToolCall{{
			ID:        "mcp-tool-1",
			Name:      "mcp_echo",
			Arguments: args,
		}},
		StopReason: "tool_calls",
	}, nil
}

type mcpTestProvider struct{}

func (mcpTestProvider) Descriptor() types.MCPServerDescriptor {
	return types.MCPServerDescriptor{
		Ref:          types.MCPServerRef{ID: "mcp-test"},
		Name:         "MCP Test",
		SourceKind:   "test",
		DefaultOn:    true,
		Capabilities: []string{"resources", "tools"},
	}
}

func (mcpTestProvider) Load(_ context.Context, _ resources.Scope) (resources.Loaded, error) {
	return resources.Loaded{
		Skills: []resources.SkillResource{{
			Name:   "mcp-skill",
			Text:   "skill from mcp",
			Source: "mcp",
		}},
		Extensions: []resources.ExtensionRegistration{{
			Name: "mcp.test",
			Kind: "mcp",
		}},
		Diagnostics: []types.Diagnostic{{
			Severity: "info",
			Code:     "mcp_provider_loaded",
			Message:  "loaded test MCP provider",
		}},
	}, nil
}

func (mcpTestProvider) Tools(context.Context, types.SessionScope, types.ModeRef) ([]tools.Tool, error) {
	return []tools.Tool{mcpEchoTool{}}, nil
}

type mcpEchoTool struct{}

func (mcpEchoTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: "mcp_echo", Description: "Echo text from the MCP provider"}
}

func (mcpEchoTool) Execute(_ context.Context, call types.ToolCall, env tools.Env) (types.ToolResult, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return types.ToolResult{
			CallID: call.ID,
			Status: "error",
			Error: &types.Diagnostic{
				Severity: "error",
				Code:     "mcp_tool_invalid_arguments",
				Message:  err.Error(),
			},
		}, nil
	}
	if env.Emit != nil {
		_ = env.Emit(types.ToolProgress{CallID: call.ID, Stream: "stdout", Text: args.Text})
	}
	payload, _ := json.Marshal(map[string]string{"echo": args.Text})
	return types.ToolResult{CallID: call.ID, Status: "ok", Payload: payload}, nil
}
