use crate::providers::{classify::default_classify, ProviderError};
use std::time::{Duration, Instant};

fn retry_after_from_resp(resp: &ureq::http::Response<ureq::Body>) -> Option<Duration> {
    let val = resp.headers().get("retry-after")?.to_str().ok()?;
    val.parse::<u64>().ok().map(Duration::from_secs)
}

const MAX_ATTEMPTS: u32 = 3;
const EXPONENTIAL_BASE_MS: u64 = 500;
const EXPONENTIAL_FACTOR: f64 = 2.0;
const JITTER_FRACTION: f64 = 0.20;
const MAX_DELAY_MS: u64 = 5_000;
const RETRY_AFTER_CAP_SECS: u64 = 10;
const MAX_BODY_BYTES: usize = 1 << 20;

fn pick_delay(attempt: u32, retry_after: Option<Duration>) -> Duration {
    if let Some(ra) = retry_after {
        return ra.min(Duration::from_secs(RETRY_AFTER_CAP_SECS));
    }
    let base = EXPONENTIAL_BASE_MS as f64 * EXPONENTIAL_FACTOR.powi(attempt as i32);
    let capped = base.min(MAX_DELAY_MS as f64);
    // simple pseudo-random jitter: use attempt + now millis
    let seed = (std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .subsec_millis()) as f64;
    let jitter = 1.0 - JITTER_FRACTION + 2.0 * JITTER_FRACTION * (seed / 1000.0);
    Duration::from_millis((capped * jitter) as u64)
}

pub fn sanitize_debug_line(s: &str) -> String {
    s.chars()
        .flat_map(|c| {
            if c == '\n' {
                vec!['\\', 'n']
            } else if c == '\r' {
                vec!['\\', 'r']
            } else {
                vec![c]
            }
        })
        .collect()
}

/// Shared HTTP client (ureq v3).
pub struct Doer {
    agent: ureq::Agent,
    pub user_agent: String,
    pub provider_id: String,
    pub extra_headers: Vec<(String, String)>,
    pub debug: Option<Box<dyn Fn(&str) + Send + Sync>>,
}

impl Doer {
    pub fn new(
        user_agent: impl Into<String>,
        provider_id: impl Into<String>,
        extra_headers: Vec<(String, String)>,
        debug: Option<Box<dyn Fn(&str) + Send + Sync>>,
    ) -> Self {
        // http_status_as_error(false) so we can read the body on non-200 responses
        let agent = ureq::Agent::config_builder()
            .http_status_as_error(false)
            .build()
            .new_agent();
        Self {
            agent,
            user_agent: user_agent.into(),
            provider_id: provider_id.into(),
            extra_headers,
            debug,
        }
    }

    fn log(&self, method: &str, url: &str, outcome: &str, elapsed: Duration) {
        if let Some(ref dbg) = self.debug {
            let line = format!(
                "[debug] {}: {} {} -> {} ({}ms)\n",
                self.provider_id,
                method,
                url,
                sanitize_debug_line(outcome),
                elapsed.as_millis()
            );
            dbg(&line);
        }
    }

    fn log_retry(&self, n: u32, delay: Duration, from_header: bool) {
        if let Some(ref dbg) = self.debug {
            let source = if from_header {
                format!("Retry-After: {}", delay.as_secs())
            } else {
                "exponential".to_string()
            };
            let line = format!(
                "[debug] {}: retry {}/{} after {:.1}s ({})\n",
                self.provider_id,
                n + 1,
                MAX_ATTEMPTS,
                delay.as_secs_f64(),
                source
            );
            dbg(&line);
        }
    }

    /// GET url with Bearer token, return deserialized JSON.
    pub fn get<T: serde::de::DeserializeOwned>(
        &self,
        url: &str,
        token: &str,
        timeout_secs: u64,
    ) -> Result<T, ProviderError> {
        let timeout = Duration::from_secs(timeout_secs);
        let mut last_err: Option<ProviderError> = None;

        for attempt in 0..MAX_ATTEMPTS {
            let start = Instant::now();

            // Build request with headers
            let mut req = self
                .agent
                .get(url)
                .header("User-Agent", &self.user_agent)
                .header("Accept", "application/json")
                .header("Authorization", &format!("Bearer {}", token));

            for (k, v) in &self.extra_headers {
                if k.to_lowercase() != "user-agent" {
                    req = req.header(k.as_str(), v.as_str());
                }
            }

            let result = req
                .config()
                .timeout_global(Some(timeout))
                .build()
                .call();

            let elapsed = start.elapsed();
            match result {
                Ok(mut resp) => {
                    let status = resp.status().as_u16();
                    if status != 200 {
                        let retry_after = retry_after_from_resp(&resp);
                        let body = resp.body_mut().read_to_string().unwrap_or_default();
                        self.log("GET", url, &format!("HTTP {status}"), elapsed);
                        let err = default_classify(status, url, &body);
                        let is_transient = err.is_transient();
                        last_err = Some(err);
                        if !is_transient || attempt == MAX_ATTEMPTS - 1 {
                            break;
                        }
                        let from_header = retry_after.is_some();
                        let delay = pick_delay(attempt, retry_after);
                        self.log_retry(attempt, delay, from_header);
                        std::thread::sleep(delay);
                        continue;
                    }
                    let body = resp.body_mut().read_to_string().unwrap_or_default();
                    if body.len() > MAX_BODY_BYTES {
                        return Err(ProviderError::Other(format!(
                            "response body from {} exceeds {} bytes",
                            url, MAX_BODY_BYTES
                        )));
                    }
                    self.log("GET", url, "ok", elapsed);
                    return serde_json::from_str(&body).map_err(|e| {
                        ProviderError::Other(format!(
                            "non-JSON response from {}: {}: {}",
                            url,
                            e,
                            &body[..body.len().min(200)]
                        ))
                    });
                }
                Err(e) => {
                    let elapsed = start.elapsed();
                    let msg = e.to_string();
                    self.log("GET", url, &msg, elapsed);
                    let err = ProviderError::Transient(msg);
                    last_err = Some(err);
                    if attempt == MAX_ATTEMPTS - 1 {
                        break;
                    }
                    let delay = pick_delay(attempt, None);
                    self.log_retry(attempt, delay, false);
                    std::thread::sleep(delay);
                }
            }
        }
        Err(last_err.unwrap_or_else(|| ProviderError::Other("request failed".into())))
    }

    /// POST form-encoded body, return deserialized JSON.
    pub fn post<T: serde::de::DeserializeOwned>(
        &self,
        url: &str,
        form: &[(&str, &str)],
        timeout_secs: u64,
    ) -> Result<T, ProviderError> {
        let timeout = Duration::from_secs(timeout_secs);
        let mut last_err: Option<ProviderError> = None;

        for attempt in 0..MAX_ATTEMPTS {
            let start = Instant::now();

            let mut req = self
                .agent
                .post(url)
                .header("User-Agent", &self.user_agent)
                .header("Accept", "application/json");

            for (k, v) in &self.extra_headers {
                let kl = k.to_lowercase();
                if kl != "user-agent" && kl != "content-type" {
                    req = req.header(k.as_str(), v.as_str());
                }
            }

            let result = req
                .config()
                .timeout_global(Some(timeout))
                .build()
                .send_form(form.iter().copied());

            let elapsed = start.elapsed();
            match result {
                Ok(mut resp) => {
                    let status = resp.status().as_u16();
                    if status != 200 {
                        let retry_after = retry_after_from_resp(&resp);
                        let body = resp.body_mut().read_to_string().unwrap_or_default();
                        self.log("POST", url, &format!("HTTP {status}"), elapsed);
                        let err = classify_post_error(status, url, &body);
                        let is_transient = err.is_transient();
                        last_err = Some(err);
                        if !is_transient || attempt == MAX_ATTEMPTS - 1 {
                            break;
                        }
                        let from_header = retry_after.is_some();
                        let delay = pick_delay(attempt, retry_after);
                        self.log_retry(attempt, delay, from_header);
                        std::thread::sleep(delay);
                        continue;
                    }
                    let body = resp.body_mut().read_to_string().unwrap_or_default();
                    if body.len() > MAX_BODY_BYTES {
                        return Err(ProviderError::Other(format!(
                            "response body from {} exceeds {} bytes",
                            url, MAX_BODY_BYTES
                        )));
                    }
                    self.log("POST", url, "ok", elapsed);
                    return serde_json::from_str(&body).map_err(|e| {
                        ProviderError::Other(format!(
                            "non-JSON response from {}: {}: {}",
                            url,
                            e,
                            &body[..body.len().min(200)]
                        ))
                    });
                }
                Err(e) => {
                    let msg = e.to_string();
                    self.log("POST", url, &msg, elapsed);
                    let err = ProviderError::Transient(msg);
                    last_err = Some(err);
                    if attempt == MAX_ATTEMPTS - 1 {
                        break;
                    }
                    let delay = pick_delay(attempt, None);
                    self.log_retry(attempt, delay, false);
                    std::thread::sleep(delay);
                }
            }
        }
        Err(last_err.unwrap_or_else(|| ProviderError::Other("request failed".into())))
    }
}

fn classify_post_error(status: u16, url: &str, body: &str) -> ProviderError {
    if status == 400 {
        if body.contains("invalid_grant") {
            return ProviderError::AuthDenied(format!(
                "refresh token rejected (invalid_grant) from {}",
                url
            ));
        }
        if body.contains("invalid_client") {
            return ProviderError::Other(format!(
                "refresh endpoint rejected request (invalid_client) from {}",
                url
            ));
        }
        if body.contains("already been used") {
            return ProviderError::AuthDenied(format!(
                "stale refresh token (already been used) from {}",
                url
            ));
        }
    }
    default_classify(status, url, body)
}
