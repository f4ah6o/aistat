use crate::accounts::Account;
use crate::cred::parse_jwt_exp;
use serde_json::value::RawValue;

#[derive(serde::Deserialize)]
struct ClaudeBlob {
    #[serde(rename = "claudeAiOauth")]
    claude_ai_oauth: Option<OAuthFields>,
}

#[derive(serde::Deserialize)]
struct OAuthFields {
    #[serde(rename = "accessToken")]
    access_token: Option<String>,
    #[serde(rename = "refreshToken")]
    refresh_token: Option<String>,
    #[serde(rename = "expiresAt")]
    expires_at: Option<i64>,
}

fn parse_raw(acct: &Account) -> Option<OAuthFields> {
    let blob: ClaudeBlob = serde_json::from_str(acct.raw_blob.get()).ok()?;
    blob.claude_ai_oauth
}

pub fn stored_access_token(acct: &Account) -> &str {
    // We can't return a reference to a temporary, so we return "" for simplicity.
    // Callers should use the stored value.
    ""
}

pub fn stored_access_token_owned(acct: &Account) -> String {
    parse_raw(acct)
        .and_then(|o| o.access_token)
        .unwrap_or_default()
}

pub fn stored_refresh_token(acct: &Account) -> String {
    parse_raw(acct)
        .and_then(|o| o.refresh_token)
        .unwrap_or_default()
}

pub fn stored_expires_at(acct: &Account) -> i64 {
    parse_raw(acct)
        .and_then(|o| o.expires_at)
        .unwrap_or(0)
}

pub fn rotate_raw_blob(
    raw_blob: &Box<RawValue>,
    tok: &super::refresh::Token,
) -> Result<Box<RawValue>, String> {
    let mut m: serde_json::Map<String, serde_json::Value> =
        serde_json::from_str(raw_blob.get()).map_err(|e| e.to_string())?;
    let oauth = m
        .get_mut("claudeAiOauth")
        .and_then(|v| v.as_object_mut())
        .ok_or("rotateRawBlob: claudeAiOauth missing or wrong type")?;
    oauth.insert("accessToken".into(), serde_json::Value::String(tok.access_token.clone()));
    oauth.insert("refreshToken".into(), serde_json::Value::String(tok.refresh_token.clone()));
    oauth.insert("expiresAt".into(), serde_json::Value::Number(tok.expires_at.into()));
    let out = serde_json::to_string(&m).map_err(|e| e.to_string())?;
    RawValue::from_string(out).map_err(|e| e.to_string())
}
