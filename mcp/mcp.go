package mcp

import (
	"context"
	"slices"

	"github.com/latentarts/memoidness/resources"
	"github.com/latentarts/memoidness/tools"
	"github.com/latentarts/memoidness/types"
)

type Provider interface {
	Descriptor() types.MCPServerDescriptor
	Load(ctx context.Context, scope resources.Scope) (resources.Loaded, error)
	Tools(ctx context.Context, scope types.SessionScope, mode types.ModeRef) ([]tools.Tool, error)
}

type Resolved struct {
	Servers     []types.MCPServerDescriptor
	Providers   []Provider
	Tools       []tools.Tool
	Diagnostics []types.Diagnostic
}

type Registry interface {
	Resolve(ctx context.Context, scope types.SessionScope, mode types.ModeRef) (Resolved, error)
}

type StaticServer struct {
	Provider     Provider
	AllowedModes []string
	Enabled      bool
}

type StaticRegistry struct {
	servers []StaticServer
}

func NewStaticRegistry(servers ...StaticServer) *StaticRegistry {
	return &StaticRegistry{servers: append([]StaticServer(nil), servers...)}
}

func (r *StaticRegistry) Resolve(ctx context.Context, scope types.SessionScope, mode types.ModeRef) (Resolved, error) {
	resolved := Resolved{}
	for _, server := range r.servers {
		if err := ctx.Err(); err != nil {
			return Resolved{}, err
		}
		if !server.Enabled || server.Provider == nil {
			continue
		}
		if len(server.AllowedModes) > 0 && !slices.Contains(server.AllowedModes, mode.ID) {
			continue
		}
		descriptor := server.Provider.Descriptor()
		resolved.Servers = append(resolved.Servers, descriptor)
		resolved.Providers = append(resolved.Providers, server.Provider)
		providerTools, err := server.Provider.Tools(ctx, scope, mode)
		if err != nil {
			return Resolved{}, err
		}
		resolved.Tools = append(resolved.Tools, providerTools...)
	}
	return resolved, nil
}
