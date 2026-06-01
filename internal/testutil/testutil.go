// Package testutil provides shared test helpers for the provider packages.
// Production code must not import this package.
package testutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

// LoadFixture reads testdata/<name> relative to the calling package's testdata
// directory and returns its raw bytes. Fails the test on read error.
func LoadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// NewStubServer returns an httptest.Server that responds to every request with
// (status, body). If captureReq is non-nil, fields of the captured request are
// shallow-copied into *captureReq before responding; the caller pre-allocates
// `var got http.Request` and passes &got, preserving the existing test idiom.
// The captured request's Body is replaced with http.NoBody so any later read is
// panic-safe. t.Cleanup registers srv.Close.
func NewStubServer(t *testing.T, body []byte, status int, captureReq *http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captureReq != nil {
			*captureReq = *r
			captureReq.Body = http.NoBody
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// CountingServer is like NewStubServer (status, body, no capture) but also
// returns a counter incremented on every request. Closed on t.Cleanup.
func CountingServer(t *testing.T, status int, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// RejectServer returns a server that fails the test if it receives any request.
// role names the endpoint in the failure message (e.g. "profile", "refresh"),
// matching the contract "<role> server must not be called". Closed on t.Cleanup.
func RejectServer(t *testing.T, role string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s server must not be called, but received %s %s", role, r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// MemStore returns an accounts.MemoryStore pre-populated with accts (in order).
// Replaces the NewMemoryStore()+Upsert-loop idiom. Fails the test on Upsert error.
func MemStore(t *testing.T, accts ...accounts.Account) *accounts.MemoryStore {
	t.Helper()
	s := accounts.NewMemoryStore()
	for _, a := range accts {
		if err := s.Upsert(context.Background(), a); err != nil {
			t.Fatalf("store.Upsert: %v", err)
		}
	}
	return s
}
