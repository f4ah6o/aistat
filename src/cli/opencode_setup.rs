use serde_json::json;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::PathBuf;

#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};

pub struct SetupArgs {
    pub workspace_id: Option<String>,
    pub auth_cookie: Option<String>,
}

pub fn run_setup(args: SetupArgs) -> i32 {
    let (workspace_id, auth_cookie) = match (args.workspace_id, args.auth_cookie) {
        (Some(workspace_id), Some(auth_cookie)) => (workspace_id, auth_cookie),
        (None, None) => match extract_from_browser() {
            Some(pair) => pair,
            None => {
                eprintln!(
                    "error: could not extract OpenCode Go workspace and auth cookie from Chrome; pass --workspace-id and --auth-cookie explicitly"
                );
                return 1;
            }
        },
        _ => {
            eprintln!("error: --workspace-id and --auth-cookie must be provided together");
            return 2;
        }
    };

    let cfg_dir = match dirs::config_dir() {
        Some(d) => d,
        None => {
            eprintln!("error: cannot resolve config directory");
            return 2;
        }
    };

    let dir = cfg_dir.join("opencode-bar");
    if let Err(e) = create_dir_private(&dir) {
        eprintln!("error: cannot create {}: {}", dir.display(), e);
        return 1;
    }

    let path: PathBuf = dir.join("opencode-go.json");
    let content = json!({
        "workspaceId": workspace_id,
        "authCookie": auth_cookie,
    });
    let s = serde_json::to_string_pretty(&content).unwrap();

    if let Err(e) = write_private(&path, s.as_bytes()) {
        eprintln!("error: cannot write {}: {}", path.display(), e);
        return 1;
    }

    eprintln!("saved to {}", path.display());
    0
}

/// Create a directory (and parents) with mode 0700 on Unix.
fn create_dir_private(dir: &std::path::Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)
    }
    #[cfg(not(unix))]
    {
        fs::create_dir_all(dir)
    }
}

/// Write bytes to a file with mode 0600 on Unix (owner-read/write only).
/// Handles both new files and pre-existing files with wrong permissions.
fn write_private(path: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    #[cfg(unix)]
    let mut f = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)?;
    #[cfg(not(unix))]
    let mut f = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .open(path)?;

    // Fix permissions on the open file so pre-existing files with wrong
    // modes (e.g. 0644 created by an earlier version) are tightened too.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        f.set_permissions(std::fs::Permissions::from_mode(0o600))?;
    }

    f.write_all(data)
}

#[cfg(target_os = "macos")]
fn extract_from_browser() -> Option<(String, String)> {
    crate::cred::chrome_cookie_darwin::extract_from_chrome()
}

#[cfg(not(target_os = "macos"))]
fn extract_from_browser() -> Option<(String, String)> {
    None
}
