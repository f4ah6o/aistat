use super::{Limit, Provider, ProviderError, ProviderOutput, ISSUE_TRACKER_URL};
use crate::cred;
use chrono::Utc;
use regex::Regex;
use std::collections::BTreeMap;
use std::sync::OnceLock;
use std::time::Duration;

const DASHBOARD_URL_TEMPLATE: &str = "https://opencode.ai/workspace/{}/go";
const FETCH_TIMEOUT_SECS: u64 = 10;

type DebugFn = Option<Box<dyn Fn(&str) + Send + Sync>>;

pub fn default_user_agent(version: &str) -> String {
    std::env::var("AISTAT_OPENCODEGO_USER_AGENT").unwrap_or_else(|_| {
        format!("agent-usage/{} (opencodego; https://github.com/f4ah6o/aistat)", version)
    })
}

// Compiled once at first use.
fn re_window_block() -> &'static Regex {
    static RE: OnceLock<Regex> = OnceLock::new();
    RE.get_or_init(|| {
        Regex::new(r#""(rolling|weekly|monthly)"\s*:\s*\{([^}]*)\}"#).unwrap()
    })
}

fn re_usage_pct() -> &'static Regex {
    static RE: OnceLock<Regex> = OnceLock::new();
    RE.get_or_init(|| Regex::new(r#""usagePercent"\s*:\s*(\d+(?:\.\d+)?)"#).unwrap())
}

fn re_reset_sec() -> &'static Regex {
    static RE: OnceLock<Regex> = OnceLock::new();
    RE.get_or_init(|| Regex::new(r#""resetInSec"\s*:\s*(\d+)"#).unwrap())
}

fn window_key(name: &str) -> &'static str {
    match name {
        "rolling" => "five_hour",
        "weekly" => "seven_day",
        "monthly" => "monthly",
        _ => "unknown",
    }
}

pub struct OpenCodeGoClient {
    user_agent: String,
    debug: DebugFn,
}

impl OpenCodeGoClient {
    pub fn new(
        user_agent: String,
        debug: DebugFn,
    ) -> Self {
        Self { user_agent, debug }
    }

    fn log(&self, msg: &str) {
        if let Some(d) = &self.debug {
            d(&format!("[debug] opencodego: {}\n", msg));
        }
    }
}

impl Provider for OpenCodeGoClient {
    fn id(&self) -> &str {
        "opencodego"
    }

    fn fetch(&self) -> Result<ProviderOutput, ProviderError> {
        let (ws_id, cookie) = cred::opencode::read_opencode_config().map_err(|e| {
            match e {
                cred::CredError::OpenCodeGoNotFound => ProviderError::AuthMissing(e.to_string()),
                _ => ProviderError::Other(e.to_string()),
            }
        })?;

        let url = DASHBOARD_URL_TEMPLATE.replacen("{}", &ws_id, 1);
        self.log(&format!("GET {}", url));

        let agent = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(FETCH_TIMEOUT_SECS)))
            .build()
            .new_agent();

        let resp = agent
            .get(&url)
            .header("User-Agent", &self.user_agent)
            .header("Cookie", &format!("auth={}", cookie))
            .header("Accept", "text/html,application/xhtml+xml")
            .call();

        let resp = match resp {
            Ok(r) => r,
            Err(ureq::Error::StatusCode(code)) => {
                self.log(&format!("HTTP {}", code));
                return match code {
                    401 | 403 => Err(ProviderError::AuthDenied(format!(
                        "HTTP {} from {} — re-export OPENCODE_GO_AUTH_COOKIE",
                        code, url
                    ))),
                    429 | 500..=599 => Err(ProviderError::Transient(format!(
                        "HTTP {} from {}", code, url
                    ))),
                    _ => Err(ProviderError::Other(format!(
                        "HTTP {} from {} — please file an issue at {}",
                        code, url, ISSUE_TRACKER_URL
                    ))),
                };
            }
            Err(e) => {
                self.log(&format!("request error: {}", e));
                return Err(ProviderError::Transient(format!("opencodego: {}", e)));
            }
        };

        let status = resp.status();
        self.log(&format!("HTTP {} ok", status));

        let body = resp.into_body().read_to_string().map_err(|e| {
            ProviderError::Transient(format!("opencodego: reading response: {}", e))
        })?;

        parse_dashboard(&body).map(|limits| ProviderOutput {
            limits: Some(limits),
            accounts: vec![],
        })
    }
}

fn parse_dashboard(body: &str) -> Result<BTreeMap<String, Limit>, ProviderError> {
    let now = Utc::now();
    let mut limits = BTreeMap::new();

    for cap in re_window_block().captures_iter(body) {
        let window_name = cap.get(1).unwrap().as_str();
        let block = cap.get(2).unwrap().as_str();

        let used_pct = match re_usage_pct().captures(block) {
            Some(m) => m[1].parse::<f64>().unwrap_or(0.0),
            None => continue,
        };

        let reset_sec = match re_reset_sec().captures(block) {
            Some(m) => m[1].parse::<i64>().unwrap_or(0).max(0),
            None => continue,
        };

        let key = window_key(window_name);
        let resets_at = now + chrono::Duration::seconds(reset_sec);
        limits.insert(
            key.to_string(),
            Limit {
                used_percent: used_pct,
                remaining_percent: 100.0 - used_pct,
                resets_at,
                reset_after_seconds: reset_sec,
            },
        );
    }

    if limits.is_empty() {
        return Err(ProviderError::Other(format!(
            "opencodego: no usage window data found in dashboard response — please file an issue at {}",
            ISSUE_TRACKER_URL
        )));
    }

    Ok(limits)
}
