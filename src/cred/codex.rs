use super::{Credential, CredError};
use serde::Deserialize;
use std::path::PathBuf;

#[derive(Deserialize)]
struct CodexAuth {
    tokens: CodexTokens,
}

#[derive(Deserialize)]
struct CodexTokens {
    access_token: Option<String>,
    refresh_token: Option<String>,
    id_token: Option<String>,
}

fn codex_auth_path() -> Result<PathBuf, CredError> {
    let home = dirs::home_dir().ok_or_else(|| {
        CredError::Other(format!(
            "{}: cannot resolve home directory",
            CredError::CodexNotFound
        ))
    })?;
    Ok(home.join(".codex").join("auth.json"))
}

pub fn parse_codex_cred(data: &[u8]) -> Result<Credential, CredError> {
    let raw: CodexAuth = serde_json::from_slice(data)
        .map_err(|e| CredError::Other(format!("codex auth.json is not valid JSON: {e}")))?;
    let access_token = raw
        .tokens
        .access_token
        .filter(|t| !t.is_empty())
        .ok_or(CredError::CodexNotFound)?;
    Ok(Credential {
        access_token,
        refresh_token: raw.tokens.refresh_token.unwrap_or_default(),
        expires_at: 0, // decoded from JWT on demand
        raw: data.to_vec(),
    })
}

pub fn read_codex_credential() -> Result<Credential, CredError> {
    let path = codex_auth_path()?;
    let data = std::fs::read(&path).map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            CredError::CodexNotFound
        } else {
            CredError::Other(format!("reading codex auth.json: {e}"))
        }
    })?;
    parse_codex_cred(&data)
}

pub fn write_codex_live_blob(raw_blob: &[u8]) -> Result<(), CredError> {
    let path = codex_auth_path()?;
    let dir = path.parent().unwrap();
    std::fs::create_dir_all(dir)
        .map_err(|e| CredError::Other(format!("creating codex auth directory: {e}")))?;

    let tmp_path = dir.join(format!(".auth-{}.json", std::process::id()));
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::write(&tmp_path, raw_blob)
            .map_err(|e| CredError::Other(format!("writing codex auth file: {e}")))?;
        std::fs::set_permissions(&tmp_path, std::fs::Permissions::from_mode(0o600))
            .map_err(|e| CredError::Other(format!("setting codex auth file mode: {e}")))?;
    }
    std::fs::rename(&tmp_path, &path)
        .map_err(|e| CredError::Other(format!("installing codex auth file: {e}")))?;
    Ok(())
}

/// Extract the id_token string from the raw codex auth.json blob.
pub fn extract_id_token(raw: &[u8]) -> Option<String> {
    let auth: CodexAuth = serde_json::from_slice(raw).ok()?;
    auth.tokens.id_token.filter(|s| !s.is_empty())
}
