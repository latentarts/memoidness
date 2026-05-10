package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/latentarts/memoidness/types"
)

func TestFilesystemLoaderLoadsInstructionsInDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CODEX.md"), []byte("codex"), 0o644); err != nil {
		t.Fatalf("write codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	loaded, err := NewFilesystemLoader().Load(context.Background(), Scope{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref:        types.WorkspaceRef{ID: "workspace-1"},
				Kind:       "local",
				WorkingDir: root,
			},
		},
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Instructions) != 2 {
		t.Fatalf("expected 2 instructions, got %d", len(loaded.Instructions))
	}
	if filepath.Base(loaded.Instructions[0].Path) != "AGENTS.md" {
		t.Fatalf("unexpected first instruction: %s", loaded.Instructions[0].Path)
	}
}

func TestFilesystemLoaderHonorsStopPaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte("agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	loaded, err := NewFilesystemLoader().Load(context.Background(), Scope{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref:        types.WorkspaceRef{ID: "workspace-1"},
				Kind:       "local",
				WorkingDir: root,
			},
		},
		StopPaths: []string{path},
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Instructions) != 0 {
		t.Fatalf("expected no instructions, got %d", len(loaded.Instructions))
	}
	if len(loaded.Diagnostics) == 0 {
		t.Fatal("expected stop-path diagnostic")
	}
}

func TestFilesystemLoaderLoadsWorkspaceSkills(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".memoidness", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "review-plan.md"), []byte("workspace skill"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	loaded, err := NewFilesystemLoader().Load(context.Background(), Scope{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref:        types.WorkspaceRef{ID: "workspace-1"},
				Kind:       "local",
				WorkingDir: root,
			},
		},
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Skills) != 1 {
		t.Fatalf("expected one workspace skill, got %+v", loaded.Skills)
	}
	if loaded.Skills[0].Name != "review-plan" || loaded.Skills[0].Source != "workspace" {
		t.Fatalf("unexpected loaded skill: %+v", loaded.Skills[0])
	}
}

func TestCompositeLoaderMergesProvidersAndGeneratesSkill(t *testing.T) {
	root := t.TempDir()
	loader := NewCompositeLoader(
		NewFilesystemLoader(),
		StaticProvider{
			Loaded: Loaded{
				Skills: []SkillResource{{
					Name:   "provider-skill",
					Text:   "from provider",
					Source: "provider",
				}},
				Extensions: []ExtensionRegistration{{
					Name: "mcp.stub",
					Kind: "mcp",
				}},
			},
		},
	)
	loaded, err := loader.Load(context.Background(), Scope{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref:        types.WorkspaceRef{ID: "workspace-1"},
				Kind:       "local",
				WorkingDir: root,
			},
		},
		TaskHint: "plan a refactor",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Skills) != 1 {
		t.Fatalf("expected provider skill to suppress generation, got %d skills", len(loaded.Skills))
	}
	if len(loaded.Extensions) != 1 || loaded.Extensions[0].Kind != "mcp" {
		t.Fatalf("unexpected extensions: %+v", loaded.Extensions)
	}
}

func TestCompositeLoaderGeneratesEphemeralSkillWhenMissing(t *testing.T) {
	root := t.TempDir()
	loader := NewCompositeLoader(NewFilesystemLoader())
	loaded, err := loader.Load(context.Background(), Scope{
		Scope: types.SessionScope{
			Principal: types.PrincipalRef{ID: "principal-1"},
			Workspace: types.WorkspaceSpec{
				Ref:        types.WorkspaceRef{ID: "workspace-1"},
				Kind:       "local",
				WorkingDir: root,
			},
		},
		TaskHint: "write tests",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Skills) != 1 || !loaded.Skills[0].Ephemeral {
		t.Fatalf("expected one ephemeral generated skill, got %+v", loaded.Skills)
	}
	if loaded.Skills[0].Source != "generated" {
		t.Fatalf("unexpected generated skill source: %+v", loaded.Skills[0])
	}
}
