package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

var ErrInvalidConfig = errors.New("invalid cli adapter config")

type serviceAPI interface {
	CreateSession(ctx context.Context, req service.CreateSessionRequest) (types.SessionSnapshot, error)
	OpenSession(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error)
	ContinueRecent(ctx context.Context, req service.ContinueRecentRequest) (types.SessionSnapshot, error)
	Prompt(ctx context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error)
	Steer(ctx context.Context, ref types.SessionRef, input types.UserInput) error
	FollowUp(ctx context.Context, ref types.SessionRef, input types.UserInput) error
	Abort(ctx context.Context, ref types.SessionRef) error
	Fork(ctx context.Context, ref types.SessionRef, at types.EntryRef) (types.SessionSnapshot, error)
	Clone(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error)
	Navigate(ctx context.Context, ref types.SessionRef, target types.EntryRef) (types.SessionSnapshot, error)
	PromoteSkill(ctx context.Context, ref types.SessionRef, req types.SkillPromotionRequest) (types.SkillPromotionResult, error)
	Snapshot(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error)
	SetMode(ctx context.Context, ref types.SessionRef, mode types.ModeRef) (types.SessionSnapshot, error)
	Subscribe(ctx context.Context, ref types.SessionRef, listener events.Listener) (func(), error)
}

type Config struct {
	Service            serviceAPI
	Stdout             io.Writer
	Stderr             io.Writer
	DefaultPrincipal   string
	DefaultWorkspace   string
	DefaultWorkingDir  string
	DefaultModel       types.ModelRef
	DefaultPersistence types.PersistenceMode
	DefaultStream      bool
}

type Adapter struct {
	svc                serviceAPI
	stdout             io.Writer
	stderr             io.Writer
	defaultPrincipal   string
	defaultWorkspace   string
	defaultWorkingDir  string
	defaultModel       types.ModelRef
	defaultPersistence types.PersistenceMode
	defaultStream      bool
}

func New(cfg Config) (*Adapter, error) {
	if cfg.Service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalidConfig)
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	if cfg.DefaultPersistence == "" {
		cfg.DefaultPersistence = types.PersistenceModeSession
	}
	return &Adapter{
		svc:                cfg.Service,
		stdout:             cfg.Stdout,
		stderr:             cfg.Stderr,
		defaultPrincipal:   cfg.DefaultPrincipal,
		defaultWorkspace:   cfg.DefaultWorkspace,
		defaultWorkingDir:  cfg.DefaultWorkingDir,
		defaultModel:       cfg.DefaultModel,
		defaultPersistence: cfg.DefaultPersistence,
		defaultStream:      cfg.DefaultStream,
	}, nil
}

func (a *Adapter) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.printUsage()
		return nil
	}
	switch args[0] {
	case "create":
		return a.runCreate(ctx, args[1:])
	case "open":
		return a.runOpen(ctx, args[1:])
	case "continue":
		return a.runContinue(ctx, args[1:])
	case "prompt":
		return a.runPrompt(ctx, args[1:])
	case "snapshot":
		return a.runSnapshot(ctx, args[1:])
	case "set-mode":
		return a.runSetMode(ctx, args[1:])
	case "fork":
		return a.runFork(ctx, args[1:])
	case "clone":
		return a.runClone(ctx, args[1:])
	case "navigate":
		return a.runNavigate(ctx, args[1:])
	case "promote-skill":
		return a.runPromoteSkill(ctx, args[1:])
	case "steer":
		return a.runSteer(ctx, args[1:])
	case "follow-up":
		return a.runFollowUp(ctx, args[1:])
	case "abort":
		return a.runAbort(ctx, args[1:])
	case "help", "--help", "-h":
		a.printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *Adapter) runCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	workingDir := fs.String("dir", a.defaultWorkingDir, "workspace working directory")
	mode := fs.String("mode", "", "session mode")
	modelID := fs.String("model", a.defaultModel.ID, "model id")
	providerID := fs.String("provider", a.defaultModel.ProviderID, "provider id")
	persistence := fs.String("persistence", string(a.defaultPersistence), "persistence mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	snapshot, err := a.svc.CreateSession(ctx, service.CreateSessionRequest{
		Options: runtime.SessionOptions{
			SessionID: *sessionID,
			Scope:     a.scope(*principal, *workspace, *workingDir),
			Mode:      types.ModeRef{ID: *mode},
			Model: types.ModelRef{
				ID:         *modelID,
				ProviderID: *providerID,
			},
			Persistence: types.PersistenceMode(*persistence),
		},
	})
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, false)
	return nil
}

func (a *Adapter) runOpen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	showMessages := fs.Bool("messages", false, "render messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.OpenSession(ctx, ref)
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, *showMessages)
	return nil
}

func (a *Adapter) runContinue(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("continue", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	showMessages := fs.Bool("messages", false, "render messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	snapshot, err := a.svc.ContinueRecent(ctx, service.ContinueRecentRequest{
		Scope: session.Scope{
			Principal: *principal,
			Workspace: *workspace,
		},
	})
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, *showMessages)
	return nil
}

func (a *Adapter) runPrompt(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	stream := fs.Bool("stream", a.defaultStream, "stream assistant output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("prompt text is required")
	}
	var sawDelta bool
	if *stream {
		unsubscribe, err := a.svc.Subscribe(ctx, ref, func(ev events.RuntimeEvent) {
			envelope, ok := ev.(events.Envelope)
			if !ok {
				return
			}
			switch payload := envelope.Payload.(type) {
			case types.MessageDelta:
				if payload.Delta == "" {
					return
				}
				sawDelta = true
				_, _ = io.WriteString(a.stdout, payload.Delta)
			case types.ToolProgress:
				a.renderToolProgress(payload)
			}
		})
		if err == nil && unsubscribe != nil {
			defer unsubscribe()
		}
	}
	result, err := a.svc.Prompt(ctx, ref, types.UserInput{Text: text}, types.PromptOptions{Stream: *stream})
	if err != nil {
		return err
	}
	if *stream && sawDelta {
		_, _ = io.WriteString(a.stdout, "\n")
	} else {
		fmt.Fprintln(a.stdout, messageText(result.FinalOutput))
	}
	a.renderDiagnostics(result.Diagnostics)
	return nil
}

func (a *Adapter) runSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	showMessages := fs.Bool("messages", true, "render messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.Snapshot(ctx, ref)
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, *showMessages)
	return nil
}

func (a *Adapter) runSetMode(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("set-mode", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	mode := fs.String("mode", "", "mode id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mode == "" {
		return errors.New("mode is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.SetMode(ctx, ref, types.ModeRef{ID: *mode})
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, false)
	return nil
}

func (a *Adapter) runFork(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	at := fs.String("at", "", "entry or visible message id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *at == "" {
		return errors.New("at is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.Fork(ctx, ref, types.EntryRef{ID: *at})
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, false)
	return nil
}

func (a *Adapter) runClone(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.Clone(ctx, ref)
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, false)
	return nil
}

func (a *Adapter) runNavigate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("navigate", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	target := fs.String("target", "", "entry or visible message id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("target is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	snapshot, err := a.svc.Navigate(ctx, ref, types.EntryRef{ID: *target})
	if err != nil {
		return err
	}
	a.renderSnapshot(snapshot, false)
	return nil
}

func (a *Adapter) runPromoteSkill(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("promote-skill", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	name := fs.String("name", "", "skill name")
	target := fs.String("target", string(types.SkillPromotionTargetWorkspace), "promotion target")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("name is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	result, err := a.svc.PromoteSkill(ctx, ref, types.SkillPromotionRequest{
		Name:   *name,
		Target: types.SkillPromotionTarget(*target),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "promoted %s to %s at %s\n", result.Name, result.Target, result.Path)
	return nil
}

func (a *Adapter) runSteer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("steer", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("steer text is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	return a.svc.Steer(ctx, ref, types.UserInput{Text: text})
}

func (a *Adapter) runFollowUp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("follow-up", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("follow-up text is required")
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	return a.svc.FollowUp(ctx, ref, types.UserInput{Text: text})
}

func (a *Adapter) runAbort(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("abort", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session", "", "session id")
	principal := fs.String("principal", a.defaultPrincipal, "principal id")
	workspace := fs.String("workspace", a.defaultWorkspace, "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref, err := a.sessionRef(*sessionID, *principal, *workspace)
	if err != nil {
		return err
	}
	return a.svc.Abort(ctx, ref)
}

func (a *Adapter) scope(principal, workspace, workingDir string) types.SessionScope {
	return types.SessionScope{
		Principal: types.PrincipalRef{ID: principal},
		Workspace: types.WorkspaceSpec{
			Ref:        types.WorkspaceRef{ID: workspace},
			Kind:       "local",
			WorkingDir: workingDir,
		},
	}
}

func (a *Adapter) sessionRef(sessionID, principal, workspace string) (types.SessionRef, error) {
	if sessionID == "" {
		return types.SessionRef{}, errors.New("session id is required")
	}
	return types.SessionRef{
		ID:        sessionID,
		Principal: principal,
		Workspace: workspace,
	}, nil
}

func (a *Adapter) renderSnapshot(snapshot types.SessionSnapshot, showMessages bool) {
	fmt.Fprintf(a.stdout, "session: %s\n", snapshot.SessionID)
	fmt.Fprintf(a.stdout, "principal: %s\n", snapshot.Scope.Principal.ID)
	fmt.Fprintf(a.stdout, "workspace: %s\n", snapshot.Scope.Workspace.Ref.ID)
	fmt.Fprintf(a.stdout, "mode: %s\n", snapshot.Mode.ID)
	fmt.Fprintf(a.stdout, "branch: %s\n", snapshot.BranchID)
	fmt.Fprintf(a.stdout, "messages: %d\n", len(snapshot.Messages))
	if !showMessages {
		return
	}
	for _, message := range snapshot.Messages {
		fmt.Fprintf(a.stdout, "[%s] %s\n", message.Role, messageText(message))
	}
}

func (a *Adapter) renderDiagnostics(diags []types.Diagnostic) {
	for _, diag := range diags {
		fmt.Fprintf(a.stderr, "%s: %s\n", diag.Code, diag.Message)
	}
}

func (a *Adapter) renderToolProgress(progress types.ToolProgress) {
	if progress.Stream != "" && progress.Text != "" {
		fmt.Fprintf(a.stderr, "[tool %s %s] %s", progress.CallID, progress.Stream, progress.Text)
		return
	}
	if progress.ExitCode != nil {
		fmt.Fprintf(a.stderr, "[tool %s exit] %d\n", progress.CallID, *progress.ExitCode)
	}
}

func (a *Adapter) printUsage() {
	fmt.Fprintln(a.stdout, "memoidness cli adapter commands:")
	fmt.Fprintln(a.stdout, "  create")
	fmt.Fprintln(a.stdout, "  open")
	fmt.Fprintln(a.stdout, "  continue")
	fmt.Fprintln(a.stdout, "  prompt")
	fmt.Fprintln(a.stdout, "  snapshot")
	fmt.Fprintln(a.stdout, "  set-mode")
	fmt.Fprintln(a.stdout, "  fork")
	fmt.Fprintln(a.stdout, "  clone")
	fmt.Fprintln(a.stdout, "  navigate")
	fmt.Fprintln(a.stdout, "  promote-skill")
	fmt.Fprintln(a.stdout, "  steer")
	fmt.Fprintln(a.stdout, "  follow-up")
	fmt.Fprintln(a.stdout, "  abort")
}

func messageText(message types.Message) string {
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch part.Kind {
		case "text", "summary":
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		case "tool_call":
			if part.Call != nil {
				parts = append(parts, fmt.Sprintf("tool_call:%s", part.Call.Name))
			}
		case "tool_result":
			if part.Result != nil {
				parts = append(parts, fmt.Sprintf("tool_result:%s", part.Result.Status))
			}
		}
	}
	return strings.Join(parts, " ")
}
