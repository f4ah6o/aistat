use serde_json::json;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::PathBuf;

#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};

pub struct SetupArgs {
    pub workspace_id: String,
    pub auth_cookie: String,
}

pub fn run_setup(args: SetupArgs) -> i32 {
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
        "workspaceId": args.workspace_id,
        "authCookie": args.auth_cookie,
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
        fs::DirBuilder::new().recursive(true).mode(0o700).create(dir)
    }
    #[cfg(not(unix))]
    {
        fs::create_dir_all(dir)
    }
}

/// Write bytes to a file with mode 0600 on Unix (owner-read/write only).
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
    f.write_all(data)
}
