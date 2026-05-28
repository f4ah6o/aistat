//go:build darwin

package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// safeWriter wraps an io.Writer with a mutex. Mirrors httpx.ConcurrencySafeWriter.
// Needed because the orphan-warn path in List may be called from goroutines
// exercising the store concurrently (e.g. parallel test cases), allowing
// callers to pass a plain *bytes.Buffer to WithDebug without a data race.
type safeWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *safeWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

const (
	darwinServicePrefix = "aistat:accounts:claude:"
	darwinIndexService  = "aistat:accounts:claude:index"
	darwinIndexAccount  = "index"
)

type darwinStore struct {
	lockPath string
	debug    *safeWriter // nil when debug is disabled
}

// OpenStore returns the macOS Keychain-backed account store. The lock file
// sentinel is created (lazily, mode 0600) at os.UserCacheDir()/aistat/store.lock;
// its parent directory is created with mode 0700 if absent.
func OpenStore(opts ...Option) (Store, error) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("accounts: cannot resolve user cache dir: %w", err)
	}
	lockDir := filepath.Join(cacheDir, "aistat")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("accounts: cannot create lock dir %s: %w", lockDir, err)
	}
	s := &darwinStore{lockPath: filepath.Join(lockDir, "store.lock")}
	if cfg.debug != nil {
		s.debug = &safeWriter{w: cfg.debug}
	}
	return s, nil
}

// withLock acquires an exclusive process-level flock on the sentinel lock file,
// calls fn, then releases it. The lock serializes concurrent aistat processes
// across the read-index → read-items → mutate → write-items → write-index
// critical section, preventing lost-index races. Each call to withLock opens
// the file anew (new open file description), so goroutines in the same process
// also serialize correctly.
func (s *darwinStore) withLock(fn func() error) error {
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("accounts: open lock file %s: %w", s.lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("accounts: acquire store lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// readIndex reads the list of UUIDs from the index keychain item.
// Returns a nil slice (not an error) if the index item does not exist.
func (s *darwinStore) readIndex(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", darwinIndexService, "-a", darwinIndexAccount, "-w")
	out, err := cmd.Output()
	if err != nil {
		if darwinIsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("accounts: read index: %w", darwinKeychainErr(err))
	}
	data := strings.TrimSpace(string(out))
	if data == "" {
		return nil, nil
	}
	var idx struct {
		UUIDs []string `json:"uuids"`
	}
	if err := json.Unmarshal([]byte(data), &idx); err != nil {
		return nil, fmt.Errorf("accounts: parse index: %w", err)
	}
	return idx.UUIDs, nil
}

// writeIndex persists the UUID list as the index keychain item.
// An empty slice deletes the index item entirely (clean state after final remove).
func (s *darwinStore) writeIndex(ctx context.Context, uuids []string) error {
	if len(uuids) == 0 {
		return darwinDeleteItem(ctx, darwinIndexService, darwinIndexAccount)
	}
	data, err := json.Marshal(struct {
		UUIDs []string `json:"uuids"`
	}{UUIDs: uuids})
	if err != nil {
		return err
	}
	return darwinWriteItem(ctx, darwinIndexService, darwinIndexAccount, string(data))
}

// darwinReadAccountItem reads the Account JSON for the given UUID.
// Returns nil, nil if not found. Looks up by service only (no account filter)
// so that identity-drift email changes (D9) do not break the read path.
func darwinReadAccountItem(ctx context.Context, uuid string) ([]byte, error) {
	svc := darwinServicePrefix + uuid
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", svc, "-w")
	out, err := cmd.Output()
	if err != nil {
		if darwinIsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("accounts: read account %s: %w", uuid, darwinKeychainErr(err))
	}
	data := strings.TrimSpace(string(out))
	if data == "" {
		return nil, nil
	}
	return []byte(data), nil
}

// upsertAccountItem writes the Account JSON as the per-account keychain item.
// See darwinWriteItem below for the `-U` upsert semantics. The explicit
// pre-delete here is NOT redundant after `-U`: it is required for D9 identity
// drift, where a stored UUID's email may change. `-U` matches by (service,
// account), so a changed email would miss the existing row and silently
// insert a duplicate. The pre-delete by service only removes the old row first.
//
// Failure mode: if the pre-delete succeeds but the subsequent add fails, the
// per-account item is gone while the UUID may still be in the index. List
// will surface this as an orphan-in-index warn (aistat: orphan account
// index entry <uuid>). The next Upsert call recreates the item cleanly.
func upsertAccountItem(ctx context.Context, a Account) error {
	svc := darwinServicePrefix + a.UUID
	// darwinDeleteItem returns nil for "not found", so this is always safe on
	// first-time upsert. Propagate any other error (e.g. permission denied).
	if err := darwinDeleteItem(ctx, svc, ""); err != nil {
		return fmt.Errorf("accounts: pre-upsert delete of %s: %w", a.UUID, err)
	}
	return darwinWriteItem(ctx, svc, a.Email, string(mustMarshal(a)))
}

// darwinWriteItem calls `security add-generic-password -U`, which creates the
// item if it does not exist and updates the value in place if it does. The -U
// flag matches by (service, account); callers that need to handle a change to
// either of those uniqueness fields must delete-then-add explicitly (see
// upsertAccountItem for the D9 identity-drift case where email may change).
//
// Without -U the call would exit 45 ("item already exists") on every write
// after the first to the same (service, account) — the historical bug that
// silently dropped index updates after the first Upsert.
func darwinWriteItem(ctx context.Context, service, account, value string) error {
	cmd := exec.CommandContext(ctx, "security", "add-generic-password",
		"-U", "-s", service, "-a", account, "-w", value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain write %s/%s: %s", service, account,
			strings.TrimSpace(string(out)))
	}
	return nil
}

// darwinDeleteItem calls `security delete-generic-password`. Returns nil if
// the item is not found. When account is empty, the lookup is by service only.
func darwinDeleteItem(ctx context.Context, service, account string) error {
	args := []string{"delete-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	cmd := exec.CommandContext(ctx, "security", args...)
	if _, err := cmd.Output(); err != nil {
		if darwinIsNotFound(err) {
			return nil
		}
		return fmt.Errorf("keychain delete %s: %w", service, darwinKeychainErr(err))
	}
	return nil
}

func (s *darwinStore) List(ctx context.Context) ([]Account, error) {
	var accounts []Account
	err := s.withLock(func() error {
		uuids, err := s.readIndex(ctx)
		if err != nil {
			return err
		}
		for _, uuid := range uuids {
			data, err := darwinReadAccountItem(ctx, uuid)
			if err != nil {
				return err
			}
			if data == nil {
				// Orphan: index entry exists but per-account item is missing.
				// This can happen if a previous run crashed after updating the
				// index on Delete (before removing the per-account item). Safe
				// to skip; List continues with the remaining entries.
				if s.debug != nil {
					fmt.Fprintf(s.debug, "aistat: orphan account index entry %s\n", uuid)
				}
				continue
			}
			var a Account
			if err := json.Unmarshal(data, &a); err != nil {
				return fmt.Errorf("accounts: parse account %s: %w", uuid, err)
			}
			accounts = append(accounts, a)
		}
		return nil
	})
	return accounts, err
}

func (s *darwinStore) Upsert(ctx context.Context, a Account) error {
	return s.withLock(func() error {
		// Write per-account item FIRST, then update index.
		// If we crash after writing the item but before updating the index, the
		// item is an orphan-without-index — silently ignored by List with a
		// --debug warn. The opposite ordering would produce an in-index item
		// whose fetch returns nil (an orphan-in-index), which is harder to
		// detect and surface.
		if err := upsertAccountItem(ctx, a); err != nil {
			return err
		}
		uuids, err := s.readIndex(ctx)
		if err != nil {
			return err
		}
		for _, id := range uuids {
			if id == a.UUID {
				return nil // already indexed
			}
		}
		uuids = append(uuids, a.UUID)
		return s.writeIndex(ctx, uuids)
	})
}

func (s *darwinStore) Delete(ctx context.Context, uuid string) error {
	return s.withLock(func() error {
		// Update index FIRST, then delete per-account item.
		// If we crash after updating the index but before deleting the item,
		// the item is an orphan-without-index — silently ignored by List.
		// The opposite ordering (delete item first) risks a UUID remaining in
		// the index with no backing item — an orphan-in-index that is harder
		// to surface cleanly.
		uuids, err := s.readIndex(ctx)
		if err != nil {
			return err
		}
		filtered := make([]string, 0, len(uuids))
		for _, id := range uuids {
			if id != uuid {
				filtered = append(filtered, id)
			}
		}
		if err := s.writeIndex(ctx, filtered); err != nil {
			return err
		}
		svc := darwinServicePrefix + uuid
		return darwinDeleteItem(ctx, svc, "")
	})
}

// darwinIsNotFound reports whether a security-command error means "item not found."
func darwinIsNotFound(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	// /usr/bin/security exits 44 (errSecItemNotFound) when the item is absent.
	if ee.ExitCode() == 44 {
		return true
	}
	return strings.Contains(strings.TrimSpace(string(ee.Stderr)), "could not be found")
}

// darwinKeychainErr extracts a readable message from a security-command failure.
func darwinKeychainErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if s := strings.TrimSpace(string(ee.Stderr)); s != "" {
			return fmt.Errorf("keychain: %s", s)
		}
	}
	return err
}

// mustMarshal marshals v to JSON, panicking on error.
// Used only where v is a known-serializable type (Account).
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("accounts: mustMarshal: %v", err))
	}
	return b
}
