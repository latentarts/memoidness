package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/types"
)

type OpenAIConfig struct {
	ID      string
	BaseURL string
	APIKey  string
	Client  *http.Client
}

type OpenAIProvider struct {
	id      string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAICompatibleProvider(cfg OpenAIConfig) *OpenAIProvider {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAIProvider{
		id:      cfg.ID,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		client:  client,
	}
}

func (p *OpenAIProvider) ID() string {
	return p.id
}

func (p *OpenAIProvider) SupportsStreaming() bool {
	return true
}

func (p *OpenAIProvider) Execute(ctx context.Context, req types.ModelRequest) (types.ModelResponse, error) {
	httpReq, err := p.buildRequest(ctx, req, false)
	if err != nil {
		return types.ModelResponse{}, err
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return types.ModelResponse{}, err
	}
	defer resp.Body.Close()

	var payload openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return types.ModelResponse{}, err
	}
	return payload.toModelResponse(), nil
}

func (p *OpenAIProvider) StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error) {
	httpReq, err := p.buildRequest(ctx, req, true)
	if err != nil {
		return types.ModelResponse{}, err
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return types.ModelResponse{}, err
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var assembled strings.Builder
	var toolCalls []types.ToolCall
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return types.ModelResponse{}, err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk openAIStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return types.ModelResponse{}, err
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					assembled.WriteString(choice.Delta.Content)
					if err := sink.Emit(events.Envelope{
						Type:    "message_delta",
						Payload: types.MessageDelta{MessageID: choice.Delta.ID, Delta: choice.Delta.Content},
					}); err != nil {
						return types.ModelResponse{}, err
					}
				}
				for _, call := range choice.Delta.ToolCalls {
					toolCalls = append(toolCalls, types.ToolCall{
						ID:        call.ID,
						Name:      call.Function.Name,
						Arguments: []byte(call.Function.Arguments),
					})
					if err := sink.Emit(events.Envelope{
						Type: "tool_call_delta",
						Payload: types.ToolCall{
							ID:        call.ID,
							Name:      call.Function.Name,
							Arguments: []byte(call.Function.Arguments),
						},
					}); err != nil {
						return types.ModelResponse{}, err
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	result := types.ModelResponse{
		ToolCalls:  toolCalls,
		StopReason: "stop",
	}
	if assembled.Len() > 0 || len(toolCalls) > 0 {
		result.Assistant = &types.Message{
			ID:   "assistant-stream",
			Role: "assistant",
		}
		if assembled.Len() > 0 {
			result.Assistant.Parts = append(result.Assistant.Parts, types.MessagePart{
				Kind: "text",
				Text: assembled.String(),
			})
		}
		for _, call := range toolCalls {
			call := call
			result.Assistant.Parts = append(result.Assistant.Parts, types.MessagePart{
				Kind: "tool_call",
				Call: &call,
			})
		}
	}
	return result, nil
}

func (p *OpenAIProvider) buildRequest(ctx context.Context, req types.ModelRequest, streaming bool) (*http.Request, error) {
	body := openAIChatRequest{
		Model:    req.Model.ID,
		Messages: make([]openAIMessage, 0, len(req.Messages)),
		Tools:    make([]openAITool, 0, len(req.VisibleTools)),
		Stream:   streaming,
	}
	for _, instruction := range req.Instructions {
		body.Messages = append(body.Messages, openAIMessage{
			Role:    "system",
			Content: instruction.Text,
		})
	}
	for _, message := range req.Messages {
		body.Messages = append(body.Messages, toOpenAIMessage(message))
	}
	for _, tool := range req.VisibleTools {
		body.Tools = append(body.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return httpReq, nil
}

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall   `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			ID        string           `json:"id"`
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

func toOpenAIMessage(message types.Message) openAIMessage {
	converted := openAIMessage{Role: message.Role}
	switch message.Role {
	case "tool":
		if len(message.Parts) > 0 && message.Parts[0].Result != nil {
			converted.ToolCallID = message.Parts[0].Result.CallID
			converted.Content = string(message.Parts[0].Result.Payload)
		}
	default:
		parts := make([]string, 0, len(message.Parts))
		toolCalls := make([]openAIToolCall, 0)
		for _, part := range message.Parts {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
			if part.Call != nil {
				toolCalls = append(toolCalls, openAIToolCall{
					ID:   part.Call.ID,
					Type: "function",
					Function: openAIFunction{
						Name:      part.Call.Name,
						Arguments: string(part.Call.Arguments),
					},
				})
			}
		}
		converted.Content = strings.Join(parts, "\n")
		if len(toolCalls) > 0 {
			converted.ToolCalls = toolCalls
		}
	}
	return converted
}

func (r openAIChatResponse) toModelResponse() types.ModelResponse {
	if len(r.Choices) == 0 {
		return types.ModelResponse{}
	}
	choice := r.Choices[0]
	message := choice.Message
	response := types.ModelResponse{
		Usage: types.Usage{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
			TotalTokens:  r.Usage.TotalTokens,
		},
		StopReason: "stop",
	}
	if len(message.ToolCalls) > 0 {
		response.ToolCalls = make([]types.ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			response.ToolCalls = append(response.ToolCalls, types.ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: []byte(call.Function.Arguments),
			})
		}
	}
	if message.Content != "" || len(response.ToolCalls) > 0 {
		assistant := &types.Message{
			ID:   fmt.Sprintf("assistant-%d", len(response.ToolCalls)+1),
			Role: "assistant",
		}
		if message.Content != "" {
			assistant.Parts = append(assistant.Parts, types.MessagePart{
				Kind: "text",
				Text: message.Content,
			})
		}
		for _, call := range response.ToolCalls {
			call := call
			assistant.Parts = append(assistant.Parts, types.MessagePart{
				Kind: "tool_call",
				Call: &call,
			})
		}
		response.Assistant = assistant
	}
	return response
}
