#![cfg(target_os = "linux")]

use super::{claude::parse_claude_cred, Credential, CredError};
use std::path::PathBuf;

fn cred_path() -> Result<PathBuf, CredError> {
    let home = dirs::home_dir().ok_or_else(|| {
        CredError::Other("cannot resolve home directory".into())
    })?;
    Ok(home.join(".claude").join(".credentials.json"))
}

pub fn read_claude_credential() -> Result<Credential, CredError> {
    let path = cred_path()?;
    let data = std::fs::read(&path).map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            CredError::ClaudeNotFound
        } else {
            CredError::Other(format!("reading Claude credentials: {e}"))
        }
    })?;
    parse_claude_cred(&data)
}

pub fn write_claude_live_blob(raw_blob: &[u8]) -> Result<(), CredError> {
    let path = cred_path()?;
    let dir = path.parent().unwrap();
    std::fs::create_dir_all(dir)
        .map_err(|e| CredError::Other(format!("creating Claude credentials directory: {e}")))?;

    let tmp_path = dir.join(format!(".credentials-{}.json", std::process::id()));
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::write(&tmp_path, raw_blob)
            .map_err(|e| CredError::Other(format!("writing Claude credentials file: {e}")))?;
        std::fs::set_permissions(&tmp_path, std::fs::Permissions::from_mode(0o600))
            .map_err(|e| CredError::Other(format!("setting Claude credentials file mode: {e}")))?;
    }
    std::fs::rename(&tmp_path, &path)
        .map_err(|e| CredError::Other(format!("installing Claude credentials file: {e}")))?;
    Ok(())
}
