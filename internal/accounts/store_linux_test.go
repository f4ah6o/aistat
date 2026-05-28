//go:build linux

package accounts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLinuxStore_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()

	a1 := makeTestAccount("uuid-1", "user1@example.com")
	if err := store.Upsert(ctx, a1); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].UUID != "uuid-1" {
		t.Errorf("want [uuid-1], got %v", list)
	}

	if err := store.Delete(ctx, "uuid-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want empty list after delete, got %v", list)
	}
}

func TestLinuxStore_FileMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.Upsert(context.Background(), makeTestAccount("uuid-1", "user1@example.com"))

	path := filepath.Join(home, ".config", "aistat", "accounts", "claude.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("file mode: got %04o, want 0600", fi.Mode().Perm())
	}
}

func TestLinuxStore_ParentDirMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := OpenStore(); err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	dir := filepath.Join(home, ".config", "aistat", "accounts")
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Errorf("dir mode: got %04o, want 0700", fi.Mode().Perm())
	}
}

func TestLinuxStore_ConcurrentUpserts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()

	a1 := makeTestAccount("uuid-1", "user1@example.com")
	a2 := makeTestAccount("uuid-2", "user2@example.com")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); store.Upsert(ctx, a1) }()
	go func() { defer wg.Done(); store.Upsert(ctx, a2) }()
	wg.Wait()

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 accounts after concurrent upserts, got %d: %v", len(list), list)
	}
}

func TestLinuxStore_EmptyAfterFinalDeleteRemovesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()

	store.Upsert(ctx, makeTestAccount("uuid-1", "user1@example.com"))
	store.Delete(ctx, "uuid-1")

	path := filepath.Join(home, ".config", "aistat", "accounts", "claude.json")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected file to be removed after deleting last account; stat err: %v", err)
	}
}

func TestLinuxStore_ListMissingFile(t *testing.T) {
	// Plan spec: "Missing file → empty list, no error."
	// Exercises the path where no file has ever been written.
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want empty list on missing file, got %v", list)
	}
}

func TestLinuxStore_CorruptJSONError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "aistat", "accounts")
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "claude.json")
	os.WriteFile(path, []byte("{not valid json"), 0600)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	_, err = store.List(context.Background())
	if err == nil {
		t.Fatal("expected error on corrupt JSON, got nil")
	}
}
