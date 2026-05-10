package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/latentarts/memoidness/types"
)

var ErrSessionNotFound = errors.New("session not found")
var ErrUnsupportedOperation = errors.New("unsupported session operation")

type RecordOptions struct {
	SessionID   string
	Scope       types.SessionScope
	Mode        types.ModeRef
	Model       types.ModelRef
	Persistence types.PersistenceMode
}

type Scope struct {
	Principal string
	Workspace string
}

type Summary struct {
	ID        string
	Principal string
	Workspace string
	Mode      types.ModeRef
	Model     types.ModelRef
	UpdatedAt time.Time
}

type Record struct {
	SessionID   string
	Scope       types.SessionScope
	Mode        types.ModeRef
	Model       types.ModelRef
	BranchID    string
	Entries     []types.SessionEntry
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Persistence types.PersistenceMode
}

func (r *Record) Ref() types.SessionRef {
	return types.SessionRef{
		ID:        r.SessionID,
		Principal: r.Scope.Principal.ID,
		Workspace: r.Scope.Workspace.Ref.ID,
	}
}

type Manager interface {
	New(ctx context.Context, opts RecordOptions) (*Record, error)
	Open(ctx context.Context, ref types.SessionRef) (*Record, error)
	ContinueRecent(ctx context.Context, scope Scope) (*Record, error)
	Fork(ctx context.Context, ref types.SessionRef, at types.EntryRef) (*Record, error)
	Clone(ctx context.Context, ref types.SessionRef) (*Record, error)
	Navigate(ctx context.Context, ref types.SessionRef, target types.EntryRef) (*Record, error)
	Append(ctx context.Context, ref types.SessionRef, entry types.SessionEntry) error
	List(ctx context.Context, scope Scope) ([]Summary, error)
}

type InMemoryManager struct {
	mu       sync.RWMutex
	records  map[string]*Record
	recents  map[string]string
	sequence uint64
}

func NewInMemoryManager() *InMemoryManager {
	return &InMemoryManager{
		records: make(map[string]*Record),
		recents: make(map[string]string),
	}
}

func (m *InMemoryManager) New(_ context.Context, opts RecordOptions) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := opts.SessionID
	if id == "" {
		m.sequence++
		id = fmt.Sprintf("session-%d", m.sequence)
	}

	now := time.Now().UTC()
	record := &Record{
		SessionID:   id,
		Scope:       opts.Scope,
		Mode:        opts.Mode,
		Model:       opts.Model,
		BranchID:    "main",
		CreatedAt:   now,
		UpdatedAt:   now,
		Persistence: opts.Persistence,
	}
	m.records[id] = record
	if key := recentKey(opts.Scope); key != "" {
		m.recents[key] = id
	}
	return cloneRecord(record), nil
}

func (m *InMemoryManager) Open(_ context.Context, ref types.SessionRef) (*Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	record, ok := m.records[ref.ID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, ref.ID)
	}
	return cloneRecord(record), nil
}

func (m *InMemoryManager) ContinueRecent(_ context.Context, scope Scope) (*Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	id, ok := m.recents[scopeKey(scope)]
	if !ok {
		return nil, fmt.Errorf("%w: recent session for %s/%s", ErrSessionNotFound, scope.Principal, scope.Workspace)
	}
	record, ok := m.records[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return cloneRecord(record), nil
}

func (m *InMemoryManager) Fork(ctx context.Context, ref types.SessionRef, at types.EntryRef) (*Record, error) {
	return m.branchLike(ctx, ref, "fork", at)
}

func (m *InMemoryManager) Clone(ctx context.Context, ref types.SessionRef) (*Record, error) {
	return m.branchLike(ctx, ref, "clone", types.EntryRef{})
}

func (m *InMemoryManager) Navigate(_ context.Context, ref types.SessionRef, target types.EntryRef) (*Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	record, ok := m.records[ref.ID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, ref.ID)
	}
	navigated, err := cloneThrough(record, target)
	if err != nil {
		return nil, err
	}
	return navigated, nil
}

func (m *InMemoryManager) Append(_ context.Context, ref types.SessionRef, entry types.SessionEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.records[ref.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, ref.ID)
	}

	record.Entries = append(record.Entries, entry)
	record.UpdatedAt = time.Now().UTC()
	if key := recentKey(record.Scope); key != "" {
		m.recents[key] = record.SessionID
	}
	return nil
}

func (m *InMemoryManager) List(_ context.Context, scope Scope) ([]Summary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	summaries := make([]Summary, 0, len(m.records))
	for _, record := range m.records {
		if scope.Principal != "" && record.Scope.Principal.ID != scope.Principal {
			continue
		}
		if scope.Workspace != "" && record.Scope.Workspace.Ref.ID != scope.Workspace {
			continue
		}
		summaries = append(summaries, Summary{
			ID:        record.SessionID,
			Principal: record.Scope.Principal.ID,
			Workspace: record.Scope.Workspace.Ref.ID,
			Mode:      record.Mode,
			Model:     record.Model,
			UpdatedAt: record.UpdatedAt,
		})
	}
	return summaries, nil
}

func (m *InMemoryManager) branchLike(_ context.Context, ref types.SessionRef, prefix string, at types.EntryRef) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.records[ref.ID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, ref.ID)
	}

	m.sequence++
	id := fmt.Sprintf("%s-%d", prefix, m.sequence)
	copyRecord := cloneRecord(record)
	if at.ID != "" {
		navigated, err := cloneThrough(record, at)
		if err != nil {
			return nil, err
		}
		copyRecord = navigated
	}
	copyRecord.SessionID = id
	copyRecord.BranchID = id
	copyRecord.CreatedAt = time.Now().UTC()
	copyRecord.UpdatedAt = copyRecord.CreatedAt
	marker := types.SessionEntry{
		ID:            fmt.Sprintf("branch-%d", m.sequence),
		Kind:          "branch_marker",
		ParentSession: &ref,
		At:            copyRecord.CreatedAt,
	}
	if len(copyRecord.Entries) > 0 {
		marker.ParentID = copyRecord.Entries[len(copyRecord.Entries)-1].ID
	}
	copyRecord.Entries = append([]types.SessionEntry{marker}, copyRecord.Entries...)
	m.records[id] = copyRecord
	if key := recentKey(copyRecord.Scope); key != "" {
		m.recents[key] = id
	}
	return cloneRecord(copyRecord), nil
}

func cloneThrough(record *Record, target types.EntryRef) (*Record, error) {
	cloned := cloneRecord(record)
	if target.ID == "" {
		return cloned, nil
	}
	index := -1
	for i, entry := range record.Entries {
		if entry.ID == target.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: entry %s", ErrSessionNotFound, target.ID)
	}
	cloned.Entries = append([]types.SessionEntry(nil), record.Entries[:index+1]...)
	if at := cloned.Entries[len(cloned.Entries)-1].At; !at.IsZero() {
		cloned.UpdatedAt = at
	}
	return cloned, nil
}

func cloneRecord(record *Record) *Record {
	if record == nil {
		return nil
	}

	entries := make([]types.SessionEntry, len(record.Entries))
	copy(entries, record.Entries)
	return &Record{
		SessionID:   record.SessionID,
		Scope:       record.Scope,
		Mode:        record.Mode,
		Model:       record.Model,
		BranchID:    record.BranchID,
		Entries:     entries,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
		Persistence: record.Persistence,
	}
}

func recentKey(scope types.SessionScope) string {
	return scopeKey(Scope{
		Principal: scope.Principal.ID,
		Workspace: scope.Workspace.Ref.ID,
	})
}

func scopeKey(scope Scope) string {
	if scope.Principal == "" && scope.Workspace == "" {
		return ""
	}
	return scope.Principal + "::" + scope.Workspace
}
