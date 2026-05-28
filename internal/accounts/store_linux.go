//go:build linux

package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type linuxStore struct {
	path     string
	lockPath string
}

// OpenStore returns the Linux file-backed account store at
// ~/.config/aistat/accounts/claude.json. The parent directory is created with
// mode 0700 if absent.
func OpenStore(opts ...Option) (Store, error) {
	// opts are accepted for API consistency; WithDebug is darwin-only.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("accounts: cannot resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "aistat", "accounts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("accounts: cannot create accounts dir %s: %w", dir, err)
	}
	return &linuxStore{
		path:     filepath.Join(dir, "claude.json"),
		lockPath: filepath.Join(dir, ".claude.lock"),
	}, nil
}

// withLock opens the sentinel lock file (creating it mode 0600 if absent),
// acquires the requested flock mode, calls fn, then releases the lock. The
// lock sits on a separate sentinel file rather than the data file because
// atomicWrite replaces the data file via rename — a flock on the data file's
// open fd would travel with the orphaned inode after rename, letting a second
// writer race ahead with stale state. The sentinel never gets renamed so the
// lock anchors a stable serialization point.
func (s *linuxStore) withLock(mode int, fn func() error) error {
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("accounts: open lock file: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
		return fmt.Errorf("accounts: acquire store lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// readAccountMap reads the current account map from the data file. Returns an
// empty map (not an error) for a missing or empty file; returns an error for
// corrupt JSON so the caller can surface it rather than silently discarding
// stored accounts. Caller must hold the store lock.
func (s *linuxStore) readAccountMap() (map[string]Account, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return make(map[string]Account), nil
		}
		return nil, fmt.Errorf("accounts: read store file: %w", err)
	}
	if len(data) == 0 {
		return make(map[string]Account), nil
	}
	var m map[string]Account
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("accounts: parse store file: %w", err)
	}
	return m, nil
}

// atomicWrite marshals m to JSON and atomically replaces s.path via a
// temp-file-in-same-dir + rename, preserving mode 0600.
func (s *linuxStore) atomicWrite(m map[string]Account) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".claude-*.json.tmp")
	if err != nil {
		return fmt.Errorf("accounts: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	var writeErr error
	if _, writeErr = tmp.Write(data); writeErr == nil {
		writeErr = tmp.Sync()
	}
	tmp.Close()
	if writeErr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("accounts: write temp file: %w", writeErr)
	}
	// os.CreateTemp creates with mode 0600 — no chmod needed.
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("accounts: rename temp file: %w", err)
	}
	return nil
}

func (s *linuxStore) List(ctx context.Context) ([]Account, error) {
	var list []Account
	err := s.withLock(syscall.LOCK_SH, func() error {
		m, err := s.readAccountMap()
		if err != nil {
			return err
		}
		list = make([]Account, 0, len(m))
		for _, a := range m {
			list = append(list, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (s *linuxStore) Upsert(ctx context.Context, a Account) error {
	return s.withLock(syscall.LOCK_EX, func() error {
		m, err := s.readAccountMap()
		if err != nil {
			return err
		}
		m[a.UUID] = a
		return s.atomicWrite(m)
	})
}

func (s *linuxStore) Delete(ctx context.Context, uuid string) error {
	return s.withLock(syscall.LOCK_EX, func() error {
		m, err := s.readAccountMap()
		if err != nil {
			return err
		}
		delete(m, uuid)
		if len(m) == 0 {
			// Remove the data file when the last account is deleted. Leave
			// the lock sentinel in place so subsequent writers serialize
			// against the same anchor.
			if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("accounts: remove empty store file: %w", err)
			}
			return nil
		}
		return s.atomicWrite(m)
	})
}
