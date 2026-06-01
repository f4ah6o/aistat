package accounts

import (
	"context"
	"sync"
	"testing"
	"time"
)

func makeTestAccount(uuid, email string) Account {
	raw := rawBlob("at-"+uuid, "rt-"+uuid, 0)
	a, err := NewAccount(raw, uuid, email, "", "", time.Now())
	if err != nil {
		panic(err)
	}
	return a
}

func TestMemoryStore(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"list empty", func(t *testing.T) {
			s := NewMemoryStore()
			list, err := s.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 0 {
				t.Errorf("want empty list, got %v", list)
			}
		}},
		{"upsert and list", func(t *testing.T) {
			s := NewMemoryStore()
			ctx := context.Background()

			a := makeTestAccount("uuid-1", "user1@example.com")
			if err := s.Upsert(ctx, a); err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			list, err := s.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("want 1 account, got %d", len(list))
			}
			if list[0].UUID != "uuid-1" {
				t.Errorf("UUID: got %q", list[0].UUID)
			}
		}},
		{"upsert updates", func(t *testing.T) {
			s := NewMemoryStore()
			ctx := context.Background()

			a := makeTestAccount("uuid-1", "user1@example.com")
			s.Upsert(ctx, a)

			updated := a
			updated.Email = "updated@example.com"
			s.Upsert(ctx, updated)

			list, _ := s.List(ctx)
			if len(list) != 1 {
				t.Fatalf("want 1 account after update, got %d", len(list))
			}
			if list[0].Email != "updated@example.com" {
				t.Errorf("Email after update: got %q", list[0].Email)
			}
		}},
		{"delete", func(t *testing.T) {
			s := NewMemoryStore()
			ctx := context.Background()

			s.Upsert(ctx, makeTestAccount("uuid-1", "user1@example.com"))
			s.Upsert(ctx, makeTestAccount("uuid-2", "user2@example.com"))

			if err := s.Delete(ctx, "uuid-1"); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			list, _ := s.List(ctx)
			if len(list) != 1 {
				t.Fatalf("want 1 account after delete, got %d", len(list))
			}
			if list[0].UUID != "uuid-2" {
				t.Errorf("remaining UUID: got %q, want uuid-2", list[0].UUID)
			}
		}},
		{"delete non-existent", func(t *testing.T) {
			s := NewMemoryStore()
			// Delete on an absent UUID must not error.
			if err := s.Delete(context.Background(), "no-such-uuid"); err != nil {
				t.Errorf("Delete non-existent: want nil, got %v", err)
			}
		}},
		{"concurrent upserts", func(t *testing.T) {
			s := NewMemoryStore()
			ctx := context.Background()

			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				uuid := "uuid-" + string(rune('a'+i))
				email := uuid + "@example.com"
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.Upsert(ctx, makeTestAccount(uuid, email))
				}()
			}
			wg.Wait()

			list, err := s.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 20 {
				t.Errorf("want 20 accounts, got %d", len(list))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
