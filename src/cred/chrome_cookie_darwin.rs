/// Auto-extract the OpenCode Go auth cookie and workspace ID from Chromium-based
/// browsers on macOS. Tries Chrome, Brave, and Arc in order.
///
/// Cookie decryption follows the standard Chromium macOS AES-128-CBC scheme:
///   key  = PBKDF2-SHA1(keychain_secret, salt="saltysalt", iters=1003, dklen=16)
///   iv   = b' ' * 16
///   data = AES-128-CBC-decrypt(cookie_encrypted_value[3:], key, iv)
///          (strip 3-byte "v10" prefix before decrypting)
use aes::cipher::{BlockDecryptMut, KeyIvInit, block_padding::Pkcs7};
use pbkdf2::pbkdf2_hmac;
use sha1::Sha1;
use std::path::Path;
use std::process::Command;

type Aes128CbcDec = cbc::Decryptor<aes::Aes128>;

struct Browser {
    #[allow(dead_code)]
    name: &'static str,
    profile_dir: &'static str,
    keychain_service: &'static str,
    keychain_account: &'static str,
}

const BROWSERS: &[Browser] = &[
    Browser {
        name: "Chrome",
        profile_dir: "Google/Chrome",
        keychain_service: "Chrome Safe Storage",
        keychain_account: "Chrome",
    },
    Browser {
        name: "Brave",
        profile_dir: "BraveSoftware/Brave-Browser",
        keychain_service: "Brave Safe Storage",
        keychain_account: "Brave",
    },
    Browser {
        name: "Arc",
        profile_dir: "Arc/User Data",
        keychain_service: "Arc Safe Storage",
        keychain_account: "Arc",
    },
];

/// Returns (workspace_id, auth_cookie) if found in any supported browser.
pub fn extract_from_chrome() -> Option<(String, String)> {
    let app_support = dirs::data_local_dir()?; // ~/Library/Application Support
    for browser in BROWSERS {
        let profile_dir = app_support.join(browser.profile_dir).join("Default");
        if !profile_dir.exists() {
            continue;
        }
        if let Some(pair) = try_browser(browser, &profile_dir) {
            return Some(pair);
        }
    }
    None
}

fn try_browser(browser: &Browser, profile_dir: &Path) -> Option<(String, String)> {
    let key = keychain_key(browser.keychain_service, browser.keychain_account)?;
    let aes_key = derive_aes_key(&key);

    let cookie_db = profile_dir.join("Cookies");
    let cookie = extract_cookie(&cookie_db, &aes_key)?;

    let workspace_id = extract_workspace_id_from_history(profile_dir)?;

    Some((workspace_id, cookie))
}

/// Read the raw secret from macOS Keychain via `security` CLI.
fn keychain_key(service: &str, account: &str) -> Option<Vec<u8>> {
    let out = Command::new("security")
        .args(["find-generic-password", "-w", "-s", service, "-a", account])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let raw = String::from_utf8(out.stdout).ok()?;
    Some(raw.trim().as_bytes().to_vec())
}

/// PBKDF2-SHA1 key derivation used by all Chromium browsers on macOS.
fn derive_aes_key(raw: &[u8]) -> [u8; 16] {
    let mut key = [0u8; 16];
    pbkdf2_hmac::<Sha1>(raw, b"saltysalt", 1003, &mut key);
    key
}

/// Query the Cookies SQLite database (copying to a temp file first to avoid
/// conflicts with a running browser) and return the decrypted `auth` cookie
/// for opencode.ai.
fn extract_cookie(db_path: &Path, aes_key: &[u8; 16]) -> Option<String> {
    // Copy to temp to avoid "database is locked" if browser is running.
    let tmp = std::env::temp_dir().join("agent-usage-chrome-cookies.db");
    std::fs::copy(db_path, &tmp).ok()?;

    let conn = rusqlite::Connection::open_with_flags(
        &tmp,
        rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
    )
    .ok()?;

    // Column name differs between Chromium versions: encrypted_value (older)
    // or encrypted_value (current). The schema is stable; we select by name.
    let mut stmt = conn
        .prepare(
            "SELECT encrypted_value FROM cookies \
             WHERE host_key IN ('opencode.ai', '.opencode.ai') \
               AND name = 'auth' \
             ORDER BY last_access_utc DESC LIMIT 1",
        )
        .ok()?;

    let encrypted: Vec<u8> = stmt
        .query_row([], |row| row.get(0))
        .ok()?;

    let _ = std::fs::remove_file(&tmp);

    decrypt_cookie(&encrypted, aes_key)
}

/// Decrypt a Chromium v10 AES-128-CBC cookie value.
fn decrypt_cookie(encrypted: &[u8], key: &[u8; 16]) -> Option<String> {
    // v10 prefix: first 3 bytes are b"v10"
    if encrypted.len() < 3 || &encrypted[..3] != b"v10" {
        return None;
    }
    let ciphertext = &encrypted[3..];
    let iv = [b' '; 16]; // Chromium uses 0x20 * 16 as IV

    let mut buf = ciphertext.to_vec();
    let pt = Aes128CbcDec::new(key.into(), &iv.into())
        .decrypt_padded_mut::<Pkcs7>(&mut buf)
        .ok()?;
    String::from_utf8(pt.to_vec()).ok()
}

/// Extract workspace ID from the browser's History database by searching for
/// visited URLs matching `opencode.ai/workspace/*/go`.
fn extract_workspace_id_from_history(profile_dir: &Path) -> Option<String> {
    let history_db = profile_dir.join("History");
    let tmp = std::env::temp_dir().join("agent-usage-chrome-history.db");
    std::fs::copy(&history_db, &tmp).ok()?;

    let conn = rusqlite::Connection::open_with_flags(
        &tmp,
        rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
    )
    .ok()?;

    let mut stmt = conn
        .prepare(
            "SELECT url FROM urls \
             WHERE url LIKE 'https://opencode.ai/workspace/%/go' \
             ORDER BY last_visit_time DESC LIMIT 1",
        )
        .ok()?;

    let url: String = stmt.query_row([], |row| row.get(0)).ok()?;
    let _ = std::fs::remove_file(&tmp);

    // Extract workspace ID from URL: https://opencode.ai/workspace/{ID}/go
    let stripped = url.strip_prefix("https://opencode.ai/workspace/")?;
    let id = stripped.strip_suffix("/go")?;
    if id.is_empty() {
        return None;
    }
    Some(id.to_string())
}
