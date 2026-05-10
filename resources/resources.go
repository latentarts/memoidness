package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/latentarts/memoidness/types"
)

var ErrInvalidScope = errors.New("invalid resource scope")
var ErrUnsupportedPromotionTarget = errors.New("unsupported skill promotion target")

type Scope struct {
	Scope        types.SessionScope
	ContextRoots []string
	Mode         types.DiscoveryMode
	StopPaths    []string
	TaskHint     string
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
	Name      string
	Text      string
	Ephemeral bool
	Source    string
	Path      string
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

type Provider interface {
	Load(ctx context.Context, scope Scope) (Loaded, error)
}

type Promoter interface {
	Promote(ctx context.Context, scope types.SessionScope, skill SkillResource, target types.SkillPromotionTarget) (types.SkillPromotionResult, error)
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
	workingDir := scope.Scope.Workspace.WorkingDir
	if workingDir == "" {
		return Loaded{
			Diagnostics: []types.Diagnostic{diagnostic("info", "resource_working_dir_empty", "", "working directory not set; skipping discovery")},
		}, nil
	}

	roots := make([]string, 0, 1+len(scope.ContextRoots))
	roots = append(roots, workingDir)
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

		skills, diags := discoverSkillFiles(absRoot, stopPaths)
		loaded.Diagnostics = append(loaded.Diagnostics, diags...)
		for _, candidate := range skills {
			text, err := os.ReadFile(candidate)
			if err != nil {
				return Loaded{}, err
			}
			name := strings.TrimSuffix(filepath.Base(candidate), filepath.Ext(candidate))
			loaded.Skills = append(loaded.Skills, SkillResource{
				Name:   name,
				Text:   string(text),
				Source: "workspace",
				Path:   candidate,
			})
			loaded.Instructions = append(loaded.Instructions, types.InstructionSource{
				Name: name,
				Kind: "skill",
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

type WorkspacePromoter struct{}

func NewWorkspacePromoter() WorkspacePromoter {
	return WorkspacePromoter{}
}

func (WorkspacePromoter) Promote(ctx context.Context, scope types.SessionScope, skill SkillResource, target types.SkillPromotionTarget) (types.SkillPromotionResult, error) {
	if target == "" {
		target = types.SkillPromotionTargetWorkspace
	}
	if target != types.SkillPromotionTargetWorkspace {
		return types.SkillPromotionResult{}, fmt.Errorf("%w: %s", ErrUnsupportedPromotionTarget, target)
	}
	if err := ctx.Err(); err != nil {
		return types.SkillPromotionResult{}, err
	}
	workingDir := scope.Workspace.WorkingDir
	if workingDir == "" {
		return types.SkillPromotionResult{}, ErrInvalidScope
	}
	dir := filepath.Join(workingDir, ".memoidness", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return types.SkillPromotionResult{}, err
	}
	path := filepath.Join(dir, skillFileName(skill.Name))
	if err := os.WriteFile(path, []byte(skill.Text), 0o644); err != nil {
		return types.SkillPromotionResult{}, err
	}
	return types.SkillPromotionResult{
		Name:   skill.Name,
		Target: target,
		Path:   path,
		Source: "workspace",
	}, nil
}

type CompositeLoader struct {
	Base      Loader
	Providers []Provider
}

func NewCompositeLoader(base Loader, providers ...Provider) CompositeLoader {
	if base == nil {
		base = NoopLoader{}
	}
	return CompositeLoader{
		Base:      base,
		Providers: append([]Provider(nil), providers...),
	}
}

func (l CompositeLoader) Load(ctx context.Context, scope Scope) (Loaded, error) {
	base, err := l.Base.Load(ctx, scope)
	if err != nil {
		return Loaded{}, err
	}
	loaded := base
	for _, provider := range l.Providers {
		added, err := provider.Load(ctx, scope)
		if err != nil {
			return Loaded{}, err
		}
		mergeInto(&loaded, added)
	}
	if len(loaded.Skills) == 0 && strings.TrimSpace(scope.TaskHint) != "" {
		skill := SkillResource{
			Name:      generatedSkillName(scope.TaskHint),
			Text:      "Ephemeral generated skill for: " + strings.TrimSpace(scope.TaskHint),
			Ephemeral: true,
			Source:    "generated",
		}
		loaded.Skills = append(loaded.Skills, skill)
		loaded.Instructions = append(loaded.Instructions, types.InstructionSource{
			Name: "generated-skill:" + skill.Name,
			Kind: "generated_skill",
			Text: skill.Text,
		})
		loaded.Diagnostics = append(loaded.Diagnostics, diagnostic("info", "generated_skill_created", "", "created ephemeral generated skill"))
	}
	return loaded, nil
}

type StaticProvider struct {
	Loaded Loaded
}

func (p StaticProvider) Load(context.Context, Scope) (Loaded, error) {
	return p.Loaded, nil
}

func mergeInto(dst *Loaded, src Loaded) {
	dst.Instructions = append(dst.Instructions, src.Instructions...)
	dst.ContextFiles = append(dst.ContextFiles, src.ContextFiles...)
	dst.Prompts = append(dst.Prompts, src.Prompts...)
	dst.Skills = append(dst.Skills, src.Skills...)
	dst.Extensions = append(dst.Extensions, src.Extensions...)
	dst.Diagnostics = append(dst.Diagnostics, src.Diagnostics...)
}

func generatedSkillName(taskHint string) string {
	trimmed := strings.TrimSpace(strings.ToLower(taskHint))
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	if trimmed == "" {
		return "generated-skill"
	}
	if len(trimmed) > 32 {
		trimmed = trimmed[:32]
	}
	return "generated-" + trimmed
}

func skillFileName(name string) string {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	trimmed = strings.ReplaceAll(trimmed, string(filepath.Separator), "-")
	if trimmed == "" {
		trimmed = "skill"
	}
	return trimmed + ".md"
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

func discoverSkillFiles(root string, stopPaths []string) ([]string, []types.Diagnostic) {
	skillsDir := filepath.Join(root, ".memoidness", "skills")
	if isStopped(skillsDir, stopPaths) {
		return nil, []types.Diagnostic{diagnostic("info", "resource_path_stopped", skillsDir, "skipped due to stop path")}
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []types.Diagnostic{diagnostic("warning", "resource_skill_dir_invalid", skillsDir, err.Error())}
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(skillsDir, entry.Name())
		if isStopped(path, stopPaths) {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
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
