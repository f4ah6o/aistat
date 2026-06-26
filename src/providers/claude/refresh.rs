use crate::httpx::Doer;
use crate::providers::ProviderError;
use chrono::Utc;
use serde::Deserialize;
use std::sync::Arc;

const REFRESH_ENDPOINT: &str = "https://platform.claude.com/v1/oauth/token";
const CLIENT_ID: &str = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
const TIMEOUT_SECS: u64 = 5;

#[derive(Debug, Clone)]
pub struct Token {
    pub access_token: String,
    pub refresh_token: String,
    pub expires_at: i64,
}

#[derive(Deserialize)]
struct TokenResp {
    access_token: Option<String>,
    refresh_token: Option<String>,
    expires_in: Option<i64>,
}

pub struct RefreshClient {
    doer: Arc<Doer>,
}

impl RefreshClient {
    pub fn new(doer: Arc<Doer>) -> Self {
        Self { doer }
    }

    pub fn exchange(&self, refresh_token: String) -> Result<Token, ProviderError> {
        let form = [
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token.as_str()),
            ("client_id", CLIENT_ID),
        ];
        let resp: TokenResp = self.doer.post(REFRESH_ENDPOINT, &form, TIMEOUT_SECS)?;
        let access_token = resp
            .access_token
            .filter(|t| !t.is_empty())
            .ok_or_else(|| {
                ProviderError::Other("refresh endpoint returned non-OAuth response".into())
            })?;
        let new_refresh = resp.refresh_token.unwrap_or(refresh_token);
        let expires_at = resp.expires_in.map(|secs| {
            Utc::now().timestamp_millis() + secs * 1000
        }).unwrap_or(0);
        Ok(Token {
            access_token,
            refresh_token: new_refresh,
            expires_at,
        })
    }
}
