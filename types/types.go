package types

import (
	"encoding/json"
	"time"
)

type ModelRef struct {
	ProviderID string
	ID         string
}

type DiscoveryMode string

const (
	DiscoveryModeAuto     DiscoveryMode = "auto"
	DiscoveryModeExplicit DiscoveryMode = "explicit"
)

type PersistenceMode string

const (
	PersistenceModeNone    PersistenceMode = "none"
	PersistenceModeSession PersistenceMode = "session"
)

type SessionLimits struct {
	MaxInputTokens int
	MaxToolCalls   int
	MaxTurns       int
}

type InstructionSource struct {
	Name string
	Kind string
	Path string
	Text string
}

type ResourceRef struct {
	Kind string
	Name string
	Path string
}

type Diagnostic struct {
	Severity string
	Code     string
	Path     string
	Message  string
}

type Message struct {
	ID           string
	Role         string
	Parts        []MessagePart
	ParentTurnID string
	ProviderMeta map[string]any
}

type MessagePart struct {
	Kind       string
	Text       string
	Call       *ToolCall
	Result     *ToolResult
	Ref        *ResourceRef
	Diagnostic *Diagnostic
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	TurnID    string
}

type ToolResult struct {
	CallID  string
	Status  string
	Payload json.RawMessage
	Error   *Diagnostic
}

type ModelRequest struct {
	Model           ModelRef
	Instructions    []InstructionSource
	Messages        []Message
	VisibleTools    []ToolDefinition
	ProviderOptions map[string]any
	Limits          SessionLimits
	Streaming       bool
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type ModelResponse struct {
	Assistant    *Message
	ToolCalls    []ToolCall
	Usage        Usage
	StopReason   string
	ProviderMeta map[string]any
}

type UserInput struct {
	Text  string
	Parts []MessagePart
}

type PromptOptions struct {
	Stream bool
}

type CompactOptions struct {
	KeepRecentMessages int
}

type CompactResult struct {
	Summary Message
}

type SessionSnapshot struct {
	SessionID string
	Messages  []Message
	BranchID  string
}

type RunResult struct {
	FinalOutput Message
	Snapshot    SessionSnapshot
	Usage       Usage
	ToolCalls   []ToolCall
	Compaction  *CompactResult
	Diagnostics []Diagnostic
}

type SessionRef struct {
	ID string
}

type EntryRef struct {
	ID string
}

type SessionEntry struct {
	ID         string
	Kind       string
	Message    *Message
	ToolCall   *ToolCall
	ToolResult *ToolResult
	Summary    *Message
	Diagnostic *Diagnostic
	ParentID   string
	At         time.Time
}
