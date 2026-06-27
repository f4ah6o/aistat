# Support OpenCode Go

Status: polished
Model: claude-sonnet-4-6
Created: 2026-06-27
Updated: 2026-06-27
Branch: claude/pensive-shirley-b09d9a

Source:
- https://github.com/f4ah6o/agent-usage/issues/3

## 概要

OpenCode Go プロバイダーを aistat に追加し、`aistat usage` で使用状況を表示する。
初期実装は読み取り専用（usage のみ）で、マルチアカウント・switch は対象外。

## 背景

OpenCode Go は 5時間・週次・月次の3つの使用状況ウィンドウを持つクォータベースのAIコーディングサービス。
[opencode-bar](https://github.com/opgginc/opencode-bar) (MIT License) が同等のデータ取得を Swift で実装しており、参考にできる。

GitHub Issue 本文に DeepWiki 経由の参考情報があり、以下が判明している：

- 認証情報: `auth.json` 内の `opencode-go` エントリからAPIキーを取得
- APIキー検証: `https://opencode.ai/zen/go/v1/models`
- 使用状況データ: ダッシュボードAPIから取得（workspace ID + auth cookie が必要）
- 設定ファイル: `~/.config/opencode-bar/opencode-go.json` または `~/.config/opencode-quota/opencode-go.json`
- 環境変数: `OPENCODE_GO_WORKSPACE_ID`, `OPENCODE_GO_AUTH_COOKIE`, `OPENCODE_GO_CONFIG_FILE`
- 公式制限値: 5時間 $12、週次 $30、月次 $60
- サブスクリプション: Go ($10/m)

## 問題

aistat は Claude・Codex のみ対応しており、OpenCode Go の使用状況を確認できない。

## 目標

`aistat usage` で OpenCode Go の3ウィンドウ（5時間・週次・月次）を JSON・テキスト両形式で表示する。
OpenCode Go が未設定の環境ではプロバイダーをスキップし、他プロバイダーに影響しない。

## 対象外

- マルチアカウント対応（`accounts.Store` 統合）
- `aistat switch` によるアカウント切り替え
- `aistat accounts` サブコマンドでの管理
- OAuth リフレッシュフロー（APIキー認証のため不要の見込み）

## 提案する方針

既存の Claude/Codex プロバイダーと同じパターンに従う。

### 新規ファイル

1. **`internal/providers/opencodego/opencodego.go`** — `Client` 構造体、`New()`, `ID() string`, `Fetch(ctx) (ProviderOutput, error)`。認証情報読み取り → APIキー検証 → ダッシュボードから使用状況取得 → `map[string]Limit` へ変換。
2. **`internal/providers/opencodego/useragent.go`** — `DefaultUserAgent(version)` 関数。
3. **`internal/cred/opencodego.go`** — `ReadOpenCodeGoCredential(ctx) (Credential, error)` と sentinel `ErrOpenCodeGoTokenNotFound`。`auth.json` と設定ファイル/環境変数の両方を読む。

### 既存ファイル変更

4. **`internal/providers/types.go:27`** — `KnownProviderIDs` に `"opencodego"` を追加。
5. **`internal/render/text.go:15`** — `textLabels` に `"opencodego"` エントリを追加: `{{"five_hour", "5-hour"}, {"seven_day", "7-day"}, {"monthly", "Monthly"}}`。
6. **`cmd/aistat/registry.go`** — `realProviders()` に OpenCode Go プロバイダーを追加（`singleAccountProvider` ラッパー不要、初期実装はマルチアカウント非対応のため）。
7. **`cmd/aistat/fake_provider.go`** (fake build tag) — fake モードに OpenCode Go のデモデータを追加。

### 認証フロー

1. `~/.config/opencode/auth.json` から `opencode-go.key` を読む。
2. 設定ファイル (`~/.config/opencode-bar/opencode-go.json` or `~/.config/opencode-quota/opencode-go.json`) または環境変数から `workspaceId` + `authCookie` を読む。
3. いずれかが欠けていれば `ErrAuthMissing` を返す。

### ウィンドウキー

| ウィンドウ | キー | リセット周期 |
|---|---|---|
| 5時間 | `five_hour` | 5h |
| 週次 | `seven_day` | 168h |
| 月次 | `monthly` | 月末 |

### エラー処理

- 認証情報なし → `fmt.Errorf("... : %w", providers.ErrAuthMissing)` でラップ。回復メッセージ: 設定ファイルのパスを案内。
- API 401/403 → `providers.ErrAuthDenied`。
- API タイムアウト/5xx → `providers.ErrTransient`（`httpx.Doer` のリトライに委ねる）。

## 受け入れ条件

- [ ] `aistat usage` の JSON 出力に `"opencodego"` キーが含まれ、`five_hour`・`seven_day`・`monthly` の各ウィンドウが `used_percent`・`remaining_percent`・`resets_at`・`reset_after_seconds` を持つ
- [ ] `aistat usage -h` のテキスト出力に OpenCode Go セクションが表示される
- [ ] OpenCode Go 未設定環境で `aistat usage` を実行した場合、opencodego にエラーが記録されるが他プロバイダーの出力は正常
- [ ] `go build -tags=fake -o /tmp/aistat ./cmd/aistat && /tmp/aistat --fake` で OpenCode Go のデモデータが表示される
- [ ] `go test ./...` が通る
- [ ] `go vet ./...` と `staticcheck ./...` がクリーン
- [ ] `GOOS=linux go vet ./...` がクリーン

## テスト計画

### ユニットテスト

- `internal/providers/opencodego/opencodego_test.go` — httptest サーバーでモックレスポンスを返し、`Fetch()` が正しい `Limit` マップを構築することを検証。エラーケース（401, 500, タイムアウト）も検証。
- `internal/cred/opencodego_test.go` — 設定ファイル/環境変数の読み取りロジックを検証。ファイルなし → sentinel エラー。

### 結合テスト

- `go test ./...` で全パッケージが通ることを確認。
- `go test -race ./...` でデータ競合がないことを確認。

### 手動確認

- `go build -tags=fake -o /tmp/aistat ./cmd/aistat && /tmp/aistat --fake` — JSON に opencodego が含まれる。
- `go build -tags=fake -o /tmp/aistat ./cmd/aistat && /tmp/aistat --fake -h` — テキスト出力に OpenCode Go セクションが表示される。
- `go vet ./...` + `staticcheck ./...` がクリーン。
- `GOOS=linux go vet ./... && GOOS=linux staticcheck ./...` がクリーン。

## リスク

- ダッシュボードAPIは公式APIではなく、Cookie 認証のためセッション切れで取得失敗する可能性がある。失敗時は `ErrAuthDenied` で報告し、設定の再取得を案内する。
- APIレスポンスの形状が文書化されていないため、参考実装 (opencode-bar) のソースコードから逆算する必要がある。形状が変わった場合は `IssueTrackerURL` を案内するエラーメッセージを出す。

## 変更履歴

yes — 新規プロバイダー「OpenCode Go」を追加。`aistat usage` で OpenCode Go の使用状況（5時間・週次・月次）を表示可能に。

## 解決済みの質問

1. **ダッシュボードAPIのエンドポイントとレスポンス形状**: `GET https://opencode.ai/workspace/{WORKSPACE_ID}/go`。Cookie `auth={AUTH_COOKIE}` で認証。レスポンスは HTML で script タグ内に `"rolling"`, `"weekly"`, `"monthly"` の JSON ブロックが埋め込まれ、各ブロックに `"usagePercent"` と `"resetInSec"` フィールドがある。正規表現でパース。
2. **設定ファイルのパスと形状**: APIキー (`auth.json`) は不使用。ダッシュボード認証は Cookie ベース。`workspaceId` + `authCookie` は環境変数 (`OPENCODE_GO_WORKSPACE_ID`, `OPENCODE_GO_AUTH_COOKIE`) または `~/.config/opencode-bar/opencode-go.json` (JSON: `{"workspaceId": "...", "authCookie": "..."}`) から取得。
3. **月次リセットタイミング**: サーバーが `resetInSec`（残り秒数）を返すため、`resets_at = now + resetInSec` で計算できる。実装側での月次計算は不要。

## 注記

- GitHub Issue 作成日時: 2026-06-26T20:24:06Z
- GitHub Issue 更新日時: 2026-06-26T20:24:06Z
- ラベル: なし
- GitHub Issue は 2026-06-27 にクローズ済み
- 参考実装 [opencode-bar](https://github.com/opgginc/opencode-bar) のソースコード調査が未解決の質問の解決に必須
