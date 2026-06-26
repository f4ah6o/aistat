pub mod account;
pub mod reconcile;
pub mod refresh;

use super::{
    multiaccount::{budget_secs, record_fetch_outcome, recompute_reset_after, sort_account_results},
    usagecache::Cache,
    AccountResult, Limit, Provider, ProviderError, ProviderOutput,
};
use crate::{
    accounts::{MemoryStore, Store},
    cred::{self, Credential},
    httpx::Doer,
};
use account::{stored_access_token_owned, stored_expires_at, stored_refresh_token};
use chrono::Utc;
use reconcile::{reconcile, ReconcileInput};
use refresh::RefreshClient;
use serde::Deserialize;
use std::collections::BTreeMap;
use std::sync::Arc;

const ENDPOINT: &str = "https://chatgpt.com/backend-api/wham/usage";
const CRED_TIMEOUT_SECS: u64 = 10;
const BASE_TIMEOUT_SECS: u64 = 10;
const PER_ACCOUNT_BUDGET_SECS: u64 = 15;
const REFRESH_SKEW_MS: i64 = 30_000;

pub fn default_user_agent(version: &str) -> String {
    format!(
        "agent-usage/{} (codex; https://github.com/f4ah6o/aistat)",
        version
    )
}

fn is_revoked_token_err(e: &ProviderError) -> bool {
    if !e.is_auth_denied() {
        return false;
    }
    let s = e.to_string();
    s.contains("token_revoked") || s.contains("token_invalidated")
}

fn window_label(limit_window_seconds: i64) -> String {
    struct Bucket {
        center: i64,
        label: &'static str,
    }
    let buckets = [
        Bucket { center: 18_000, label: "five_hour" },
        Bucket { center: 604_800, label: "seven_day" },
        Bucket { center: 2_592_000, label: "thirty_day" },
    ];
    for b in &buckets {
        let lo = b.center - b.center / 20;
        let hi = b.center + b.center / 20;
        if limit_window_seconds >= lo && limit_window_seconds <= hi {
            return b.label.to_string();
        }
    }
    format!("window_{}s", limit_window_seconds)
}

#[derive(Deserialize)]
struct Window {
    used_percent: f64,
    limit_window_seconds: i64,
    reset_after_seconds: i64,
    reset_at: i64,
}

#[derive(Deserialize)]
struct RateLimit {
    primary_window: Option<Window>,
    secondary_window: Option<Window>,
}

#[derive(Deserialize)]
struct UsageResp {
    rate_limit: Option<RateLimit>,
    code_review_rate_limit: Option<Window>,
}

pub struct CodexClient {
    doer: Arc<Doer>,
    refresh: RefreshClient,
    store: Arc<dyn Store>,
    cache: Cache,
    cache_bypass: bool,
}

impl CodexClient {
    pub fn new(
        user_agent: String,
        debug: Option<Box<dyn Fn(&str) + Send + Sync>>,
        store: Option<Arc<dyn Store>>,
        cache_bypass: bool,
    ) -> Self {
        let doer = Arc::new(Doer::new(
            user_agent,
            "codex",
            vec![],
            debug,
        ));
        let refresh = RefreshClient::new(Arc::clone(&doer));
        let store = store.unwrap_or_else(|| Arc::new(MemoryStore::new()));
        let cache = Cache::new("codex");
        Self { doer, refresh, store, cache, cache_bypass }
    }

    fn read_live_credential(&self) -> Result<Option<Credential>, ProviderError> {
        match cred::codex::read_codex_credential() {
            Ok(c) => Ok(Some(c)),
            Err(cred::CredError::CodexNotFound) => Ok(None),
            Err(e) => Err(ProviderError::Other(e.to_string())),
        }
    }

    fn window_to_limit(w: &Window) -> Option<Limit> {
        if w.reset_at <= 0 {
            return None;
        }
        let resets = chrono::DateTime::<Utc>::from_timestamp(w.reset_at, 0)?;
        let now = Utc::now();
        let secs = (resets - now).num_seconds().max(0);
        Some(Limit {
            used_percent: w.used_percent,
            remaining_percent: 100.0 - w.used_percent,
            resets_at: resets,
            reset_after_seconds: secs,
        })
    }

    fn fetch_limits_fresh(&self, access_token: &str) -> Result<BTreeMap<String, Limit>, ProviderError> {
        let raw: UsageResp = self.doer.get(ENDPOINT, access_token, PER_ACCOUNT_BUDGET_SECS)?;

        let rl = raw.rate_limit.ok_or_else(|| {
            ProviderError::Other("codex usage response missing rate_limit object".into())
        })?;

        let mut limits = BTreeMap::new();
        for w_opt in [rl.primary_window.as_ref(), rl.secondary_window.as_ref()] {
            if let Some(w) = w_opt {
                if let Some(lim) = Self::window_to_limit(w) {
                    limits.insert(window_label(w.limit_window_seconds), lim);
                }
            }
        }
        if let Some(w) = raw.code_review_rate_limit.as_ref() {
            if let Some(lim) = Self::window_to_limit(w) {
                limits.insert(format!("code_review_{}", window_label(w.limit_window_seconds)), lim);
            }
        }
        Ok(limits)
    }

    fn fetch_limits_cached(
        &self,
        access_token: &str,
        uuid: &str,
    ) -> Result<BTreeMap<String, Limit>, ProviderError> {
        if uuid.is_empty() {
            return self.fetch_limits_fresh(access_token);
        }
        if !self.cache_bypass {
            if let Some((cached, age)) = self.cache.get_with_age(uuid) {
                let refreshed = recompute_reset_after(cached, Utc::now());
                return Ok(refreshed);
            }
        }
        let limits = self.fetch_limits_fresh(access_token)?;
        self.cache.put(uuid, &limits);
        Ok(limits)
    }

    fn refresh_error_message(e: &ProviderError) -> String {
        let msg = e.to_string();
        if msg.contains("already been used") {
            "stale refresh token (codex CLI rotated it); retry or run `codex login` to recover".into()
        } else if msg.contains("invalid_grant") {
            "account credential expired (run `codex login` to refresh)".into()
        } else {
            msg
        }
    }
}

impl Provider for CodexClient {
    fn id(&self) -> &str {
        "codex"
    }

    fn fetch(&self) -> Result<ProviderOutput, ProviderError> {
        let live = self.read_live_credential()?;

        let stored = self.store.list().unwrap_or_else(|e| {
            eprintln!("aistat: codex: could not read account store ({}); proceeding with live credential only", e);
            vec![]
        });

        let reconcile_out = reconcile(ReconcileInput {
            live_blob: live.as_ref(),
            stored: &stored,
            now: Utc::now(),
        });

        if reconcile_out.inserted || reconcile_out.upserted {
            for acct in &reconcile_out.accounts {
                if acct.uuid == reconcile_out.active_uuid {
                    let _ = self.store.upsert(acct.clone());
                    break;
                }
            }
        }

        if live.is_none() && reconcile_out.accounts.is_empty() {
            return Err(ProviderError::AuthMissing(
                "codex token not found at ~/.codex/auth.json — run `codex login`".into(),
            ));
        }

        if let Some(ref warn) = reconcile_out.capture_warn {
            eprintln!("{}", warn);
        }

        let total_accounts = reconcile_out.accounts.len()
            + if reconcile_out.live_unstored.is_some() { 1 } else { 0 };
        let _budget = budget_secs(BASE_TIMEOUT_SECS, PER_ACCOUNT_BUDGET_SECS, total_accounts);

        let mut account_results: Vec<AccountResult> = vec![];
        let mut transient_count = 0usize;
        let mut success_count = 0usize;

        if let Some(ref live_cred) = reconcile_out.live_unstored {
            let limits_result = self.fetch_limits_fresh(&live_cred.access_token);
            let mut ar = AccountResult {
                email: "(live Codex account)".into(),
                uuid: String::new(),
                plan: String::new(),
                active: true,
                limits: None,
                error: None,
            };
            let revoked = limits_result
                .as_ref()
                .err()
                .map(|e| is_revoked_token_err(e))
                .unwrap_or(false);
            let (ok, trans) = record_fetch_outcome(&mut ar, limits_result);
            if ok { success_count += 1; }
            if trans { transient_count += 1; }
            if revoked {
                ar.error = Some(format!(
                    "aistat: codex: {}: tokens revoked by upstream (likely a `codex login` for another account); run `codex login` to recover",
                    ar.email
                ));
            }
            account_results.push(ar);
        }

        let now = Utc::now();
        for acct in &reconcile_out.accounts {
            let mut ar = AccountResult {
                email: acct.email.clone(),
                uuid: acct.uuid.clone(),
                plan: acct.rate_limit_tier.clone(),
                active: acct.uuid == reconcile_out.active_uuid,
                limits: None,
                error: None,
            };

            let expires_at_ms = stored_expires_at(acct);
            if expires_at_ms > 0 {
                let now_plus_skew = now.timestamp_millis() + REFRESH_SKEW_MS;
                if expires_at_ms < now_plus_skew {
                    match self.refresh.exchange(stored_refresh_token(acct)) {
                        Err(e) => {
                            ar.error = Some(Self::refresh_error_message(&e));
                            if e.is_transient() { transient_count += 1; }
                            account_results.push(ar);
                            continue;
                        }
                        Ok(tok) => {
                            if let Ok(new_blob) = account::rotate_raw_blob(&acct.raw_blob, &tok) {
                                let mut updated = acct.clone();
                                updated.raw_blob = new_blob;
                                let _ = self.store.upsert(updated);
                            }
                        }
                    }
                }
            }

            let at = stored_access_token_owned(acct);
            let limits_result = self.fetch_limits_cached(&at, &acct.uuid);
            let revoked = limits_result
                .as_ref()
                .err()
                .map(|e| is_revoked_token_err(e))
                .unwrap_or(false);
            let (ok, trans) = record_fetch_outcome(&mut ar, limits_result);
            if ok { success_count += 1; }
            if trans { transient_count += 1; }
            if revoked {
                ar.error = Some(format!(
                    "aistat: codex: {}: tokens revoked by upstream (likely a `codex login` for another account); run `codex login` to recover",
                    acct.email
                ));
            }
            account_results.push(ar);
        }

        sort_account_results(&mut account_results);

        if success_count == 0 && transient_count > 0 {
            return Err(ProviderError::Transient(format!(
                "all {} account fetch(es) failed",
                account_results.len()
            )));
        }

        Ok(ProviderOutput {
            limits: None,
            accounts: account_results,
        })
    }
}
