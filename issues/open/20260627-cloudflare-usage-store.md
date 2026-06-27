# Store five-hour usage in a Cloudflare service

Status: open
Model: GPT-5
Created: 2026-06-27
Updated: 2026-06-27
Branch: feat/20260627-cloudflare-usage-store

## 概要

`agent-limits usage` の結果から5時間枠の usage だけをクラウドに登録する、Cloudflare serverless 前提の最小サービスを追加する。
サービスは GitHub ログインで利用者を識別し、プロバイダーの認証情報や全 usage レスポンスは保存しない。

## 背景

このリポジトリは Claude Code、Codex、OpenCode Go の利用制限を CLI で報告する。
次の段階として、CLI が取得した使用状況のうち短期的な5時間制限だけをクラウド側に保存し、端末や実行環境をまたいで参照できるようにする。

Cloudflare Workers などの serverless 構成を想定する。
認証は GitHub login を使い、1Password Developer Environments を扱う場合は値を表示せず、1Password MCP server の認可と実行時注入を使う。

## 問題

現在は `agent-limits usage` の結果がローカルで完結しており、5時間制限の usage をクラウドへ登録・保持する仕組みがない。
クラウド側で保存すべきデータ範囲、GitHub 認証、秘密情報の扱いが未定義のため、実装時に過剰なデータ保存や secret の露出が起きるリスクがある。

## 目標

GitHub login 済みの利用者ごとに、`agent-limits usage` の5時間枠 usage だけを Cloudflare 側へ保存できる。
保存対象は usage に限定し、プロバイダー資格情報、Cookie、アクセストークン、週次・月次の利用状況、デバッグログは保存しない。

## 対象外

- Claude、Codex、OpenCode Go へのクラウド側ログイン代行
- provider credential、auth cookie、access token、refresh token の保存
- 週次・月次制限の保存
- 課金、チーム管理、共有ダッシュボード
- Cloudflare 以外のホスティング実装

## 提案する方針

Cloudflare Workers を API 層として追加し、GitHub OAuth login でユーザーを識別する。
保存先は Cloudflare D1 または KV を候補にし、まずは単純な最新値保存で足りるか、履歴が必要かを実装前に決める。

CLI 側には `agent-limits usage` の結果から5時間枠だけを抽出して送信する最小コマンドまたはオプションを追加する。
送信 payload は provider ID、5時間枠の used/remaining/reset 情報、取得時刻、CLI バージョン程度に限定する。

サーバー側は GitHub user ID を主キーに含め、同一ユーザー・同一 provider の5時間 usage を upsert する。
secret は Cloudflare の環境変数または 1Password runtime injection で扱い、ログやエラーに値を出さない。

## 受け入れ条件

- [ ] GitHub login で認証したユーザーだけが5時間 usage を登録できる
- [ ] `agent-limits usage` の結果から5時間枠に相当する usage だけが送信・保存される
- [ ] 保存データに provider credential、cookie、access token、refresh token、週次・月次 usage が含まれない
- [ ] 同一ユーザー・同一 provider の登録は最新値として更新される
- [ ] 未認証リクエストと別ユーザーのデータ参照が拒否される
- [ ] サーバー・CLI のログに secret 値が出力されない
- [ ] Cloudflare ローカル開発と本番デプロイに必要な設定項目が README または運用ドキュメントに記載される

## テスト計画

- `cargo fmt --check`
- `cargo check --all-targets`
- `cargo test`
- `cargo clippy --all-targets -- -D warnings`
- Cloudflare 側のユニットテストで、未認証、認証済み upsert、他ユーザー参照拒否、payload schema の検証を確認する
- ローカル Workers 実行で GitHub OAuth callback と usage 登録 API の手動確認を行う
- ログ出力に secret、cookie、token が含まれないことをレビューで確認する

## リスク

GitHub OAuth と Cloudflare の secret 設定が増えるため、開発環境と本番環境で設定漏れが起きやすい。
5時間枠の名称や構造は provider ごとに異なる可能性があるため、CLI 側で canonical な5時間 usage へ正規化する必要がある。
履歴保存を後から追加する場合、最新値のみの schema から移行が必要になる可能性がある。

## 変更履歴

`CHANGES.md` impact: yes

項目案：

- GitHub login で認証し、5時間 usage だけを保存する Cloudflare serverless サービスを追加。

## 注記

実装時は Cloudflare Workers、GitHub OAuth、D1 または KV の選定を最初に確定する。
1Password Developer Environments を使う場合は 1Password MCP server と runtime injection を使い、secret 値の表示や記録を避ける。
