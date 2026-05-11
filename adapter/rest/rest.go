package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

var ErrInvalidConfig = errors.New("invalid rest adapter config")

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
	Service  serviceAPI
	BasePath string
}

type Adapter struct {
	svc      serviceAPI
	basePath string
}

type createSessionRequest struct {
	SessionID   string                `json:"session_id"`
	Scope       types.SessionScope    `json:"scope"`
	Mode        types.ModeRef         `json:"mode"`
	Model       types.ModelRef        `json:"model"`
	Persistence types.PersistenceMode `json:"persistence"`
}

type continueRequest struct {
	Principal string `json:"principal"`
	Workspace string `json:"workspace"`
}

type promptRequest struct {
	Input  types.UserInput      `json:"input"`
	Stream bool                 `json:"stream"`
	Opts   *types.PromptOptions `json:"options,omitempty"`
}

type textInputRequest struct {
	Input types.UserInput `json:"input"`
}

type setModeRequest struct {
	Mode types.ModeRef `json:"mode"`
}

type forkRequest struct {
	At types.EntryRef `json:"at"`
}

type navigateRequest struct {
	Target types.EntryRef `json:"target"`
}

type promoteSkillRequest struct {
	Name   string                     `json:"name"`
	Target types.SkillPromotionTarget `json:"target"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func New(cfg Config) (*Adapter, error) {
	if cfg.Service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalidConfig)
	}
	basePath := strings.TrimSpace(cfg.BasePath)
	if basePath == "" {
		basePath = "/"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		basePath = "/"
	}
	return &Adapter{
		svc:      cfg.Service,
		basePath: basePath,
	}, nil
}

func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(a.ServeHTTP)
}

func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := a.relativePath(r.URL.Path)
	switch {
	case path == "/sessions" && r.Method == http.MethodPost:
		a.handleCreateSession(w, r)
	case path == "/sessions/continue" && r.Method == http.MethodPost:
		a.handleContinue(w, r)
	case strings.HasPrefix(path, "/sessions/"):
		a.handleSessionRoute(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

func (a *Adapter) handleSessionRoute(w http.ResponseWriter, r *http.Request, path string) {
	trimmed := strings.TrimPrefix(path, "/sessions/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	sessionID := parts[0]
	ref := types.SessionRef{
		ID:        sessionID,
		Principal: r.URL.Query().Get("principal"),
		Workspace: r.URL.Query().Get("workspace"),
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleSnapshot(w, r, ref)
		return
	}

	switch parts[1] {
	case "prompt":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handlePrompt(w, r, ref)
	case "steer":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleTextInput(w, r, ref, func(ctx context.Context, input types.UserInput) error {
			return a.svc.Steer(ctx, ref, input)
		})
	case "follow-up":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleTextInput(w, r, ref, func(ctx context.Context, input types.UserInput) error {
			return a.svc.FollowUp(ctx, ref, input)
		})
	case "abort":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleAbort(w, r, ref)
	case "mode":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleSetMode(w, r, ref)
	case "fork":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleFork(w, r, ref)
	case "clone":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleClone(w, r, ref)
	case "navigate":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleNavigate(w, r, ref)
	case "promote-skill":
		if r.Method != http.MethodPost {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handlePromoteSkill(w, r, ref)
	case "events":
		if r.Method != http.MethodGet {
			a.writeMethodNotAllowed(w)
			return
		}
		a.handleEvents(w, r, ref)
	default:
		http.NotFound(w, r)
	}
}

func (a *Adapter) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	snapshot, err := a.svc.CreateSession(r.Context(), service.CreateSessionRequest{
		Options: runtime.SessionOptions{
			SessionID:   req.SessionID,
			Scope:       req.Scope,
			Mode:        req.Mode,
			Model:       req.Model,
			Persistence: req.Persistence,
		},
	})
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, snapshot)
}

func (a *Adapter) handleContinue(w http.ResponseWriter, r *http.Request) {
	var req continueRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	snapshot, err := a.svc.ContinueRecent(r.Context(), service.ContinueRecentRequest{
		Scope: session.Scope{
			Principal: req.Principal,
			Workspace: req.Workspace,
		},
	})
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handleSnapshot(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	snapshot, err := a.svc.Snapshot(r.Context(), ref)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handlePrompt(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	var req promptRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	opts := types.PromptOptions{Stream: req.Stream}
	if req.Opts != nil {
		opts = *req.Opts
	}
	result, err := a.svc.Prompt(r.Context(), ref, req.Input, opts)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, result)
}

func (a *Adapter) handleTextInput(w http.ResponseWriter, r *http.Request, ref types.SessionRef, fn func(context.Context, types.UserInput) error) {
	var req textInputRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	if err := fn(r.Context(), req.Input); err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Adapter) handleAbort(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	if err := a.svc.Abort(r.Context(), ref); err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Adapter) handleSetMode(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	var req setModeRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	snapshot, err := a.svc.SetMode(r.Context(), ref, req.Mode)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handleFork(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	var req forkRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	snapshot, err := a.svc.Fork(r.Context(), ref, req.At)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handleClone(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	snapshot, err := a.svc.Clone(r.Context(), ref)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handleNavigate(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	var req navigateRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	snapshot, err := a.svc.Navigate(r.Context(), ref, req.Target)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, snapshot)
}

func (a *Adapter) handlePromoteSkill(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	var req promoteSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeBadRequest(w, err)
		return
	}
	result, err := a.svc.PromoteSkill(r.Context(), ref, types.SkillPromotionRequest{
		Name:   req.Name,
		Target: req.Target,
	})
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	a.writeJSON(w, http.StatusOK, result)
}

func (a *Adapter) handleEvents(w http.ResponseWriter, r *http.Request, ref types.SessionRef) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.writeError(w, http.StatusInternalServerError, errors.New("response writer does not support flushing"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	unsubscribe, err := a.svc.Subscribe(r.Context(), ref, func(ev events.RuntimeEvent) {
		encoded, marshalErr := json.Marshal(ev)
		if marshalErr != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flusher.Flush()
	})
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err)
		return
	}
	defer unsubscribe()

	flusher.Flush()
	<-r.Context().Done()
}

func (a *Adapter) relativePath(path string) string {
	if a.basePath == "/" {
		if path == "" {
			return "/"
		}
		return path
	}
	if path == a.basePath {
		return "/"
	}
	if strings.HasPrefix(path, a.basePath+"/") {
		return strings.TrimPrefix(path, a.basePath)
	}
	return path
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func (a *Adapter) writeBadRequest(w http.ResponseWriter, err error) {
	a.writeError(w, http.StatusBadRequest, err)
}

func (a *Adapter) writeMethodNotAllowed(w http.ResponseWriter) {
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (a *Adapter) writeError(w http.ResponseWriter, status int, err error) {
	a.writeJSON(w, status, errorResponse{Error: err.Error()})
}

func (a *Adapter) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
