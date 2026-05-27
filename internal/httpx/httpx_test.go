package httpx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drogers0/llm-usage/internal/providers"
)

func newDoer(t *testing.T, client *http.Client) *Doer {
	t.Helper()
	return &Doer{
		Client:     client,
		UserAgent:  "usage-check-test/0",
		ProviderID: "test",
	}
}

func TestGetJSON_OKUnmarshals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"foo":42}`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var got struct{ Foo int }
	if err := d.GetJSON(context.Background(), srv.URL, "tok", &got, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if got.Foo != 42 {
		t.Errorf("got %v, want 42", got.Foo)
	}
}

func TestGetJSON_BearerAndUAHeadersSet(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL, "tok-x", &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if captured.Get("Authorization") != "Bearer tok-x" {
		t.Errorf("Authorization wrong: %q", captured.Get("Authorization"))
	}
	if captured.Get("User-Agent") != "usage-check-test/0" {
		t.Errorf("User-Agent wrong: %q", captured.Get("User-Agent"))
	}
	if captured.Get("Accept") != "application/json" {
		t.Errorf("default Accept wrong: %q", captured.Get("Accept"))
	}
}

func TestGetJSON_ExtraHeadersOverrideDefault(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	d.ExtraHeaders = map[string]string{
		"Accept":         "application/vnd.github+json",
		"Anthropic-Beta": "oauth-2025-04-20",
	}
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if captured.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("ExtraHeaders should override Accept: %q", captured.Get("Accept"))
	}
	if captured.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Errorf("Anthropic-Beta header missing: %q", captured.Get("Anthropic-Beta"))
	}
}

func TestGetJSON_401IsAuthDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`unauthorized`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify)
	if !errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("expected ErrAuthDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("error should mention HTTP 401: %v", err)
	}
}

func TestGetJSON_503IsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify)
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient, got: %v", err)
	}
}

func TestGetJSON_418IsBareError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		w.Write([]byte(`teapot`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify)
	if errors.Is(err, providers.ErrTransient) || errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("418 should be bare error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 418") {
		t.Errorf("error should mention HTTP 418: %v", err)
	}
}

func TestGetJSON_NetworkErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // shut down before any request
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify)
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("network error should be transient: %v", err)
	}
}

func TestGetJSON_ContextCanceledNotTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var dst struct{}
	err := d.GetJSON(ctx, srv.URL, "tok", &dst, DefaultClassify)
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("cancelled ctx should not be transient: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestGetJSON_NonJSONOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>oops</html>`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{ Foo int }
	err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify)
	if err == nil || !strings.Contains(err.Error(), "non-JSON response from") {
		t.Errorf("expected non-JSON error, got: %v", err)
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error should include the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "<html>") {
		t.Errorf("error should include body snippet: %v", err)
	}
}

func TestGetJSON_DebugLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	d := newDoer(t, srv.Client())
	d.Debug = &buf
	d.ProviderID = "claude"
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL, "tok", &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "[debug] claude:") {
		t.Errorf("debug should be prefixed with provider id: %s", out)
	}
	if !strings.Contains(out, "GET "+srv.URL) {
		t.Errorf("debug should contain URL: %s", out)
	}
	if !strings.Contains(out, "-> ok") {
		t.Errorf("debug should contain ok outcome: %s", out)
	}
}

func TestGetJSON_RedirectLogsFinalURL(t *testing.T) {
	// Server with two endpoints: /a 301s to /b; /b returns 200.
	var captured string
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		// Build the redirect target relative to the test server's host.
		target := "http://" + r.Host + "/b"
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Path
		w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var buf bytes.Buffer
	d := newDoer(t, srv.Client())
	d.Debug = &buf
	d.ProviderID = "test"
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL+"/a", "tok", &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if captured != "/b" {
		t.Fatalf("redirect target not followed; captured %q", captured)
	}
	out := buf.String()
	if !strings.Contains(out, "/b") {
		t.Errorf("debug should log final URL containing /b, got: %s", out)
	}
}

func TestGetJSON_PartialReadDuringCancel(t *testing.T) {
	// Server starts streaming bytes then waits; cancellation mid-stream
	// must surface as context.Canceled, not a transient or non-JSON error.
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(200)
		w.Write([]byte(`{"foo":`)) // intentionally incomplete
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		cancel()
		time.Sleep(500 * time.Millisecond) // keep the body open past cancel
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{ Foo int }
	err := d.GetJSON(ctx, srv.URL, "tok", &dst, DefaultClassify)
	if err == nil {
		t.Fatal("expected error after cancel")
	}
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("cancel-during-read should not be transient: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestSnip(t *testing.T) {
	if Snip([]byte("short")) != "short" {
		t.Errorf("short string truncated")
	}
	long := bytes.Repeat([]byte("x"), 300)
	got := Snip(long)
	if len(got) != 203 { // 200 + "..."
		t.Errorf("long snip length = %d, want 203", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long snip should end with ...")
	}
}

func TestDefaultClassify(t *testing.T) {
	const url = "http://example.test/x"
	cases := []struct {
		status int
		want   error
	}{
		{401, providers.ErrAuthDenied},
		{403, providers.ErrAuthDenied},
		{408, providers.ErrTransient},
		{429, providers.ErrTransient},
		{500, providers.ErrTransient},
		{503, providers.ErrTransient},
		{599, providers.ErrTransient},
		{404, nil}, // bare error, not a sentinel
		{418, nil},
	}
	for _, c := range cases {
		err := DefaultClassify(url, c.status, []byte("body"))
		if c.want == nil {
			if errors.Is(err, providers.ErrAuthDenied) || errors.Is(err, providers.ErrTransient) {
				t.Errorf("status %d should be bare, got: %v", c.status, err)
			}
		} else if !errors.Is(err, c.want) {
			t.Errorf("status %d: expected %v, got %v", c.status, c.want, err)
		}
		// Every error must include both the URL and the body snippet so the
		// final message identifies which endpoint failed.
		if !strings.Contains(err.Error(), url) {
			t.Errorf("status %d: error should include url %q, got %v", c.status, url, err)
		}
		if !strings.Contains(err.Error(), "body") {
			t.Errorf("status %d: error should include body snippet, got %v", c.status, err)
		}
	}
}

func TestConcurrencySafeWriter_NoInterleave(t *testing.T) {
	var buf bytes.Buffer
	w := &ConcurrencySafeWriter{W: &buf}
	const N = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			w.Write([]byte("AAAAAAAAAA\n"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			w.Write([]byte("BBBBBBBBBB\n"))
		}
	}()
	wg.Wait()

	// Scan lines (bufio.Scanner ignores trailing empty entry from final \n).
	sc := bufio.NewScanner(&buf)
	count := 0
	for sc.Scan() {
		line := sc.Text()
		if line != "AAAAAAAAAA" && line != "BBBBBBBBBB" {
			t.Fatalf("interleaved line detected: %q", line)
		}
		count++
	}
	if count != 2*N {
		t.Fatalf("expected %d lines, got %d", 2*N, count)
	}
}
