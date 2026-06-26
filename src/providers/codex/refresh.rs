use crate::httpx::Doer;
use crate::providers::ProviderError;
use serde::Deserialize;
use std::sync::Arc;

const REFRESH_ENDPOINT: &str = "https://auth.openai.com/oauth/token";
const CLIENT_ID: &str = "app_EMoamEEZ73f0CkXaXp7hrann";
const TIMEOUT_SECS: u64 = 5;

#[derive(Debug, Clone)]
pub struct Token {
    pub access_token: String,
    pub refresh_token: String,
    pub id_token: String,
}

#[derive(Deserialize)]
struct TokenResp {
    access_token: Option<String>,
    refresh_token: Option<String>,
    id_token: Option<String>,
    error: Option<String>,
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
        let resp: TokenResp = self.doer.post(REFRESH_ENDPOINT, &form, TIMEOUT_SECS)
            .map_err(|e| {
                // Map invalid_grant specifically
                let msg = e.to_string();
                if msg.contains("invalid_grant") {
                    ProviderError::AuthDenied("refresh token rejected (invalid_grant)".into())
                } else {
                    e
                }
            })?;

        if let Some(err) = resp.error {
            return Err(ProviderError::AuthDenied(format!("refresh error: {}", err)));
        }

        let access_token = resp
            .access_token
            .filter(|t| !t.is_empty())
            .ok_or_else(|| {
                ProviderError::Other("refresh endpoint returned non-OAuth response".into())
            })?;

        Ok(Token {
            access_token,
            refresh_token: resp.refresh_token.unwrap_or(refresh_token),
            id_token: resp.id_token.unwrap_or_default(),
        })
    }
}
