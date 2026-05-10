package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

type Service struct {
	runtime runtime.Runtime
	store   session.Manager

	mu       sync.RWMutex
	sessions map[string]runtime.Session
}

type CreateSessionRequest struct {
	Options runtime.SessionOptions
}

type ContinueRecentRequest struct {
	Scope session.Scope
}

func New(rt runtime.Runtime, store session.Manager) *Service {
	return &Service{
		runtime:  rt,
		store:    store,
		sessions: make(map[string]runtime.Session),
	}
}

func (s *Service) CreateSession(ctx context.Context, req CreateSessionRequest) (types.SessionSnapshot, error) {
	sess, err := s.runtime.NewSession(ctx, req.Options)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	s.remember(sess)
	return sess.Snapshot(), nil
}

func (s *Service) OpenSession(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	return sess.Snapshot(), nil
}

func (s *Service) ContinueRecent(ctx context.Context, req ContinueRecentRequest) (types.SessionSnapshot, error) {
	record, err := s.store.ContinueRecent(ctx, req.Scope)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	return s.OpenSession(ctx, record.Ref())
}

func (s *Service) Prompt(ctx context.Context, ref types.SessionRef, input types.UserInput, opts types.PromptOptions) (types.RunResult, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.RunResult{}, err
	}
	return sess.Prompt(ctx, input, opts)
}

func (s *Service) Steer(ctx context.Context, ref types.SessionRef, input types.UserInput) error {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return err
	}
	return sess.Steer(ctx, input)
}

func (s *Service) FollowUp(ctx context.Context, ref types.SessionRef, input types.UserInput) error {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return err
	}
	return sess.FollowUp(ctx, input)
}

func (s *Service) Abort(ctx context.Context, ref types.SessionRef) error {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return err
	}
	return sess.Abort(ctx)
}

func (s *Service) Fork(ctx context.Context, ref types.SessionRef, at types.EntryRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	forked, err := sess.Fork(ctx, at)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	s.remember(forked)
	return forked.Snapshot(), nil
}

func (s *Service) Clone(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	cloned, err := sess.Clone(ctx)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	s.remember(cloned)
	return cloned.Snapshot(), nil
}

func (s *Service) Navigate(ctx context.Context, ref types.SessionRef, target types.EntryRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	if err := sess.Navigate(ctx, target); err != nil {
		return types.SessionSnapshot{}, err
	}
	return sess.Snapshot(), nil
}

func (s *Service) PromoteSkill(ctx context.Context, ref types.SessionRef, req types.SkillPromotionRequest) (types.SkillPromotionResult, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SkillPromotionResult{}, err
	}
	return sess.PromoteSkill(ctx, req)
}

func (s *Service) Snapshot(ctx context.Context, ref types.SessionRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	return sess.Snapshot(), nil
}

func (s *Service) SetMode(ctx context.Context, ref types.SessionRef, mode types.ModeRef) (types.SessionSnapshot, error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return types.SessionSnapshot{}, err
	}
	if err := sess.SetMode(ctx, mode); err != nil {
		return types.SessionSnapshot{}, err
	}
	return sess.Snapshot(), nil
}

func (s *Service) Subscribe(ctx context.Context, ref types.SessionRef, listener events.Listener) (func(), error) {
	sess, err := s.ensureSession(ctx, ref)
	if err != nil {
		return nil, err
	}
	return sess.Subscribe(listener), nil
}

func (s *Service) ensureSession(ctx context.Context, ref types.SessionRef) (runtime.Session, error) {
	if ref.ID == "" {
		return nil, fmt.Errorf("%w: session id is required", session.ErrSessionNotFound)
	}
	s.mu.RLock()
	if sess, ok := s.sessions[ref.ID]; ok {
		s.mu.RUnlock()
		return sess, nil
	}
	s.mu.RUnlock()

	sess, err := s.runtime.OpenSession(ctx, ref)
	if err != nil {
		return nil, err
	}
	s.remember(sess)
	return sess, nil
}

func (s *Service) remember(sess runtime.Session) {
	snapshot := sess.Snapshot()
	s.mu.Lock()
	s.sessions[snapshot.SessionID] = sess
	s.mu.Unlock()
}
