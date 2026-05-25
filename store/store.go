// File-backed JSON store — mirrors TinyDB behaviour.
// All records live in a single JSON file per environment.
package store

import (
	"encoding/json"
	"os"
	"sync"
)

const (
	TableCredentials       = "credentials"
	TableSources           = "sources"
	TableDestinations      = "destinations"
	TableBackups           = "backups"
	TableRestores          = "restores"
	TableCronJobs          = "cronjobs"
	TableRetentionPolicies = "retention_policies"
	TableNotifications     = "notifications"
	TableUsers             = "users"
	TableRestoreTargets    = "restore_targets"
	TableSettings          = "settings"
)

type Store struct {
	mu   sync.RWMutex
	data map[string]map[string]json.RawMessage // table → id → raw JSON
	path string
}

func New(path string) (*Store, error) {
	if err := os.MkdirAll(filepath_dir(path), 0755); err != nil {
		return nil, err
	}
	s := &Store{
		data: make(map[string]map[string]json.RawMessage),
		path: path,
	}
	return s, s.load()
}

func filepath_dir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &s.data)
}

func (s *Store) flush() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0644)
}

// Upsert inserts or replaces a record by id.
func (s *Store) Upsert(table, id string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[table] == nil {
		s.data[table] = make(map[string]json.RawMessage)
	}
	s.data[table][id] = raw
	return s.flush()
}

// GetAll decodes every record in a table into a slice.
// result must be a pointer to a slice (e.g. *[]MyType).
func (s *Store) GetAll(table string, result any) error {
	s.mu.RLock()
	rows := make([]json.RawMessage, 0, len(s.data[table]))
	for _, v := range s.data[table] {
		rows = append(rows, v)
	}
	s.mu.RUnlock()

	arr, _ := json.Marshal(rows)
	return json.Unmarshal(arr, result)
}

// GetByID decodes one record; returns false if not found.
func (s *Store) GetByID(table, id string, result any) (bool, error) {
	s.mu.RLock()
	raw, ok := s.data[table][id]
	s.mu.RUnlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, result)
}

// Exists returns whether a record with the given id exists.
func (s *Store) Exists(table, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[table][id]
	return ok
}

// Delete removes a record.
func (s *Store) Delete(table, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[table], id)
	return s.flush()
}

// RawExport returns a copy of the requested tables as raw JSON maps.
func (s *Store) RawExport(tables []string) map[string]map[string]json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]json.RawMessage)
	for _, t := range tables {
		if s.data[t] == nil {
			continue
		}
		tbl := make(map[string]json.RawMessage, len(s.data[t]))
		for k, v := range s.data[t] {
			tbl[k] = v
		}
		out[t] = tbl
	}
	return out
}

// ReplaceTable atomically replaces all records in a table.
func (s *Store) ReplaceTable(table string, data map[string]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[table] = data
	return s.flush()
}
