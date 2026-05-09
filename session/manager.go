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
	WorkingDir  string
	Model       types.ModelRef
	Persistence types.PersistenceMode
}

type Scope struct {
	WorkingDir string
}

type Summary struct {
	ID        string
	Model     types.ModelRef
	UpdatedAt time.Time
}

type Record struct {
	SessionID   string
	WorkingDir  string
	Model       types.ModelRef
	BranchID    string
	Entries     []types.SessionEntry
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Persistence types.PersistenceMode
}

func (r *Record) Ref() types.SessionRef {
	return types.SessionRef{ID: r.SessionID}
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
		WorkingDir:  opts.WorkingDir,
		Model:       opts.Model,
		BranchID:    "main",
		CreatedAt:   now,
		UpdatedAt:   now,
		Persistence: opts.Persistence,
	}
	m.records[id] = record
	if opts.WorkingDir != "" {
		m.recents[opts.WorkingDir] = id
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

	id, ok := m.recents[scope.WorkingDir]
	if !ok {
		return nil, fmt.Errorf("%w: recent session for %s", ErrSessionNotFound, scope.WorkingDir)
	}
	record, ok := m.records[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return cloneRecord(record), nil
}

func (m *InMemoryManager) Fork(ctx context.Context, ref types.SessionRef, _ types.EntryRef) (*Record, error) {
	return m.cloneLike(ctx, ref, "fork")
}

func (m *InMemoryManager) Clone(ctx context.Context, ref types.SessionRef) (*Record, error) {
	return m.cloneLike(ctx, ref, "clone")
}

func (m *InMemoryManager) Navigate(ctx context.Context, ref types.SessionRef, _ types.EntryRef) (*Record, error) {
	return m.Open(ctx, ref)
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
	if record.WorkingDir != "" {
		m.recents[record.WorkingDir] = record.SessionID
	}
	return nil
}

func (m *InMemoryManager) List(_ context.Context, scope Scope) ([]Summary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	summaries := make([]Summary, 0, len(m.records))
	for _, record := range m.records {
		if scope.WorkingDir != "" && record.WorkingDir != scope.WorkingDir {
			continue
		}
		summaries = append(summaries, Summary{
			ID:        record.SessionID,
			Model:     record.Model,
			UpdatedAt: record.UpdatedAt,
		})
	}
	return summaries, nil
}

func (m *InMemoryManager) cloneLike(_ context.Context, ref types.SessionRef, prefix string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.records[ref.ID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, ref.ID)
	}

	m.sequence++
	id := fmt.Sprintf("%s-%d", prefix, m.sequence)
	copyRecord := cloneRecord(record)
	copyRecord.SessionID = id
	copyRecord.BranchID = id
	copyRecord.CreatedAt = time.Now().UTC()
	copyRecord.UpdatedAt = copyRecord.CreatedAt
	m.records[id] = copyRecord
	if copyRecord.WorkingDir != "" {
		m.recents[copyRecord.WorkingDir] = id
	}
	return cloneRecord(copyRecord), nil
}

func cloneRecord(record *Record) *Record {
	if record == nil {
		return nil
	}

	entries := make([]types.SessionEntry, len(record.Entries))
	copy(entries, record.Entries)
	return &Record{
		SessionID:   record.SessionID,
		WorkingDir:  record.WorkingDir,
		Model:       record.Model,
		BranchID:    record.BranchID,
		Entries:     entries,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
		Persistence: record.Persistence,
	}
}
