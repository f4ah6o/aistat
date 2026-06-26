use super::CredError;
use std::process::Command;

pub fn read_github_token() -> Result<String, CredError> {
    let out = Command::new("gh")
        .args(["auth", "token"])
        .output()
        .map_err(|_| CredError::GitHubNotFound)?;
    if !out.status.success() {
        return Err(CredError::GitHubNotFound);
    }
    let token = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if token.is_empty() {
        return Err(CredError::GitHubNotFound);
    }
    Ok(token)
}
