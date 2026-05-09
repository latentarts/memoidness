package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/latentarts/memoidness/types"
)

var ErrInvalidScope = errors.New("invalid resource scope")

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

type FilesystemLoader struct{}

func NewFilesystemLoader() FilesystemLoader {
	return FilesystemLoader{}
}

func (FilesystemLoader) Load(ctx context.Context, scope Scope) (Loaded, error) {
	if scope.WorkingDir == "" {
		return Loaded{
			Diagnostics: []types.Diagnostic{diagnostic("info", "resource_working_dir_empty", "", "working directory not set; skipping discovery")},
		}, nil
	}

	roots := make([]string, 0, 1+len(scope.ContextRoots))
	roots = append(roots, scope.WorkingDir)
	roots = append(roots, scope.ContextRoots...)

	stopPaths := normalizeStopPaths(scope.StopPaths)
	seen := map[string]struct{}{}
	loaded := Loaded{}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return Loaded{}, err
		}
		if root == "" {
			continue
		}

		absRoot, err := filepath.Abs(root)
		if err != nil {
			loaded.Diagnostics = append(loaded.Diagnostics, diagnostic("warning", "resource_root_invalid", root, err.Error()))
			continue
		}
		if isStopped(absRoot, stopPaths) {
			loaded.Diagnostics = append(loaded.Diagnostics, diagnostic("info", "resource_root_stopped", absRoot, "skipped due to stop path"))
			continue
		}
		if _, ok := seen[absRoot]; ok {
			continue
		}
		seen[absRoot] = struct{}{}

		files, diags := discoverInstructionFiles(absRoot, stopPaths)
		loaded.Diagnostics = append(loaded.Diagnostics, diags...)
		for _, candidate := range files {
			text, err := os.ReadFile(candidate)
			if err != nil {
				if os.IsNotExist(err) {
					loaded.Diagnostics = append(loaded.Diagnostics, diagnostic("info", "resource_missing_optional", candidate, "optional file not found"))
					continue
				}
				return Loaded{}, err
			}

			name := filepath.Base(candidate)
			loaded.Instructions = append(loaded.Instructions, types.InstructionSource{
				Name: name,
				Kind: "file",
				Path: candidate,
				Text: string(text),
			})
			loaded.ContextFiles = append(loaded.ContextFiles, ContextFile{
				Name: name,
				Path: candidate,
				Text: string(text),
			})
		}
	}

	sort.Slice(loaded.Instructions, func(i, j int) bool {
		if loaded.Instructions[i].Path == loaded.Instructions[j].Path {
			return loaded.Instructions[i].Name < loaded.Instructions[j].Name
		}
		return loaded.Instructions[i].Path < loaded.Instructions[j].Path
	})
	sort.Slice(loaded.ContextFiles, func(i, j int) bool {
		if loaded.ContextFiles[i].Path == loaded.ContextFiles[j].Path {
			return loaded.ContextFiles[i].Name < loaded.ContextFiles[j].Name
		}
		return loaded.ContextFiles[i].Path < loaded.ContextFiles[j].Path
	})

	return loaded, nil
}

func discoverInstructionFiles(root string, stopPaths []string) ([]string, []types.Diagnostic) {
	candidates := []string{
		"AGENTS.md",
		"CODEX.md",
		"CLAUDE.md",
		"CURSOR.md",
		"GEMINI.md",
		"PI.md",
	}

	paths := make([]string, 0, len(candidates))
	diags := make([]types.Diagnostic, 0)
	for _, name := range candidates {
		path := filepath.Join(root, name)
		if isStopped(path, stopPaths) {
			diags = append(diags, diagnostic("info", "resource_path_stopped", path, "skipped due to stop path"))
			continue
		}
		paths = append(paths, path)
	}
	return paths, diags
}

func normalizeStopPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		normalized = append(normalized, abs)
	}
	slices.Sort(normalized)
	return normalized
}

func isStopped(path string, stopPaths []string) bool {
	if len(stopPaths) == 0 {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, stop := range stopPaths {
		if absPath == stop || strings.HasPrefix(absPath, stop+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func diagnostic(severity, code, path, message string) types.Diagnostic {
	return types.Diagnostic{
		Severity: severity,
		Code:     code,
		Path:     path,
		Message:  message,
	}
}
