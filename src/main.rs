mod accounts;
mod cli;
mod cred;
mod httpx;
mod orchestrate;
mod providers;
mod render;

use clap::{Parser, Subcommand};
use cli::usage::{run_usage, UsageArgs};

#[derive(Parser)]
#[command(
    name = "agent-usage",
    about = "agent-usage — read Claude / Codex usage limits",
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
    /// Report usage for all providers (default), or one: claude, codex
    Usage {
        /// Provider to query: claude, codex
        provider: Option<String>,

        /// Bypass the usage cache and force a fresh read
        #[arg(long = "refresh")]
        refresh: bool,
    },
}

fn main() {
    let cli = Cli::parse();
    let human = cli.human;

    let code = match cli.command {
        Some(Commands::Usage { provider, refresh }) => {
            run_usage(UsageArgs { provider, refresh, human, debug: cli.debug })
        }
        None => run_usage(UsageArgs { provider: None, refresh: false, human, debug: cli.debug }),
    };

    std::process::exit(code);
}
