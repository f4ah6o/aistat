use super::{Limit, Provider, ProviderError, ProviderOutput, ISSUE_TRACKER_URL};
use crate::{cred, httpx::Doer};
use chrono::Utc;
use serde::Deserialize;
use std::collections::BTreeMap;

const ENDPOINT: &str = "https://api.github.com/copilot_internal/user";
const ACCEPT_HEADER: &str = "application/vnd.github+json";
const TIMEOUT_SECS: u64 = 10;
const QUOTA_KEY: &str = "premium_interactions";

pub fn default_user_agent(version: &str) -> String {
    format!(
        "agent-usage/{} (copilot; https://github.com/f4ah6o/aistat)",
        version
    )
}

#[derive(Deserialize)]
struct QuotaSnapshot {
    entitlement: f64,
    percent_remaining: f64,
    unlimited: bool,
}

#[derive(Deserialize)]
struct CopilotUserResp {
    quota_reset_date_utc: Option<chrono::DateTime<Utc>>,
    quota_snapshots: Option<BTreeMap<String, QuotaSnapshot>>,
}

pub struct CopilotClient {
    doer: Doer,
}

impl CopilotClient {
    pub fn new(
        user_agent: String,
        debug: Option<Box<dyn Fn(&str) + Send + Sync>>,
    ) -> Self {
        let doer = Doer::new(
            user_agent,
            "copilot",
            vec![("Accept".into(), ACCEPT_HEADER.into())],
            debug,
        );
        Self { doer }
    }
}

impl Provider for CopilotClient {
    fn id(&self) -> &str {
        "copilot"
    }

    fn fetch(&self) -> Result<ProviderOutput, ProviderError> {
        let token = cred::github::read_github_token()
            .map_err(|e| ProviderError::AuthMissing(e.to_string()))?;

        let resp: CopilotUserResp = self.doer.get(ENDPOINT, &token, TIMEOUT_SECS)?;

        let snapshots = match resp.quota_snapshots {
            Some(s) => s,
            None => return Ok(ProviderOutput { limits: Some(BTreeMap::new()), accounts: vec![] }),
        };

        let pool = match snapshots.get(QUOTA_KEY) {
            Some(p) => p,
            None => {
                if !snapshots.is_empty() {
                    eprintln!(
                        "copilot: quota_snapshots present but {:?} key missing — GitHub may have renamed the quota; please file an issue at {}",
                        QUOTA_KEY, ISSUE_TRACKER_URL
                    );
                }
                return Ok(ProviderOutput { limits: Some(BTreeMap::new()), accounts: vec![] });
            }
        };

        if pool.unlimited || pool.entitlement <= 0.0 {
            return Ok(ProviderOutput { limits: Some(BTreeMap::new()), accounts: vec![] });
        }

        let used = (100.0 - pool.percent_remaining).clamp(0.0, 100.0);
        let now = Utc::now();
        let reset = resp.quota_reset_date_utc.unwrap_or(now);
        let secs = (reset - now).num_seconds().max(0);

        let mut limits = BTreeMap::new();
        limits.insert(
            "month".to_string(),
            Limit {
                used_percent: used,
                remaining_percent: 100.0 - used,
                resets_at: reset,
                reset_after_seconds: secs,
            },
        );
        Ok(ProviderOutput { limits: Some(limits), accounts: vec![] })
    }
}
