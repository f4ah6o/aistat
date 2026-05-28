package accounts

import (
	"context"
	"sync"
)

// MemoryStore is a thread-safe in-memory implementation of Store for use in
// tests of other packages. It is exported so importing packages can construct
// it directly without going through OpenStore (which is platform-specific).
type MemoryStore struct {
	mu       sync.Mutex
	accounts map[string]Account
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{accounts: make(map[string]Account)}
}

func (m *MemoryStore) List(_ context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		list = append(list, a)
	}
	return list, nil
}

func (m *MemoryStore) Upsert(_ context.Context, a Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[a.UUID] = a
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accounts, uuid)
	return nil
}
