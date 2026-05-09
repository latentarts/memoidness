package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/types"
)

var ErrProviderNotFound = errors.New("provider not found")

type Provider interface {
	ID() string
	SupportsStreaming() bool
	Execute(ctx context.Context, req types.ModelRequest) (types.ModelResponse, error)
	StreamCompletion(ctx context.Context, req types.ModelRequest, sink events.Sink) (types.ModelResponse, error)
}

type Registry interface {
	Get(id string) (Provider, error)
	Default() (Provider, error)
}

type StaticRegistry struct {
	providers map[string]Provider
	defaultID string
}

func NewStaticRegistry(defaultID string, providers ...Provider) *StaticRegistry {
	index := make(map[string]Provider, len(providers))
	for _, p := range providers {
		index[p.ID()] = p
	}
	return &StaticRegistry{providers: index, defaultID: defaultID}
}

func (r *StaticRegistry) Get(id string) (Provider, error) {
	provider, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return provider, nil
}

func (r *StaticRegistry) Default() (Provider, error) {
	if r.defaultID != "" {
		return r.Get(r.defaultID)
	}
	if len(r.providers) == 1 {
		for _, p := range r.providers {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: default provider is not configured", ErrProviderNotFound)
}
