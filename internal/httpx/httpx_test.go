package httpx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/testutil"
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
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain string", "plain", "plain"},
		{"embedded newline", "line one\nline two", `line one\nline two`},
		{"embedded quotes", `with "quotes" inside`, `with \"quotes\" inside`},
		{"embedded tab", "tab\there", `tab\there`},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeDebugLine(tt.in); got != tt.want {
				t.Errorf("SanitizeDebugLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDoerLog_SanitizesNewlines(t *testing.T) {
	// A non-200 with a multi-line body must produce a single physical
	// debug line. Without sanitization, the embedded \n in the Snip'd body
	// would emit a multi-line log entry.
	// Use 401 (non-transient) so a single request is made; the test purpose is
	// that the multi-line body is sanitized into a single physical debug line.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
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

func TestGetJSON(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"ok unmarshals", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"foo":42}`))
			}))
			defer srv.Close()
			d := newDoer(t, srv.Client())
			var got struct{ Foo int }
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &got, DefaultClassify)
			testutil.WantNoErr(t, err)
			if got.Foo != 42 {
				t.Errorf("got %v, want 42", got.Foo)
			}
		}},
		{"bearer and ua headers set", func(t *testing.T) {
			var captured http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r.Header.Clone()
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok-x", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if captured.Get("Authorization") != "Bearer tok-x" {
				t.Errorf("Authorization wrong: %q", captured.Get("Authorization"))
			}
			if captured.Get("User-Agent") != "aistat-test/0" {
				t.Errorf("User-Agent wrong: %q", captured.Get("User-Agent"))
			}
			if captured.Get("Accept") != "application/json" {
				t.Errorf("default Accept wrong: %q", captured.Get("Accept"))
			}
		}},
		{"extra headers does not override reserved", func(t *testing.T) {
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
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if got := captured.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("Authorization should be untouched, got %q", got)
			}
			if got := captured.Get("User-Agent"); got != "aistat-test/0" {
				t.Errorf("User-Agent should be untouched, got %q", got)
			}
			if got := captured.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("non-reserved key should still apply; Accept = %q", got)
			}
		}},
		{"extra headers override default", func(t *testing.T) {
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
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if captured.Get("Accept") != "application/vnd.github+json" {
				t.Errorf("ExtraHeaders should override Accept: %q", captured.Get("Accept"))
			}
			if captured.Get("Anthropic-Beta") != "oauth-2025-04-20" {
				t.Errorf("Anthropic-Beta header missing: %q", captured.Get("Anthropic-Beta"))
			}
		}},
		{"401 is auth denied", func(t *testing.T) {
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
		}},
		{"503 is transient", func(t *testing.T) {
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
		}},
		{"418 is bare error", func(t *testing.T) {
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
		}},
		{"network error is transient", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			srv.Close() // shut down before any request
			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("network error should be transient: %v", err)
			}
		}},
		{"context canceled not transient", func(t *testing.T) {
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
		}},
		{"non-json on success", func(t *testing.T) {
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
		}},
		{"debug logs", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			var buf bytes.Buffer
			d := newDoer(t, srv.Client())
			d.Debug = &buf
			d.ProviderID = "claude"
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
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
		}},
		{"redirect logs final url", func(t *testing.T) {
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
			err := d.GetJSON(context.Background(), srv.URL+"/a", "tok", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if captured != "/b" {
				t.Fatalf("redirect target not followed; captured %q", captured)
			}
			out := buf.String()
			if !strings.Contains(out, "/b") {
				t.Errorf("debug should log final URL containing /b, got: %s", out)
			}
		}},
		{"partial read during cancel", func(t *testing.T) {
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
		}},
		{"body too large", func(t *testing.T) {
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
		}},
		{"oversized non-200 still classified", func(t *testing.T) {
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
		}},
		{"child deadline wrapped transient", func(t *testing.T) {
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
		}},
		{"parent cancellation stays bare", func(t *testing.T) {
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
		}},
		{"child deadline during read wrapped transient", func(t *testing.T) {
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
		}},
		{"scheme downgrade rejected", func(t *testing.T) {
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
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
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
	const testURL = "http://example.test/x"
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"401 auth denied", 401, providers.ErrAuthDenied},
		{"403 auth denied", 403, providers.ErrAuthDenied},
		{"408 transient", 408, providers.ErrTransient},
		{"429 transient", 429, providers.ErrTransient},
		{"500 transient", 500, providers.ErrTransient},
		{"503 transient", 503, providers.ErrTransient},
		{"599 transient", 599, providers.ErrTransient},
		{"404 bare error", 404, nil},
		{"418 bare error", 418, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.status}
			err := DefaultClassify(testURL, resp, []byte("body"))
			if tt.want == nil {
				if errors.Is(err, providers.ErrAuthDenied) || errors.Is(err, providers.ErrTransient) {
					t.Errorf("status %d should be bare, got: %v", tt.status, err)
				}
			} else if !errors.Is(err, tt.want) {
				t.Errorf("status %d: expected %v, got %v", tt.status, tt.want, err)
			}
			// Every error must include both the URL and the body snippet so the
			// final message identifies which endpoint failed.
			if !strings.Contains(err.Error(), testURL) {
				t.Errorf("status %d: error should include url %q, got %v", tt.status, testURL, err)
			}
			if !strings.Contains(err.Error(), "body") {
				t.Errorf("status %d: error should include body snippet, got %v", tt.status, err)
			}
		})
	}
}

// assertDelay fails the test if elapsed is not within [want-tol, want+tol].
func assertDelay(t *testing.T, elapsed, want, tol time.Duration) {
	t.Helper()
	lo, hi := want-tol, want+tol
	if elapsed < lo || elapsed > hi {
		t.Errorf("elapsed %v not in [%v, %v]", elapsed, lo, hi)
	}
}

func TestDo(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"retry on 429 uses retry-after", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(429)
					return
				}
				w.Write([]byte(`{"val":7}`))
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			var got struct{ Val int }
			start := time.Now()
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &got, DefaultClassify)
			testutil.WantNoErr(t, err)
			elapsed := time.Since(start)

			if calls.Load() != 2 {
				t.Errorf("expected 2 calls, got %d", calls.Load())
			}
			if got.Val != 7 {
				t.Errorf("decoded val = %d, want 7", got.Val)
			}
			// Must have waited at least the Retry-After duration.
			if elapsed < time.Second {
				t.Errorf("elapsed %v < 1s; Retry-After header not respected", elapsed)
			}
		}},
		{"retry on 429 without header uses exponential", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(429)
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			var dst struct{}
			start := time.Now()
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			elapsed := time.Since(start)

			if calls.Load() != int32(maxAttempts) {
				t.Errorf("expected %d calls, got %d", maxAttempts, calls.Load())
			}
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient, got: %v", err)
			}
			// Exponential schedule: ~0.5s + ~1.0s = ~1.5s (±20% jitter + scheduling slack).
			assertDelay(t, elapsed, 1500*time.Millisecond, 500*time.Millisecond)
		}},
		{"retry gives up after max attempts", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(503)
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)

			if calls.Load() != int32(maxAttempts) {
				t.Errorf("expected exactly %d attempts, got %d", maxAttempts, calls.Load())
			}
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient after exhausting retries, got: %v", err)
			}
		}},
		{"retry respects ctx cancel", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(429)
			}))
			defer srv.Close()

			// 300ms deadline — enough for the first HTTP round-trip but not the 30s sleep.
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()

			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.GetJSON(ctx, srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)

			// sleepWithCtx must short-circuit immediately (30s sleep > 300ms deadline),
			// so only 1 request should be made — the 429 — before the loop bails.
			if calls.Load() != 1 {
				t.Errorf("expected exactly 1 call (sleep short-circuited by deadline), got %d", calls.Load())
			}
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient, got: %v", err)
			}
		}},
		{"no retry on non-transient", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(401)
				w.Write([]byte(`unauthorized`))
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)

			if calls.Load() != 1 {
				t.Errorf("expected exactly 1 attempt for non-transient error, got %d", calls.Load())
			}
			if !errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("expected ErrAuthDenied, got: %v", err)
			}
		}},
		{"retry on 5xx then 200 succeeds", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					w.WriteHeader(502)
					return
				}
				w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			var got struct{ Ok bool }
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &got, DefaultClassify)
			testutil.WantNoErr(t, err)
			if calls.Load() != 2 {
				t.Errorf("expected 2 calls, got %d", calls.Load())
			}
			if !got.Ok {
				t.Errorf("decoded ok = false, want true")
			}
		}},
		{"retry debug logs", func(t *testing.T) {
			// Assert that retry log lines appear with correct format.
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(429)
					return
				}
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			var buf bytes.Buffer
			d := newDoer(t, srv.Client())
			d.Debug = &buf
			d.ProviderID = "test"
			var dst struct{}
			err := d.GetJSON(context.Background(), srv.URL, "tok", 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			out := buf.String()
			// One retry log line: "retry 2/3 after ...s (Retry-After: 1)"
			if !strings.Contains(out, fmt.Sprintf("retry 2/%d", maxAttempts)) {
				t.Errorf("expected retry log line with 'retry 2/%d', got:\n%s", maxAttempts, out)
			}
			if !strings.Contains(out, "Retry-After:") {
				t.Errorf("expected Retry-After in retry log, got:\n%s", out)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	future := now.Add(5 * time.Second)
	futureHeader := future.UTC().Format(http.TimeFormat)
	past := now.Add(-5 * time.Second)
	pastHeader := past.UTC().Format(http.TimeFormat)

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty header", "", 0},
		{"zero delta", "0", 0},
		{"negative delta", "-1", 0},
		{"5 second delta", "5", 5 * time.Second},
		{"120 second delta uncapped", "120", 120 * time.Second}, // uncapped; pickDelay caps at retryAfterCap
		{"non-numeric header", "foo", 0},
		{"past date", pastHeader, 0},                                          // past date → 0
		{"future date", futureHeader, 5 * time.Second},                        // future date → ~5s (exact)
		{"whitespace delta", "  5  ", 5 * time.Second},                        // leading/trailing whitespace in delta form
		{"whitespace http-date", "  " + futureHeader + "  ", 5 * time.Second}, // whitespace around HTTP-date
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.header, now)
			if got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestPickDelay(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"retry-after cap", func(t *testing.T) {
			// A large Retry-After value must be capped at retryAfterCap.
			got := pickDelay(0, 120*time.Second)
			if got != retryAfterCap {
				t.Errorf("pickDelay with 120s retryAfter = %v, want %v", got, retryAfterCap)
			}
		}},
		{"exponential schedule", func(t *testing.T) {
			// attempt 0 → ~0.5s ± 20%; attempt 1 → ~1.0s ± 20%.
			d0 := pickDelay(0, 0)
			lo0 := time.Duration(float64(500*time.Millisecond) * (1 - jitterFraction))
			hi0 := time.Duration(float64(500*time.Millisecond) * (1 + jitterFraction))
			if d0 < lo0 || d0 > hi0 {
				t.Errorf("attempt 0 delay %v not in [%v, %v]", d0, lo0, hi0)
			}

			d1 := pickDelay(1, 0)
			lo1 := time.Duration(float64(time.Second) * (1 - jitterFraction))
			hi1 := time.Duration(float64(time.Second) * (1 + jitterFraction))
			if d1 < lo1 || d1 > hi1 {
				t.Errorf("attempt 1 delay %v not in [%v, %v]", d1, lo1, hi1)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestPostForm(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"happy path", func(t *testing.T) {
			var gotMethod, gotContentType, gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotContentType = r.Header.Get("Content-Type")
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotBody = r.Form.Get("grant_type")
				w.Write([]byte(`{"result":99}`))
			}))
			defer srv.Close()
			d := newDoer(t, srv.Client())
			var got struct{ Result int }
			vals := url.Values{"grant_type": {"refresh_token"}}
			err := d.PostForm(context.Background(), srv.URL, vals, 10*time.Second, &got, DefaultClassify)
			testutil.WantNoErr(t, err)
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotContentType != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
			}
			if gotBody != "refresh_token" {
				t.Errorf("grant_type = %q, want refresh_token", gotBody)
			}
			if got.Result != 99 {
				t.Errorf("result = %d, want 99", got.Result)
			}
		}},
		{"no authorization header", func(t *testing.T) {
			var captured http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r.Header.Clone()
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.PostForm(context.Background(), srv.URL, url.Values{}, 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if got := captured.Get("Authorization"); got != "" {
				t.Errorf("Authorization should be absent on PostForm, got %q", got)
			}
		}},
		{"ua and accept still set", func(t *testing.T) {
			var captured http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r.Header.Clone()
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			d := newDoer(t, srv.Client())
			var dst struct{}
			err := d.PostForm(context.Background(), srv.URL, url.Values{}, 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if got := captured.Get("User-Agent"); got != "aistat-test/0" {
				t.Errorf("User-Agent = %q, want aistat-test/0", got)
			}
			if got := captured.Get("Accept"); got != "application/json" {
				t.Errorf("Accept = %q, want application/json", got)
			}
		}},
		{"extra headers user-agent reserved", func(t *testing.T) {
			var captured http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r.Header.Clone()
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			d := newDoerWithExtra(t, srv.Client(), map[string]string{
				"User-Agent": "evil-ua",
			})
			var dst struct{}
			err := d.PostForm(context.Background(), srv.URL, url.Values{}, 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if got := captured.Get("User-Agent"); got != "aistat-test/0" {
				t.Errorf("User-Agent should be Doer's value, got %q", got)
			}
		}},
		{"retry rebuilds body", func(t *testing.T) {
			// Capture the body on each request; assert it is byte-equal across attempts.
			var mu sync.Mutex
			var bodies []string
			var calls atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				mu.Lock()
				bodies = append(bodies, r.Form.Encode())
				mu.Unlock()
				if n < 2 {
					w.WriteHeader(429)
					return
				}
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			d := newDoer(t, srv.Client())
			vals := url.Values{"grant_type": {"refresh_token"}, "token": {"abc123"}}
			var dst struct{}
			err := d.PostForm(context.Background(), srv.URL, vals, 10*time.Second, &dst, DefaultClassify)
			testutil.WantNoErr(t, err)
			if calls.Load() != 2 {
				t.Errorf("expected 2 calls, got %d", calls.Load())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("expected 2 body captures, got %d", len(bodies))
			}
			if bodies[0] != bodies[1] {
				t.Errorf("body not equal across retries:\n  attempt 1: %q\n  attempt 2: %q", bodies[0], bodies[1])
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
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
