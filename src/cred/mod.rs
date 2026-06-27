pub mod claude;
pub mod codex;
pub mod github;
pub mod opencode;

#[cfg(target_os = "macos")]
pub mod chrome_cookie_darwin;
#[cfg(target_os = "macos")]
pub mod keychain_darwin;
#[cfg(target_os = "linux")]
pub mod keychain_linux;

use thiserror::Error;

#[derive(Debug, Error)]
pub enum CredError {
    #[error("claude token not found — run `claude /login` to authenticate")]
    ClaudeNotFound,
    #[error("codex token not found at ~/.codex/auth.json — run `codex login`")]
    CodexNotFound,
    #[error("GitHub token not found — run `gh auth login`")]
    GitHubNotFound,
    #[error("opencode go config not found — set OPENCODE_GO_WORKSPACE_ID and OPENCODE_GO_AUTH_COOKIE, or run `agent-usage opencodego setup`")]
    OpenCodeGoNotFound,
    #[error("{0}")]
    Other(String),
}

#[derive(Debug, Clone)]
pub struct Credential {
    pub access_token: String,
    pub refresh_token: String,
    /// Milliseconds since epoch; 0 if absent.
    pub expires_at: i64,
    pub raw: Vec<u8>,
}

/// Decode the base64url payload of a JWT and return (sub, email, exp_sec).
/// Signature is NOT verified.
pub fn parse_codex_id_token(
    id_token: &str,
) -> Result<(String, String, i64), CredError> {
    if id_token.is_empty() {
        return Err(CredError::Other("codex id_token is empty".into()));
    }
    let parts: Vec<&str> = id_token.split('.').collect();
    if parts.len() != 3 || parts.iter().any(|p| p.is_empty()) {
        return Err(CredError::Other(format!(
            "codex id_token: expected 3 non-empty segments, got {}",
            parts.len()
        )));
    }
    let payload = base64::Engine::decode(
        &base64::engine::general_purpose::URL_SAFE_NO_PAD,
        parts[1],
    )
    .map_err(|e| CredError::Other(format!("codex id_token: payload base64 decode: {e}")))?;

    #[derive(serde::Deserialize)]
    struct Claims {
        sub: Option<String>,
        email: Option<String>,
        exp: Option<f64>,
    }
    let claims: Claims = serde_json::from_slice(&payload)
        .map_err(|e| CredError::Other(format!("codex id_token: payload JSON: {e}")))?;

    let sub = claims
        .sub
        .filter(|s| !s.is_empty())
        .ok_or_else(|| CredError::Other("codex id_token: missing sub claim".into()))?;

    Ok((
        sub,
        claims.email.unwrap_or_default(),
        claims.exp.unwrap_or(0.0) as i64,
    ))
}

/// Decode the exp claim from any JWT without verifying the signature.
pub fn parse_jwt_exp(token: &str) -> Option<i64> {
    let parts: Vec<&str> = token.split('.').collect();
    if parts.len() != 3 || parts.iter().any(|p| p.is_empty()) {
        return None;
    }
    let payload = base64::Engine::decode(
        &base64::engine::general_purpose::URL_SAFE_NO_PAD,
        parts[1],
    )
    .ok()?;

    #[derive(serde::Deserialize)]
    struct Claims {
        exp: Option<f64>,
    }
    let claims: Claims = serde_json::from_slice(&payload).ok()?;
    let exp = claims.exp?;
    if exp <= 0.0 {
        return None;
    }
    Some(exp as i64)
}
