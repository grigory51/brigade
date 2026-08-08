//go:build !darwin || !cgo

package desktopenv

import (
	"encoding/json"
	"errors"
	"os"
)

// fileTokenStore нужен только browser-fallback сборкам не на macOS. Brigade.app на macOS
// всегда использует системный Keychain.
type fileTokenStore struct{ path string }

func newTokenStore(path string) tokenStore { return &fileTokenStore{path: path + ".tokens"} }

func (s *fileTokenStore) read() (map[string]string, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, err
	}
	return values, nil
}
func (s *fileTokenStore) Get(id string) (string, error) {
	values, err := s.read()
	return values[id], err
}
func (s *fileTokenStore) Set(id, token string) error {
	values, err := s.read()
	if err != nil {
		return err
	}
	if token == "" {
		delete(values, id)
	} else {
		values[id] = token
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}
func (s *fileTokenStore) Delete(id string) error { return s.Set(id, "") }
