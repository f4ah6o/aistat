use serde_json::json;
use std::fs;
use std::path::PathBuf;

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
    if let Err(e) = fs::create_dir_all(&dir) {
        eprintln!("error: cannot create {}: {}", dir.display(), e);
        return 1;
    }

    let path: PathBuf = dir.join("opencode-go.json");
    let content = json!({
        "workspaceId": args.workspace_id,
        "authCookie": args.auth_cookie,
    });
    let s = serde_json::to_string_pretty(&content).unwrap();

    if let Err(e) = fs::write(&path, &s) {
        eprintln!("error: cannot write {}: {}", path.display(), e);
        return 1;
    }

    eprintln!("saved to {}", path.display());
    0
}
