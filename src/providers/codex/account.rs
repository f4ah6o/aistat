use crate::accounts::Account;
use crate::cred::parse_jwt_exp;
use serde_json::value::RawValue;

#[derive(serde::Deserialize)]
struct CodexBlob {
    tokens: Option<CodexTokens>,
}

#[derive(serde::Deserialize)]
struct CodexTokens {
    access_token: Option<String>,
    refresh_token: Option<String>,
    id_token: Option<String>,
}

fn parse_raw(acct: &Account) -> Option<CodexTokens> {
    let blob: CodexBlob = serde_json::from_str(acct.raw_blob.get()).ok()?;
    blob.tokens
}

pub fn stored_access_token_owned(acct: &Account) -> String {
    parse_raw(acct)
        .and_then(|t| t.access_token)
        .unwrap_or_default()
}

pub fn stored_refresh_token(acct: &Account) -> String {
    parse_raw(acct)
        .and_then(|t| t.refresh_token)
        .unwrap_or_default()
}

/// Returns expiry from the access_token JWT exp claim (ms), or 0 if absent.
pub fn stored_expires_at(acct: &Account) -> i64 {
    let at = stored_access_token_owned(acct);
    if at.is_empty() { return 0; }
    parse_jwt_exp(&at).map(|s| s * 1000).unwrap_or(0)
}

pub fn rotate_raw_blob(
    raw_blob: &Box<RawValue>,
    tok: &super::refresh::Token,
) -> Result<Box<RawValue>, String> {
    let mut m: serde_json::Map<String, serde_json::Value> =
        serde_json::from_str(raw_blob.get()).map_err(|e| e.to_string())?;
    let tokens = m
        .get_mut("tokens")
        .and_then(|v| v.as_object_mut())
        .ok_or("rotateRawBlob: tokens missing or wrong type")?;
    tokens.insert("access_token".into(), serde_json::Value::String(tok.access_token.clone()));
    tokens.insert("refresh_token".into(), serde_json::Value::String(tok.refresh_token.clone()));
    if !tok.id_token.is_empty() {
        tokens.insert("id_token".into(), serde_json::Value::String(tok.id_token.clone()));
    }
    let out = serde_json::to_string(&m).map_err(|e| e.to_string())?;
    RawValue::from_string(out).map_err(|e| e.to_string())
}
