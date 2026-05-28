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

	"github.com/drogers0/aistat/internal/providers"
)

func newDoer(t *testing.T, client *http.Client) *Doer {
	t.Helper()
	return NewDoer(client, "aistat-test/0", "test", nil, nil)
}

func newDoerWithExtra(t *testing.T, client *http.Client, extra map[string]string) *Doer {
	t.Helper()
	return NewDoer(client, "aistat-test/0", "test", extra, nil)
}

func TestSanitizeDebugLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"line one\nline two", `line one\nline two`},
		{"with \"quotes\" inside", `with \"quotes\" inside`},
		{"tab\there", `tab\there`},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeDebugLine(c.in); got != c.want {
			t.Errorf("SanitizeDebugLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDoerLog_SanitizesNewlines(t *testing.T) {
	// A non-200 with a multi-line body must produce a single physical
	// debug line. Without sanitization, the embedded \n in the Snip'd body
	// would emit a multi-line log entry.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("<html>\nline two\nline three</html>"))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	d := newDoer(t, srv.Client())
	d.Debug = &buf
	var dst any
	_ = d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("expected exactly one newline in debug output, got %d:\n%s", got, out)
	}
	if !strings.HasPrefix(out, "[debug] test: GET ") {
		t.Errorf("expected [debug] prefix, got:\n%s", out)
	}
}

func TestGetJSON_OKUnmarshals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"foo":42}`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var got struct{ Foo int }
	if err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &got, DefaultClassify); err != nil {
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
	if err := d.GetJSON(context.Background(), srv.URL, "tok-x", 10*time.Second, &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if captured.Get("Authorization") != "Bearer tok-x" {
		t.Errorf("Authorization wrong: %q", captured.Get("Authorization"))
	}
	if captured.Get("User-Agent") != "aistat-test/0" {
		t.Errorf("User-Agent wrong: %q", captured.Get("User-Agent"))
	}
	if captured.Get("Accept") != "application/json" {
		t.Errorf("default Accept wrong: %q", captured.Get("Accept"))
	}
}

func TestGetJSON_ExtraHeadersDoesNotOverrideReserved(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	d := newDoerWithExtra(t, srv.Client(), map[string]string{
		"Authorization": "Bearer EVIL",
		"User-Agent":    "evil/0",
		"authorization": "Bearer EVIL-lower",
		"Accept":        "application/vnd.github+json",
	})
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify); err != nil {
		t.Fatal(err)
	}
	if got := captured.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization should be untouched, got %q", got)
	}
	if got := captured.Get("User-Agent"); got != "aistat-test/0" {
		t.Errorf("User-Agent should be untouched, got %q", got)
	}
	if got := captured.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("non-reserved key should still apply; Accept = %q", got)
	}
}

func TestGetJSON_ExtraHeadersOverrideDefault(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	d := newDoerWithExtra(t, srv.Client(), map[string]string{
		"Accept":         "application/vnd.github+json",
		"Anthropic-Beta": "oauth-2025-04-20",
	})
	var dst struct{}
	if err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify); err != nil {
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
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	err := d.GetJSON(ctx, srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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
	if err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify); err != nil {
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
	if err := d.GetJSON(context.Background(), srv.URL+"/a", "tok", 10*time.Second, &dst, DefaultClassify); err != nil {
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
	err := d.GetJSON(ctx, srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
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

func TestGetJSON_BodyTooLarge(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), maxBodyBytes+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{ Foo int }
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
	msg := err.Error()
	if !strings.Contains(msg, srv.URL) {
		t.Errorf("error should name URL, got: %v", err)
	}
	if !strings.Contains(msg, "1048576") {
		t.Errorf("error should name byte limit (1048576), got: %v", err)
	}
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("over-limit body should NOT be classified ErrTransient (would trigger retry): %v", err)
	}
	if errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("over-limit body should NOT be classified ErrAuthDenied: %v", err)
	}
}

func TestGetJSON_OversizedNon200StillClassified(t *testing.T) {
	// Defensive: an upstream returning a 2 MiB HTML error page on a 401 must
	// still classify as ErrAuthDenied. The size guard only applies to 200s.
	payload := bytes.Repeat([]byte("x"), maxBodyBytes+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write(payload)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
	if !errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("oversized 401 must classify as ErrAuthDenied, got: %v", err)
	}
	if strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized 401 must not surface as a size error, got: %v", err)
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

func TestGetJSON_ChildDeadlineWrappedTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{}
	err := d.GetJSON(context.Background(), srv.URL, "tok", 50*time.Millisecond, &dst, DefaultClassify)
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("child-only deadline must wrap as ErrTransient, got: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error chain must preserve context.DeadlineExceeded, got: %v", err)
	}
}

func TestGetJSON_ParentCancellationStaysBare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var dst struct{}
	err := d.GetJSON(ctx, srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("parent cancellation must NOT wrap as ErrTransient, got: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestGetJSON_ChildDeadlineDuringReadWrappedTransient(t *testing.T) {
	// Server sends headers + partial body, then stalls past the per-call
	// timeout so the child ctx expires during ReadAll. Parent ctx stays alive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(200)
		w.Write([]byte(`{"foo":`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	d := newDoer(t, srv.Client())
	var dst struct{ Foo int }
	err := d.GetJSON(context.Background(), srv.URL, "tok", 50*time.Millisecond, &dst, DefaultClassify)
	if err == nil {
		t.Fatal("expected error from child deadline during ReadAll")
	}
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("child deadline during read must wrap as ErrTransient, got: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error chain must preserve context.DeadlineExceeded, got: %v", err)
	}
}

func TestGetJSON_SchemeDowngradeRejected(t *testing.T) {
	// Downgrade target: plain HTTP. Capture whether it was ever hit.
	var downgradeHit bool
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downgradeHit = true
		w.Write([]byte(`{}`))
	}))
	defer plain.Close()

	// Origin: HTTPS, issues a 302 to the plain HTTP target.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tlsSrv.Close()

	// Build a Doer whose Client trusts the TLS server's cert (via the server's
	// own Client) but has our RejectSchemeDowngrade policy installed.
	client := tlsSrv.Client()
	client.CheckRedirect = RejectSchemeDowngrade
	d := newDoer(t, client)

	var dst struct{}
	err := d.GetJSON(context.Background(), tlsSrv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
	if err == nil {
		t.Fatal("expected scheme-downgrade error")
	}
	if !strings.Contains(err.Error(), "scheme downgrade") {
		t.Errorf("expected error mentioning 'scheme downgrade', got: %v", err)
	}
	if downgradeHit {
		t.Errorf("downgrade target must NOT have been reached")
	}
}

func TestConcurrencySafeWriter_NoInterleave(t *testing.T) {
	var buf bytes.Buffer
	w := NewConcurrencySafeWriter(&buf)
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
