pub mod account;
pub mod profile;
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
use account::{stored_access_token, stored_expires_at, stored_refresh_token};
use chrono::Utc;
use reconcile::{reconcile, ReconcileInput};
use refresh::RefreshClient;
use serde::Deserialize;
use std::collections::BTreeMap;
use std::sync::Arc;

const ENDPOINT: &str = "https://api.anthropic.com/api/oauth/usage";
const CRED_TIMEOUT_SECS: u64 = 10;
const BASE_TIMEOUT_SECS: u64 = 10;
const PER_ACCOUNT_BUDGET_SECS: u64 = 15;
const REFRESH_SKEW_MS: i64 = 30_000;

pub fn default_user_agent(version: &str) -> String {
    format!(
        "agent-usage/{} (claude; https://github.com/f4ah6o/aistat)",
        version
    )
}

pub struct ClaudeClient {
    doer: Arc<Doer>,
    refresh: RefreshClient,
    store: Arc<dyn Store>,
    cache: Cache,
    cache_bypass: bool,
}

impl ClaudeClient {
    pub fn new(
        user_agent: String,
        debug: Option<Box<dyn Fn(&str) + Send + Sync>>,
        store: Option<Arc<dyn Store>>,
        cache_bypass: bool,
    ) -> Self {
        let doer = Arc::new(Doer::new(
            user_agent.clone(),
            "claude",
            vec![("Anthropic-Beta".into(), "oauth-2025-04-20".into())],
            debug,
        ));
        let refresh = RefreshClient::new(Arc::clone(&doer));
        let store = store.unwrap_or_else(|| Arc::new(MemoryStore::new()));
        let cache = Cache::new("claude");
        Self {
            doer,
            refresh,
            store,
            cache,
            cache_bypass,
        }
    }

    fn read_live_credential(&self) -> Result<Option<Credential>, ProviderError> {
        match cred::claude::read_claude_credential() {
            Ok(c) => Ok(Some(c)),
            Err(cred::CredError::ClaudeNotFound) => Ok(None),
            Err(e) => Err(ProviderError::Other(e.to_string())),
        }
    }

    fn fetch_limits_fresh(&self, access_token: &str) -> Result<BTreeMap<String, Limit>, ProviderError> {
        #[derive(Deserialize)]
        struct Window {
            utilization: f64,
            resets_at: Option<String>,
        }

        let raw: BTreeMap<String, serde_json::Value> =
            self.doer.get(ENDPOINT, access_token, PER_ACCOUNT_BUDGET_SECS)?;

        let now = Utc::now();
        let mut limits = BTreeMap::new();

        for key in &["five_hour", "seven_day", "seven_day_sonnet"] {
            let Some(val) = raw.get(*key) else { continue };
            let win: Window = match serde_json::from_value(val.clone()) {
                Ok(w) => w,
                Err(_) => continue,
            };
            let Some(resets_str) = win.resets_at else { continue };
            let resets = match resets_str.parse::<chrono::DateTime<Utc>>() {
                Ok(t) => t,
                Err(_) => {
                    return Err(ProviderError::Other(format!(
                        "claude window {} has unparseable resets_at {:?}",
                        key, resets_str
                    )))
                }
            };
            let secs = (resets - now).num_seconds().max(0);
            limits.insert(
                key.to_string(),
                Limit {
                    used_percent: win.utilization,
                    remaining_percent: 100.0 - win.utilization,
                    resets_at: resets,
                    reset_after_seconds: secs,
                },
            );
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
                if let Some(ref dbg) = self.doer.debug {
                    dbg(&format!(
                        "[debug] claude: usage cache hit for {} (age {}s)\n",
                        uuid,
                        age.as_secs()
                    ));
                }
                return Ok(refreshed);
            }
        }
        let limits = self.fetch_limits_fresh(access_token)?;
        self.cache.put(uuid, &limits);
        Ok(limits)
    }

    fn refresh_error_message(e: &ProviderError) -> String {
        let msg = e.to_string();
        if msg.contains("invalid_grant") {
            "account credential expired (run `claude /login` to refresh)".into()
        } else if msg.contains("refresh endpoint") || msg.contains("broken") {
            format!(
                "aistat: claude: refresh endpoint rejected request ({}); this is likely an aistat refresh implementation issue. Run 'claude /login' to work around it.",
                msg
            )
        } else {
            msg
        }
    }
}

impl Provider for ClaudeClient {
    fn id(&self) -> &str {
        "claude"
    }

    fn fetch(&self) -> Result<ProviderOutput, ProviderError> {
        let live = self.read_live_credential()?;

        let stored = self.store.list().unwrap_or_else(|e| {
            eprintln!("aistat: claude: could not read account store ({}); proceeding with live credential only", e);
            vec![]
        });

        let profile_doer = Arc::clone(&self.doer);
        let reconcile_out = reconcile(ReconcileInput {
            live_blob: live.as_ref(),
            stored: &stored,
            lookup_profile: &|token| {
                profile::get_profile(&profile_doer, token)
            },
            now: Utc::now(),
        });

        // Persist inserted/upserted accounts
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
                "claude token not found — run `claude /login` to authenticate".into(),
            ));
        }

        if let Some(ref warn) = reconcile_out.capture_warn {
            eprintln!("{}", warn);
        }

        let total_accounts = reconcile_out.accounts.len()
            + if reconcile_out.live_unstored.is_some() { 1 } else { 0 };
        let _budget_secs = budget_secs(BASE_TIMEOUT_SECS, PER_ACCOUNT_BUDGET_SECS, total_accounts);

        let mut account_results: Vec<AccountResult> = vec![];
        let mut transient_count = 0usize;
        let mut success_count = 0usize;

        // Synthetic live-unstored row
        if let Some(ref live_cred) = reconcile_out.live_unstored {
            let limits_result = self.fetch_limits_fresh(&live_cred.access_token);
            let mut ar = AccountResult {
                email: "(live Claude account)".into(),
                uuid: String::new(),
                plan: String::new(),
                active: true,
                limits: None,
                error: None,
            };
            if let (ok, trans) = record_fetch_outcome(&mut ar, limits_result) {
                if ok { success_count += 1; }
                if trans { transient_count += 1; }
            }
            account_results.push(ar);
        }

        // Per-account sequential fetch
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

            // Refresh if near expiry
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

            let limits_result = self.fetch_limits_cached(stored_access_token(acct), &acct.uuid);
            let (ok, trans) = record_fetch_outcome(&mut ar, limits_result);
            if ok { success_count += 1; }
            if trans { transient_count += 1; }
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
