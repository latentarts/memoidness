package resources

import (
	"context"

	"github.com/latentarts/memoidness/types"
)

type Scope struct {
	WorkingDir   string
	ContextRoots []string
	Mode         types.DiscoveryMode
	StopPaths    []string
}

type ContextFile struct {
	Name string
	Path string
	Text string
}

type PromptTemplate struct {
	Name string
	Text string
}

type SkillResource struct {
	Name string
	Text string
}

type ExtensionRegistration struct {
	Name string
	Kind string
}

type Loaded struct {
	Instructions []types.InstructionSource
	ContextFiles []ContextFile
	Prompts      []PromptTemplate
	Skills       []SkillResource
	Extensions   []ExtensionRegistration
	Diagnostics  []types.Diagnostic
}

type Loader interface {
	Load(ctx context.Context, scope Scope) (Loaded, error)
}

type NoopLoader struct{}

func (NoopLoader) Load(context.Context, Scope) (Loaded, error) {
	return Loaded{}, nil
}
