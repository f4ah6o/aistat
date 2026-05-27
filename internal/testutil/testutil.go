// Package testutil provides shared test helpers for the provider packages.
// Production code must not import this package.
package testutil

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
