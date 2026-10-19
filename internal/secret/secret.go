// SPDX-License-Identifier: GPL-3.0-only
package secret

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	keyring "github.com/zalando/go-keyring"
)

type Store interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}
type Keyring struct{ Namespace string }

func (k Keyring) Get(id string) (string, error) {
	v, err := keyring.Get("org.olegiv.my-code-dashboard."+k.Namespace, id)
	if err != nil {
		return "", errors.New("credential unavailable: unlock your keychain or replace the connection token")
	}
	return v, nil
}
func (k Keyring) Set(id, value string) error {
	if err := keyring.Set("org.olegiv.my-code-dashboard."+k.Namespace, id, value); err != nil {
		return errors.New("could not save credential in the system keychain; no plaintext fallback was used")
	}
	return nil
}
func (k Keyring) Delete(id string) error {
	err := keyring.Delete("org.olegiv.my-code-dashboard."+k.Namespace, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Files is a headless backend for files supplied by systemd LoadCredential.
// Credential provisioning is deliberately outside the browser application.
type Files struct{ Directory string }

func (f Files) Get(id string) (string, error) {
	if filepath.Base(id) != id || id == "." {
		return "", errors.New("invalid credential identifier")
	}
	path := filepath.Join(f.Directory, id)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file is missing, non-regular, or accessible to other users")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read provisioned credential")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return "", errors.New("cannot read provisioned credential")
	}
	if len(data) > 16384 {
		return "", errors.New("credential is too large")
	}
	return strings.TrimSpace(string(data)), nil
}
func (f Files) Set(string, string) error {
	return errors.New("headless credentials must be provisioned as private files outside the app")
}
func (f Files) Delete(string) error {
	return errors.New("remove the provisioned credential file outside the app")
}

// Memory is used by synthetic demo mode and test fixtures only.
type Memory struct {
	mu     sync.Mutex
	values map[string]string
}

func NewMemory() *Memory { return &Memory{values: map[string]string{}} }
func (m *Memory) Get(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[id]
	if !ok {
		return "", errors.New("credential not found")
	}
	return v, nil
}
func (m *Memory) Set(id, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[id] = v
	return nil
}
func (m *Memory) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, id)
	return nil
}
