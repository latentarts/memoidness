package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/types"
)

func TestOpenAICompatibleProviderExecuteAndStream(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if payload["stream"] == true {
				return jsonResponse(`data: {"choices":[{"delta":{"id":"msg-1","content":"hello "}}]}

data: {"choices":[{"delta":{"id":"msg-1","content":"world"}}]}

data: [DONE]
`, "text/event-stream"), nil
			}
			return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"hello world"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, "application/json"), nil
		}),
	}

	p := NewOpenAICompatibleProvider(OpenAIConfig{
		ID:      "oa",
		BaseURL: "https://example.invalid",
		Client:  client,
	})
	req := types.ModelRequest{
		Model: types.ModelRef{ID: "test-model"},
		Messages: []types.Message{{
			ID:   "user-1",
			Role: "user",
			Parts: []types.MessagePart{{
				Kind: "text",
				Text: "hi",
			}},
		}},
	}

	nonstream, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := nonstream.Assistant.Parts[0].Text; got != "hello world" {
		t.Fatalf("unexpected execute response: %q", got)
	}

	var deltas []string
	streamed, err := p.StreamCompletion(context.Background(), req, sinkFunc(func(ev events.RuntimeEvent) error {
		envelope := ev.(events.Envelope)
		delta := envelope.Payload.(types.MessageDelta)
		deltas = append(deltas, delta.Delta)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream completion: %v", err)
	}
	if got := streamed.Assistant.Parts[0].Text; got != "hello world" {
		t.Fatalf("unexpected streamed response: %q", got)
	}
	if strings.Join(deltas, "") != "hello world" {
		t.Fatalf("unexpected streamed deltas: %v", deltas)
	}
}

type sinkFunc func(events.RuntimeEvent) error

func (f sinkFunc) Emit(ev events.RuntimeEvent) error {
	return f(ev)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body, contentType string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
