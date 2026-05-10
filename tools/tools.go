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
	DefinitionsFor(names map[string]struct{}) []types.ToolDefinition
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

func (r *StaticRegistry) DefinitionsFor(names map[string]struct{}) []types.ToolDefinition {
	if len(names) == 0 {
		return nil
	}
	definitions := make([]types.ToolDefinition, 0, len(names))
	for name := range names {
		if tool, ok := r.tools[name]; ok {
			definitions = append(definitions, tool.Definition())
		}
	}
	return definitions
}

func (r *StaticRegistry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

type CompositeRegistry struct {
	base  Registry
	extra map[string]Tool
}

func NewCompositeRegistry(base Registry, extra ...Tool) *CompositeRegistry {
	index := make(map[string]Tool, len(extra))
	for _, tool := range extra {
		index[tool.Definition().Name] = tool
	}
	return &CompositeRegistry{
		base:  base,
		extra: index,
	}
}

func (r *CompositeRegistry) Definitions() []types.ToolDefinition {
	seen := make(map[string]struct{}, len(r.extra))
	definitions := make([]types.ToolDefinition, 0, len(r.extra))
	if r.base != nil {
		definitions = append(definitions, r.base.Definitions()...)
		for _, definition := range definitions {
			seen[definition.Name] = struct{}{}
		}
	}
	for _, tool := range r.extra {
		definition := tool.Definition()
		if _, ok := seen[definition.Name]; ok {
			continue
		}
		definitions = append(definitions, definition)
	}
	return definitions
}

func (r *CompositeRegistry) DefinitionsFor(names map[string]struct{}) []types.ToolDefinition {
	if len(names) == 0 {
		return nil
	}
	definitions := make([]types.ToolDefinition, 0, len(names))
	for name := range names {
		if tool, ok := r.Lookup(name); ok {
			definitions = append(definitions, tool.Definition())
		}
	}
	return definitions
}

func (r *CompositeRegistry) Lookup(name string) (Tool, bool) {
	if tool, ok := r.extra[name]; ok {
		return tool, true
	}
	if r.base == nil {
		return nil, false
	}
	return r.base.Lookup(name)
}
