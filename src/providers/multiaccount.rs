use super::{AccountResult, Limit, ProviderError};
use chrono::{DateTime, Utc};
use std::collections::BTreeMap;

pub fn sort_account_results(accounts: &mut Vec<AccountResult>) {
    accounts.sort_by(|a, b| {
        b.active
            .cmp(&a.active)
            .then_with(|| a.email.cmp(&b.email))
    });
}

/// Records a fetch outcome into ar. Returns (success, is_transient).
pub fn record_fetch_outcome(
    ar: &mut AccountResult,
    result: Result<BTreeMap<String, Limit>, ProviderError>,
) -> (bool, bool) {
    match result {
        Ok(limits) => {
            ar.limits = Some(limits);
            (true, false)
        }
        Err(e) => {
            let is_transient = e.is_transient();
            ar.error = Some(e.to_string());
            ar.limits = None;
            (false, is_transient)
        }
    }
}

/// Recomputes reset_after_seconds relative to now for each limit in the map.
pub fn recompute_reset_after(
    limits: BTreeMap<String, Limit>,
    now: DateTime<Utc>,
) -> BTreeMap<String, Limit> {
    limits
        .into_iter()
        .map(|(k, mut l)| {
            let secs = (l.resets_at - now).num_seconds().max(0);
            l.reset_after_seconds = secs;
            (k, l)
        })
        .collect()
}

/// Computes the pool timeout: base + per_account * count (in seconds).
pub fn budget_secs(base_secs: u64, per_account_secs: u64, count: usize) -> u64 {
    base_secs + per_account_secs * count as u64
}
