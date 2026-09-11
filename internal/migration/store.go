package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrNotFound = errors.New("migration session not found")

type Store interface {
	Get(string) (Session, error)
	Put(Session) error
}

type FileStore struct {
	Dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) *FileStore { return &FileStore{Dir: dir} }

func (s *FileStore) path(id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

func (s *FileStore) Get(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, fmt.Errorf("decode session %s: %w", id, err)
	}
	return session, nil
}

func (s *FileStore) Put(session Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	path, err := s.path(session.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// Durably persist the rename where the filesystem supports directory fsync.
	if dir, err := os.Open(s.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
