# Testing conventions

Tests in this repo are **table-driven with `t.Run` subtests**. One parent `Test<Behavior>`
per behavior, with each case a row in a `tests` slice. This keeps every case for a behavior
in one place and gives precise per-case failure names.

## The standard idiom

Loop variable `tt`, slice named `tests`, a leading `name string` field, wrapped in `t.Run`:

```go
func TestWindowLabel(t *testing.T) {
	tests := []struct {
		name string
		secs int64
		want string
	}{
		{"five_hour exact", 18000, "five_hour"},
		{"just below lower boundary", 17099, "window_17099s"},
		{"zero duration", 0, "window_0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowLabel(tt.secs); got != tt.want {
				t.Errorf("windowLabel(%d) = %q, want %q", tt.secs, got, tt.want)
			}
		})
	}
}
```

## Two table shapes

- **Data table** — for homogeneous cases that differ only in inputs/expected values (the
  example above). Each old standalone func becomes one row.
- **Closure table** — for heterogeneous cases that each need distinct setup or assertions
  (e.g. `Fetch` cases that spin up different stub servers). Rows carry a `run func(t *testing.T)`;
  each case body lives in one place, unchanged:

  ```go
  func TestRotateRawBlob(t *testing.T) {
  	tests := []struct {
  		name string
  		run  func(t *testing.T)
  	}{
  		{"missing tokens object errors", func(t *testing.T) {
  			_, err := rotateRawBlob([]byte(`{"other":"field"}`), Token{AccessToken: "x"})
  			if err == nil {
  				t.Fatal("expected error for missing tokens object")
  			}
  			if !strings.Contains(err.Error(), "tokens missing or wrong type") {
  				t.Errorf("error = %q, want 'tokens missing or wrong type'", err.Error())
  			}
  		}},
  	}
  	for _, tt := range tests {
  		t.Run(tt.name, tt.run)
  	}
  }
  ```

## Rules

- **Migrate, never rewrite.** Each row asserts exactly what the original standalone func
  asserted — same inputs, same expected values, same predicates. No dropped cases, no
  weakened assertions (`len(x)==2` must not become "non-empty"; `errors.Is(err, Sentinel)`
  must not become `err != nil`; request-count checks stay).
- **Verbatim error-string contracts.** Any asserted substring (via `strings.Contains`,
  exact compare, etc.) is copied byte-for-byte.
- **Setup stays inside the subtest.** Build stub servers, counters, `t.Setenv`, `t.TempDir`,
  `t.Cleanup`, etc. inside `t.Run` (or the row's `run` closure), using the subtest's `t` —
  not hoisted to the parent. Hoist only where the original cluster already shared one setup.
- **No `t.Parallel`.** Subtests run sequentially; many use `t.Setenv` / shared stub state.
- **`t.Fatal` stays in its row** — it aborts that subtest only, matching the old
  one-func-per-case independence. A shared prerequisite that must abort the whole cluster
  goes in the parent before the loop.
- **Subtest names** are lower-case domain phrases mirroring the old `_Suffix`, unique within
  the parent. Avoid `/` (subtest separator).
- **Cluster, don't over-merge.** One parent per existing `_*` cluster. A one-off func with no
  siblings may stay standalone.

## Verifying a migration (no behavior change)

`go test ./...` and `go test -race ./...` must stay green, and the executed case set must be
unchanged. For a migrated package, the count of **true leaf** subtests (names that are not a
prefix of another) must equal the pre-migration count, and no asserted string literal may
disappear (consolidated failure-message *format* strings like `windowLabel(%d)=...` may change;
asserted *values* must not). See `table-driven-test-convention_PLAN.md` for the exact
`go test -json` leaf check and `gofmt`-AST string-literal diff.
