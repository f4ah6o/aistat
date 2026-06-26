pub mod classify;
pub mod claude;
pub mod codex;
pub mod copilot;
pub mod multiaccount;
pub mod usagecache;

use chrono::{DateTime, Utc};
use serde::{Serialize, Serializer};
use std::collections::BTreeMap;
use thiserror::Error;

pub const ISO8601_FMT: &str = "%Y-%m-%dT%H:%M:%S%:z";

pub const KNOWN_PROVIDER_IDS: &[&str] = &["claude", "codex"];

pub const PROJECT_URL: &str = "https://github.com/f4ah6o/aistat";
pub const ISSUE_TRACKER_URL: &str = "https://github.com/f4ah6o/aistat/issues";

pub fn provider_title(id: &str) -> String {
    let mut c = id.chars();
    match c.next() {
        None => String::new(),
        Some(f) => f.to_uppercase().collect::<String>() + c.as_str(),
    }
}

#[derive(Debug, Error)]
pub enum ProviderError {
    #[error("auth missing: {0}")]
    AuthMissing(String),
    #[error("auth denied: {0}")]
    AuthDenied(String),
    #[error("transient failure: {0}")]
    Transient(String),
    #[error("{0}")]
    Other(String),
}

impl ProviderError {
    pub fn is_transient(&self) -> bool {
        matches!(self, ProviderError::Transient(_))
    }
    pub fn is_auth_denied(&self) -> bool {
        matches!(self, ProviderError::AuthDenied(_))
    }
    pub fn is_auth_missing(&self) -> bool {
        matches!(self, ProviderError::AuthMissing(_))
    }
}

#[derive(Debug, Clone)]
pub struct Limit {
    pub used_percent: f64,
    pub remaining_percent: f64,
    pub resets_at: DateTime<Utc>,
    pub reset_after_seconds: i64,
}

fn round_pct(f: f64) -> f64 {
    (f * 100.0).round() / 100.0
}

impl Serialize for Limit {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap;
        let mut map = s.serialize_map(Some(4))?;
        map.serialize_entry("used_percent", &round_pct(self.used_percent))?;
        map.serialize_entry("remaining_percent", &round_pct(self.remaining_percent))?;
        map.serialize_entry(
            "resets_at",
            &self.resets_at.format(ISO8601_FMT).to_string(),
        )?;
        map.serialize_entry("reset_after_seconds", &self.reset_after_seconds)?;
        map.end()
    }
}

#[derive(Debug, Default)]
pub struct ProviderOutput {
    pub limits: Option<BTreeMap<String, Limit>>,
    pub accounts: Vec<AccountResult>,
}

#[derive(Debug, Clone)]
pub struct AccountResult {
    pub email: String,
    pub uuid: String,
    pub plan: String,
    pub active: bool,
    pub limits: Option<BTreeMap<String, Limit>>,
    pub error: Option<String>,
}

impl Serialize for AccountResult {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap;
        let error_present = self.error.as_deref().map(|e| !e.is_empty()).unwrap_or(false);
        let n = 4 + if error_present { 1 } else { 0 };
        let mut map = s.serialize_map(Some(n))?;
        map.serialize_entry("email", &self.email)?;
        map.serialize_entry("plan", &self.plan)?;
        map.serialize_entry("active", &self.active)?;
        map.serialize_entry("limits", &self.limits)?;
        if error_present {
            map.serialize_entry("error", &self.error)?;
        }
        map.end()
    }
}

#[derive(Debug)]
pub struct ProviderResult {
    pub limits: Option<BTreeMap<String, Limit>>,
    pub accounts: Vec<AccountResult>,
    pub error: Option<String>,
}

impl Serialize for ProviderResult {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap;
        let error_present = self.error.as_deref().map(|e| !e.is_empty()).unwrap_or(false);
        if !self.accounts.is_empty() {
            let n = 1 + if error_present { 1 } else { 0 };
            let mut map = s.serialize_map(Some(n))?;
            map.serialize_entry("accounts", &self.accounts)?;
            if error_present {
                map.serialize_entry("error", &self.error)?;
            }
            return map.end();
        }
        let n = 1 + if error_present { 1 } else { 0 };
        let mut map = s.serialize_map(Some(n))?;
        map.serialize_entry("limits", &self.limits)?;
        if error_present {
            map.serialize_entry("error", &self.error)?;
        }
        map.end()
    }
}

#[derive(Debug)]
pub struct Report {
    pub checked_at: DateTime<Utc>,
    pub providers: BTreeMap<String, ProviderResult>,
}

impl Serialize for Report {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeMap;
        let mut map = s.serialize_map(Some(2))?;
        map.serialize_entry(
            "checked_at",
            &self.checked_at.format(ISO8601_FMT).to_string(),
        )?;
        map.serialize_entry("providers", &self.providers)?;
        map.end()
    }
}

pub trait Provider: Send + Sync {
    fn id(&self) -> &str;
    fn fetch(&self) -> Result<ProviderOutput, ProviderError>;
}
