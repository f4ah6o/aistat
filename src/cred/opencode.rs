use super::CredError;
use std::env;
use std::path::PathBuf;

/// Resolution order:
/// 1. OPENCODE_GO_WORKSPACE_ID + OPENCODE_GO_AUTH_COOKIE env vars
/// 2. OPENCODE_GO_CONFIG_FILE env var (explicit path)
/// 3. ~/.config/opencode-bar/opencode-go.json
/// 4. ~/.config/opencode-quota/opencode-go.json
/// 5. (macOS only) Auto-extract from Chrome/Brave/Arc cookie store
pub fn read_opencode_config() -> Result<(String, String), CredError> {
    // 1. Environment variables.
    let ws_id = env::var("OPENCODE_GO_WORKSPACE_ID").unwrap_or_default();
    let cookie = env::var("OPENCODE_GO_AUTH_COOKIE").unwrap_or_default();
    if !ws_id.is_empty() && !cookie.is_empty() {
        return Ok((ws_id, cookie));
    }

    // 2. Explicit config file path override.
    if let Ok(p) = env::var("OPENCODE_GO_CONFIG_FILE") {
        if !p.is_empty() {
            return read_config_file(PathBuf::from(p));
        }
    }

    // 3. Standard config paths.
    let cfg_dir = dirs::config_dir().ok_or(CredError::OpenCodeGoNotFound)?;
    let candidates = [
        cfg_dir.join("opencode-bar").join("opencode-go.json"),
        cfg_dir.join("opencode-quota").join("opencode-go.json"),
    ];
    for path in &candidates {
        match read_config_file(path.clone()) {
            Ok(pair) => return Ok(pair),
            Err(CredError::OpenCodeGoNotFound) => continue,
            Err(e) => return Err(e),
        }
    }

    // 4. macOS: auto-extract from Chromium browser cookie store.
    #[cfg(target_os = "macos")]
    if let Some(pair) = super::chrome_cookie_darwin::extract_from_chrome() {
        return Ok(pair);
    }

    Err(CredError::OpenCodeGoNotFound)
}

#[derive(serde::Deserialize)]
struct ConfigFile {
    #[serde(rename = "workspaceId")]
    workspace_id: String,
    #[serde(rename = "authCookie")]
    auth_cookie: String,
}

fn read_config_file(path: PathBuf) -> Result<(String, String), CredError> {
    let data = std::fs::read(&path).map_err(|_| CredError::OpenCodeGoNotFound)?;
    let cfg: ConfigFile = serde_json::from_slice(&data).map_err(|e| {
        CredError::Other(format!(
            "opencode go config {}: invalid JSON: {}",
            path.display(),
            e
        ))
    })?;
    if cfg.workspace_id.is_empty() || cfg.auth_cookie.is_empty() {
        return Err(CredError::Other(format!(
            "opencode go config {}: missing workspaceId or authCookie",
            path.display()
        )));
    }
    Ok((cfg.workspace_id, cfg.auth_cookie))
}
