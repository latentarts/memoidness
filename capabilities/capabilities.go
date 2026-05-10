package capabilities

import (
	"context"
	"slices"

	"github.com/latentarts/memoidness/resources"
	"github.com/latentarts/memoidness/types"
)

type Plugin interface {
	Descriptor() types.CapabilityDescriptor
	Apply(ctx context.Context, scope types.SessionScope, mode types.ModeRef, resolved *Resolved) error
}

type Resolved struct {
	Mode         types.ModeRef
	Capabilities []types.CapabilityDescriptor
	AllowedTools map[string]struct{}
	Resources    []resources.Provider
	AllowSubagents bool
}

type Registry interface {
	Resolve(ctx context.Context, scope types.SessionScope, requestedMode types.ModeRef) (Resolved, error)
}

type StaticRegistry struct {
	plugins     []Plugin
	defaultMode types.ModeRef
}

func NewStaticRegistry(defaultMode types.ModeRef, plugins ...Plugin) *StaticRegistry {
	return &StaticRegistry{
		plugins:     append([]Plugin(nil), plugins...),
		defaultMode: defaultMode,
	}
}

func (r *StaticRegistry) resolve(ctx context.Context, scope types.SessionScope, requestedMode types.ModeRef) (Resolved, error) {
	resolved := Resolved{
		AllowedTools: make(map[string]struct{}),
	}
	mode := requestedMode
	if mode.ID == "" {
		mode = r.defaultMode
	}
	if mode.ID == "" {
		mode = types.ModeRef{ID: "implementation"}
	}
	resolved.Mode = mode
	for _, plugin := range r.plugins {
		resolved.Capabilities = append(resolved.Capabilities, plugin.Descriptor())
		if err := plugin.Apply(ctx, scope, mode, &resolved); err != nil {
			return Resolved{}, err
		}
	}
	return resolved, nil
}

func (r *StaticRegistry) Resolve(ctx context.Context, scope types.SessionScope, requestedMode types.ModeRef) (Resolved, error) {
	return r.resolve(ctx, scope, requestedMode)
}

type ModeToolPlugin struct {
	Capability   types.CapabilityDescriptor
	ToolNames    []string
	AllowedModes []string
}

type OrchestrationPlugin struct {
	Capability   types.CapabilityDescriptor
	AllowedModes []string
}

func (p OrchestrationPlugin) Descriptor() types.CapabilityDescriptor {
	return p.Capability
}

func (p OrchestrationPlugin) Apply(_ context.Context, _ types.SessionScope, mode types.ModeRef, resolved *Resolved) error {
	if len(p.AllowedModes) > 0 && !slices.Contains(p.AllowedModes, mode.ID) {
		return nil
	}
	resolved.AllowSubagents = true
	return nil
}

func (p ModeToolPlugin) Descriptor() types.CapabilityDescriptor {
	return p.Capability
}

func (p ModeToolPlugin) Apply(_ context.Context, _ types.SessionScope, mode types.ModeRef, resolved *Resolved) error {
	if len(p.AllowedModes) > 0 && !slices.Contains(p.AllowedModes, mode.ID) {
		return nil
	}
	for _, name := range p.ToolNames {
		resolved.AllowedTools[name] = struct{}{}
	}
	return nil
}

func DefaultPlugins() []Plugin {
	return []Plugin{
		ModeToolPlugin{
			Capability: types.CapabilityDescriptor{
				Ref:         types.CapabilityRef{ID: "tool.read_file", Category: "tool"},
				Name:        "Read File",
				Description: "Allow read-only file inspection",
				DefaultOn:   true,
			},
			ToolNames:    []string{"read_file"},
			AllowedModes: []string{"plan", "implementation"},
		},
		ModeToolPlugin{
			Capability: types.CapabilityDescriptor{
				Ref:         types.CapabilityRef{ID: "tool.write_file", Category: "tool"},
				Name:        "Write File",
				Description: "Allow file modification",
				DefaultOn:   true,
			},
			ToolNames:    []string{"write_file"},
			AllowedModes: []string{"implementation"},
		},
		ModeToolPlugin{
			Capability: types.CapabilityDescriptor{
				Ref:         types.CapabilityRef{ID: "tool.exec", Category: "tool"},
				Name:        "Execute Process",
				Description: "Allow process execution",
				DefaultOn:   true,
			},
			ToolNames:    []string{"exec"},
			AllowedModes: []string{"implementation"},
		},
		OrchestrationPlugin{
			Capability: types.CapabilityDescriptor{
				Ref:         types.CapabilityRef{ID: "orchestration.subagents", Category: "orchestration"},
				Name:        "Subagent Orchestration",
				Description: "Allow delegated child-session execution",
				DefaultOn:   true,
			},
			AllowedModes: []string{"plan", "implementation"},
		},
	}
}

type ResourcePlugin struct {
	Capability types.CapabilityDescriptor
	Provider   resources.Provider
}

func (p ResourcePlugin) Descriptor() types.CapabilityDescriptor {
	return p.Capability
}

func (p ResourcePlugin) Apply(context.Context, types.SessionScope, types.ModeRef, *Resolved) error {
	return nil
}

type StaticResourcePlugin struct {
	Capability   types.CapabilityDescriptor
	Provider     resources.Provider
	AllowedModes []string
}

func (p StaticResourcePlugin) Descriptor() types.CapabilityDescriptor {
	return p.Capability
}

func (p StaticResourcePlugin) Apply(_ context.Context, _ types.SessionScope, mode types.ModeRef, resolved *Resolved) error {
	if len(p.AllowedModes) > 0 && !slices.Contains(p.AllowedModes, mode.ID) {
		return nil
	}
	if p.Provider != nil {
		resolved.Resources = append(resolved.Resources, p.Provider)
	}
	return nil
}

func DefaultResourcePlugins() []Plugin {
	return []Plugin{
		StaticResourcePlugin{
			Capability: types.CapabilityDescriptor{
				Ref:         types.CapabilityRef{ID: "resource.plan-guidance", Category: "resource"},
				Name:        "Plan Guidance",
				Description: "Adds planning-oriented runtime guidance",
				DefaultOn:   true,
			},
			AllowedModes: []string{"plan"},
			Provider: resources.StaticProvider{
				Loaded: resources.Loaded{
					Instructions: []types.InstructionSource{{
						Name: "plan-mode-guidance",
						Kind: "capability",
						Text: "Operate in planning mode. Avoid mutating actions and focus on analysis, sequencing, and verification.",
					}},
					Skills: []resources.SkillResource{{
						Name:   "plan-review",
						Text:   "Skill for planning, review, and decomposition work.",
						Source: "capability",
					}},
				},
			},
		},
	}
}
