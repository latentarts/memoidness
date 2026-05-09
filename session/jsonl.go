package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/latentarts/memoidness/types"
)

var ErrMalformedSession = errors.New("malformed session data")

type JSONLManager struct {
	root string
	mu   sync.Mutex
}

type sessionLine struct {
	Kind        string              `json:"kind"`
	SessionID   string              `json:"session_id,omitempty"`
	WorkingDir  string              `json:"working_dir,omitempty"`
	Model       *types.ModelRef     `json:"model,omitempty"`
	BranchID    string              `json:"branch_id,omitempty"`
	Persistence types.PersistenceMode `json:"persistence,omitempty"`
	CreatedAt   time.Time           `json:"created_at,omitempty"`
	UpdatedAt   time.Time           `json:"updated_at,omitempty"`
	Entry       *types.SessionEntry `json:"entry,omitempty"`
}

func NewJSONLManager(root string) (*JSONLManager, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: session root is required", ErrMalformedSession)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &JSONLManager{root: root}, nil
}

func (m *JSONLManager) New(ctx context.Context, opts RecordOptions) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := opts.SessionID
	if id == "" {
		id = fmt.Sprintf("session-%d", time.Now().UTC().UnixNano())
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.writeHeader(record); err != nil {
		return nil, err
	}
	return cloneRecord(record), nil
}

func (m *JSONLManager) Open(ctx context.Context, ref types.SessionRef) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.readRecord(ref.ID)
}

func (m *JSONLManager) ContinueRecent(ctx context.Context, scope Scope) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	summaries, err := m.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, fmt.Errorf("%w: recent session for %s", ErrSessionNotFound, scope.WorkingDir)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return m.readRecord(summaries[0].ID)
}

func (m *JSONLManager) Fork(context.Context, types.SessionRef, types.EntryRef) (*Record, error) {
	return nil, ErrUnsupportedOperation
}

func (m *JSONLManager) Clone(context.Context, types.SessionRef) (*Record, error) {
	return nil, ErrUnsupportedOperation
}

func (m *JSONLManager) Navigate(context.Context, types.SessionRef, types.EntryRef) (*Record, error) {
	return nil, ErrUnsupportedOperation
}

func (m *JSONLManager) Append(ctx context.Context, ref types.SessionRef, entry types.SessionEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	record, err := m.readRecordLocked(ref.ID)
	if err != nil {
		return err
	}
	record.Entries = append(record.Entries, entry)
	record.UpdatedAt = time.Now().UTC()

	file, err := os.OpenFile(m.sessionPath(ref.ID), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	line := sessionLine{
		Kind:      "entry",
		SessionID: ref.ID,
		UpdatedAt: record.UpdatedAt,
		Entry:     &entry,
	}
	if err := writeJSONL(file, line); err != nil {
		return err
	}
	return nil
}

func (m *JSONLManager) List(ctx context.Context, scope Scope) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		record, err := m.readRecord(strings.TrimSuffix(entry.Name(), ".jsonl"))
		if err != nil {
			return nil, err
		}
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

func (m *JSONLManager) writeHeader(record *Record) error {
	file, err := os.OpenFile(m.sessionPath(record.SessionID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	return writeJSONL(file, sessionLine{
		Kind:        "session",
		SessionID:   record.SessionID,
		WorkingDir:  record.WorkingDir,
		Model:       &record.Model,
		BranchID:    record.BranchID,
		Persistence: record.Persistence,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	})
}

func (m *JSONLManager) readRecord(id string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readRecordLocked(id)
}

func (m *JSONLManager) readRecordLocked(id string) (*Record, error) {
	file, err := os.Open(m.sessionPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var record *Record
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(strings.TrimSpace(string(line))) > 0 {
			var decoded sessionLine
			if decodeErr := json.Unmarshal(line, &decoded); decodeErr != nil {
				return nil, fmt.Errorf("%w: %s", ErrMalformedSession, decodeErr)
			}
			switch decoded.Kind {
			case "session":
				if decoded.Model == nil {
					return nil, fmt.Errorf("%w: missing session model", ErrMalformedSession)
				}
				record = &Record{
					SessionID:   decoded.SessionID,
					WorkingDir:  decoded.WorkingDir,
					Model:       *decoded.Model,
					BranchID:    decoded.BranchID,
					CreatedAt:   decoded.CreatedAt,
					UpdatedAt:   decoded.UpdatedAt,
					Persistence: decoded.Persistence,
				}
			case "entry":
				if record == nil || decoded.Entry == nil {
					return nil, fmt.Errorf("%w: entry before session header", ErrMalformedSession)
				}
				record.Entries = append(record.Entries, *decoded.Entry)
				if !decoded.UpdatedAt.IsZero() {
					record.UpdatedAt = decoded.UpdatedAt
				} else if decoded.Entry.At.After(record.UpdatedAt) {
					record.UpdatedAt = decoded.Entry.At
				}
			default:
				return nil, fmt.Errorf("%w: unknown line kind %q", ErrMalformedSession, decoded.Kind)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if record == nil {
		return nil, fmt.Errorf("%w: missing session header", ErrMalformedSession)
	}
	return cloneRecord(record), nil
}

func (m *JSONLManager) sessionPath(id string) string {
	return filepath.Join(m.root, id+".jsonl")
}

func writeJSONL(w io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}
