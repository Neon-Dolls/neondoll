package extension

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrStateNotFound = errors.New("extension: state not found")

type StateStore interface {
	Get(context.Context, string, string) ([]byte, error)
	Put(context.Context, string, string, []byte) error
}

type FileStateStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStateStore(root string) *FileStateStore { return &FileStateStore{root: root} }

func (s *FileStateStore) path(instance string) string { return filepath.Join(s.root, instance+".json") }

func (s *FileStateStore) load(instance string) (map[string][]byte, error) {
	data, err := os.ReadFile(s.path(instance))
	if os.IsNotExist(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string][]byte{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}
func (s *FileStateStore) Get(_ context.Context, instance, key string) ([]byte, error) {
	if strings.TrimSpace(instance) == "" || strings.TrimSpace(key) == "" {
		return nil, ErrStateNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load(instance)
	if err != nil {
		return nil, err
	}
	value, ok := values[key]
	if !ok {
		return nil, ErrStateNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s *FileStateStore) Put(_ context.Context, instance, key string, value []byte) error {
	if strings.TrimSpace(instance) == "" || strings.TrimSpace(key) == "" {
		return ErrStateNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load(instance)
	if err != nil {
		return err
	}
	values[key] = append([]byte(nil), value...)
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, "state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path(instance))
}
