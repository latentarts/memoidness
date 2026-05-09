package tools

import (
	"context"

	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/types"
)

type Env struct {
	WorkingDir string
	Policy     policy.SessionPolicy
	Emit       func(types.ToolProgress) error
}

type Tool interface {
	Definition() types.ToolDefinition
	Execute(ctx context.Context, call types.ToolCall, env Env) (types.ToolResult, error)
}

type Registry interface {
	Definitions() []types.ToolDefinition
	Lookup(name string) (Tool, bool)
}

type StaticRegistry struct {
	tools map[string]Tool
}

func NewStaticRegistry(registered ...Tool) *StaticRegistry {
	index := make(map[string]Tool, len(registered))
	for _, tool := range registered {
		index[tool.Definition().Name] = tool
	}
	return &StaticRegistry{tools: index}
}

func (r *StaticRegistry) Definitions() []types.ToolDefinition {
	definitions := make([]types.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, tool.Definition())
	}
	return definitions
}

func (r *StaticRegistry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}
