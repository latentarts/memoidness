package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

var ErrInvalidConfig = errors.New("invalid rpc adapter config")

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
	Service serviceAPI
}

type Server struct {
	svc serviceAPI
}

type RequestEnvelope struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type ResponseEnvelope struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Method  string `json:"method,omitempty"`
	Payload any    `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
}

type createSessionParams struct {
	SessionID   string                `json:"session_id"`
	Scope       types.SessionScope    `json:"scope"`
	Mode        types.ModeRef         `json:"mode"`
	Model       types.ModelRef        `json:"model"`
	Persistence types.PersistenceMode `json:"persistence"`
}

type sessionRefParams struct {
	Ref types.SessionRef `json:"ref"`
}

type continueParams struct {
	Principal string `json:"principal"`
	Workspace string `json:"workspace"`
}

type promptParams struct {
	Ref   types.SessionRef    `json:"ref"`
	Input types.UserInput     `json:"input"`
	Opts  types.PromptOptions `json:"options"`
}

type textInputParams struct {
	Ref   types.SessionRef `json:"ref"`
	Input types.UserInput  `json:"input"`
}

type setModeParams struct {
	Ref  types.SessionRef `json:"ref"`
	Mode types.ModeRef    `json:"mode"`
}

type forkParams struct {
	Ref types.SessionRef `json:"ref"`
	At  types.EntryRef   `json:"at"`
}

type navigateParams struct {
	Ref    types.SessionRef `json:"ref"`
	Target types.EntryRef   `json:"target"`
}

type promoteSkillParams struct {
	Ref    types.SessionRef           `json:"ref"`
	Name   string                     `json:"name"`
	Target types.SkillPromotionTarget `json:"target"`
}

type subscribeParams struct {
	Ref types.SessionRef `json:"ref"`
}

func New(cfg Config) (*Server, error) {
	if cfg.Service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalidConfig)
	}
	return &Server{svc: cfg.Service}, nil
}

func (s *Server) Serve(ctx context.Context, rw io.ReadWriter) error {
	var mu sync.Mutex
	write := func(env ResponseEnvelope) error {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewEncoder(rw).Encode(env); err != nil {
			return err
		}
		return nil
	}

	scanner := bufio.NewScanner(rw)
	defer func() {
		// no-op cleanup hook to keep defer near scanner setup if stream resources expand later
	}()
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req RequestEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if writeErr := write(ResponseEnvelope{Type: "error", Error: err.Error()}); writeErr != nil {
				return writeErr
			}
			continue
		}
		if req.Method == "" {
			if err := write(ResponseEnvelope{ID: req.ID, Type: "error", Error: "method is required"}); err != nil {
				return err
			}
			continue
		}
		if err := s.handleRequest(ctx, req, write); err != nil {
			if writeErr := write(ResponseEnvelope{ID: req.ID, Type: "error", Method: req.Method, Error: err.Error()}); writeErr != nil {
				return writeErr
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Server) handleRequest(ctx context.Context, req RequestEnvelope, write func(ResponseEnvelope) error) error {
	switch req.Method {
	case "create_session":
		var params createSessionParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.CreateSession(ctx, service.CreateSessionRequest{
			Options: runtime.SessionOptions{
				SessionID:   params.SessionID,
				Scope:       params.Scope,
				Mode:        params.Mode,
				Model:       params.Model,
				Persistence: params.Persistence,
			},
		})
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "open_session":
		var params sessionRefParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.OpenSession(ctx, params.Ref)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "continue_session":
		var params continueParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.ContinueRecent(ctx, service.ContinueRecentRequest{
			Scope: session.Scope{
				Principal: params.Principal,
				Workspace: params.Workspace,
			},
		})
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "prompt":
		var params promptParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		result, err := s.svc.Prompt(ctx, params.Ref, params.Input, params.Opts)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: result})
	case "snapshot":
		var params sessionRefParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.Snapshot(ctx, params.Ref)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "set_mode":
		var params setModeParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.SetMode(ctx, params.Ref, params.Mode)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "fork":
		var params forkParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.Fork(ctx, params.Ref, params.At)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "clone":
		var params sessionRefParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.Clone(ctx, params.Ref)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "navigate":
		var params navigateParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		snapshot, err := s.svc.Navigate(ctx, params.Ref, params.Target)
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: snapshot})
	case "promote_skill":
		var params promoteSkillParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		result, err := s.svc.PromoteSkill(ctx, params.Ref, types.SkillPromotionRequest{
			Name:   params.Name,
			Target: params.Target,
		})
		if err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: result})
	case "steer":
		var params textInputParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		if err := s.svc.Steer(ctx, params.Ref, params.Input); err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: map[string]any{"ok": true}})
	case "follow_up":
		var params textInputParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		if err := s.svc.FollowUp(ctx, params.Ref, params.Input); err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: map[string]any{"ok": true}})
	case "abort":
		var params sessionRefParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		if err := s.svc.Abort(ctx, params.Ref); err != nil {
			return err
		}
		return write(ResponseEnvelope{ID: req.ID, Type: "response", Method: req.Method, Payload: map[string]any{"ok": true}})
	case "subscribe":
		var params subscribeParams
		if err := decodeParams(req.Params, &params); err != nil {
			return err
		}
		unsubscribe, err := s.svc.Subscribe(ctx, params.Ref, func(ev events.RuntimeEvent) {
			_ = write(ResponseEnvelope{
				ID:      req.ID,
				Type:    "event",
				Method:  req.Method,
				Payload: ev,
			})
		})
		if err != nil {
			return err
		}
		// Keep the subscription alive for the stream lifetime.
		go func() {
			<-ctx.Done()
			unsubscribe()
		}()
		return write(ResponseEnvelope{
			ID:      req.ID,
			Type:    "response",
			Method:  req.Method,
			Payload: map[string]any{"subscribed": true},
		})
	default:
		return fmt.Errorf("unknown method %q", req.Method)
	}
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return errors.New("params are required")
	}
	return json.Unmarshal(raw, dst)
}
