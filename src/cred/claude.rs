use super::{Credential, CredError};
use serde::Deserialize;

#[derive(Deserialize)]
struct ClaudeCred {
    #[serde(rename = "claudeAiOauth")]
    claude_ai_oauth: ClaudeOAuth,
}

#[derive(Deserialize)]
struct ClaudeOAuth {
    #[serde(rename = "accessToken")]
    access_token: Option<String>,
    #[serde(rename = "refreshToken")]
    refresh_token: Option<String>,
    #[serde(rename = "expiresAt")]
    expires_at: Option<i64>,
}

pub fn parse_claude_cred(data: &[u8]) -> Result<Credential, CredError> {
    let raw: ClaudeCred = serde_json::from_slice(data)
        .map_err(|e| CredError::Other(format!("claude credential is not valid JSON: {e}")))?;
    let access_token = raw
        .claude_ai_oauth
        .access_token
        .filter(|t| !t.is_empty())
        .ok_or(CredError::ClaudeNotFound)?;
    Ok(Credential {
        access_token,
        refresh_token: raw.claude_ai_oauth.refresh_token.unwrap_or_default(),
        expires_at: raw.claude_ai_oauth.expires_at.unwrap_or(0),
        raw: data.to_vec(),
    })
}

pub fn read_claude_credential() -> Result<Credential, CredError> {
    #[cfg(target_os = "macos")]
    {
        super::keychain_darwin::read_claude_credential()
    }
    #[cfg(target_os = "linux")]
    {
        super::keychain_linux::read_claude_credential()
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        Err(CredError::Other(
            "reading Claude credential not supported on this platform".into(),
        ))
    }
}

pub fn write_claude_live_blob(raw_blob: &[u8]) -> Result<(), CredError> {
    #[cfg(target_os = "macos")]
    {
        super::keychain_darwin::write_claude_live_blob(raw_blob)
    }
    #[cfg(target_os = "linux")]
    {
        super::keychain_linux::write_claude_live_blob(raw_blob)
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = raw_blob;
        Err(CredError::Other(
            "writing Claude live credential is not supported on this platform".into(),
        ))
    }
}
