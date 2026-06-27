mod accounts;
mod cli;
mod cred;
mod httpx;
mod orchestrate;
mod providers;
mod render;

use clap::{Parser, Subcommand};
use cli::opencode_setup::{run_setup, SetupArgs};
use cli::usage::{run_usage, UsageArgs};

#[derive(Parser)]
#[command(
    name = "agent-usage",
    about = "agent-usage — read Claude / Codex / OpenCode Go usage limits",
    version = env!("CARGO_PKG_VERSION"),
)]
struct Cli {
    /// Render human-readable text instead of JSON
    #[arg(long = "human", global = true)]
    human: bool,

    /// Write per-request and per-provider lines to stderr
    #[arg(long = "debug", global = true)]
    debug: bool,

    #[command(subcommand)]
    command: Option<Commands>,
}

#[derive(Subcommand)]
enum Commands {
    /// Report usage for all providers (default), or one: claude, codex, opencodego
    Usage {
        /// Provider to query: claude, codex, opencodego
        provider: Option<String>,

        /// Bypass the usage cache and force a fresh read
        #[arg(long = "refresh")]
        refresh: bool,
    },

    /// Configure OpenCode Go credentials
    #[command(name = "opencodego")]
    OpenCodeGo {
        #[command(subcommand)]
        action: OpenCodeGoAction,
    },
}

#[derive(Subcommand)]
enum OpenCodeGoAction {
    /// Save workspace ID and auth cookie to ~/.config/opencode-bar/opencode-go.json
    ///
    /// How to find your values:
    ///   Workspace ID : open https://opencode.ai in a browser and navigate to your
    ///                  Go dashboard — the ID appears in the URL as
    ///                  opencode.ai/workspace/<WORKSPACE_ID>/go
    ///   Auth cookie  : open DevTools (F12) → Application → Cookies →
    ///                  https://opencode.ai → copy the value of the "auth" cookie
    Setup {
        /// Workspace ID (from the dashboard URL)
        #[arg(long)]
        workspace_id: String,

        /// Auth cookie value (from browser DevTools)
        #[arg(long)]
        auth_cookie: String,
    },
}

fn main() {
    let cli = Cli::parse();
    let human = cli.human;

    let code = match cli.command {
        Some(Commands::Usage { provider, refresh }) => {
            run_usage(UsageArgs { provider, refresh, human, debug: cli.debug })
        }
        Some(Commands::OpenCodeGo {
            action: OpenCodeGoAction::Setup { workspace_id, auth_cookie },
        }) => run_setup(SetupArgs { workspace_id, auth_cookie }),
        None => run_usage(UsageArgs { provider: None, refresh: false, human, debug: cli.debug }),
    };

    std::process::exit(code);
}
