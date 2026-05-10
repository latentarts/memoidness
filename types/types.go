package types

import (
	"encoding/json"
	"time"
)

type ModelRef struct {
	ProviderID string
	ID         string
}

type PrincipalRef struct {
	ID string
}

type WorkspaceRef struct {
	ID string
}

type WorkspaceSpec struct {
	Ref           WorkspaceRef
	Kind          string
	WorkingDir    string
	RemoteProject string
	ContextRoots  []string
}

type SessionScope struct {
	Principal PrincipalRef
	Workspace WorkspaceSpec
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

type ToolProgress struct {
	CallID   string
	Stream   string
	Text     string
	ExitCode *int
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

type MessageDelta struct {
	MessageID string
	Delta     string
}

type ModeRef struct {
	ID string
}

type CapabilityRef struct {
	ID       string
	Category string
}

type CapabilityDescriptor struct {
	Ref          CapabilityRef
	Name         string
	Description  string
	DefaultOn    bool
	Dependencies []CapabilityRef
}

type MCPServerRef struct {
	ID string
}

type MCPServerDescriptor struct {
	Ref          MCPServerRef
	Name         string
	SourceKind   string
	DefaultOn    bool
	Capabilities []string
}

type RuntimeError struct {
	Code    string
	Message string
	Detail  string
}

type UserInput struct {
	Text  string
	Parts []MessagePart
}

type PromptOptions struct {
	Stream bool
}

type SkillPromotionTarget string

const (
	SkillPromotionTargetWorkspace SkillPromotionTarget = "workspace"
	SkillPromotionTargetPrincipal SkillPromotionTarget = "principal"
)

type SkillPromotionRequest struct {
	Name   string
	Target SkillPromotionTarget
}

type SkillPromotionResult struct {
	Name   string
	Target SkillPromotionTarget
	Path   string
	Source string
}

type SubagentRequest struct {
	SessionID string
	Scope     *SessionScope
	Mode      *ModeRef
	Model     *ModelRef
	ToolAllowlist []string
	Input     UserInput
	Options   PromptOptions
}

type SubagentResult struct {
	SessionRef SessionRef
	Run        RunResult
}

type CompactOptions struct {
	KeepRecentMessages int
}

type CompactResult struct {
	Summary Message
}

type SessionSnapshot struct {
	SessionID string
	Scope     SessionScope
	Mode      ModeRef
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
	ID        string
	Principal string
	Workspace string
}

type EntryRef struct {
	ID string
}

type SessionEntry struct {
	ID            string
	Kind          string
	Message       *Message
	ToolCall      *ToolCall
	ToolResult    *ToolResult
	Summary       *Message
	Diagnostic    *Diagnostic
	SkillPromotion *SkillPromotionResult
	ToolAllowlist []string
	ParentID      string
	ParentSession *SessionRef
	Mode          *ModeRef
	Capability    *CapabilityRef
	MCPServers    []MCPServerDescriptor
	Subagent      *SessionRef
	At            time.Time
}

type ToolReadFileArgs struct {
	Path string `json:"path"`
}

type ToolReadFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ToolWriteFileArgs struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Append bool   `json:"append"`
}

type ToolWriteFileResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Append  bool   `json:"append"`
	Written bool   `json:"written"`
}

type ToolExecArgs struct {
	Command []string `json:"command"`
}

type ToolExecResult struct {
	Command  []string `json:"command"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
}
