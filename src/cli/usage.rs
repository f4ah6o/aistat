use crate::orchestrate::{run, ExitStatus, RunOptions};
use crate::providers::KNOWN_PROVIDER_IDS;
use crate::render::{json::render_json, text::render_text};
use std::io::{self};

pub struct UsageArgs {
    pub provider: Option<String>,
    pub refresh: bool,
    pub human: bool,
    pub debug: bool,
}

pub fn run_usage(args: UsageArgs) -> i32 {
    if let Some(ref p) = args.provider {
        if !KNOWN_PROVIDER_IDS.contains(&p.as_str()) {
            eprintln!(
                "usage {}: provider must be one of {}",
                p,
                KNOWN_PROVIDER_IDS.join(", ")
            );
            return ExitStatus::UsageError as i32;
        }
    }

    let requested: Vec<&str> = match &args.provider {
        Some(p) => vec![p.as_str()],
        None => KNOWN_PROVIDER_IDS.to_vec(),
    };

    let providers = crate::cli::registry::real_providers(args.refresh, args.debug);

    let (report, status) = run(
        &requested,
        &providers,
        RunOptions { debug: args.debug },
    );

    let stdout = io::stdout();
    let mut out = stdout.lock();

    let render_err = if args.human {
        render_text(&mut out, &report, &requested)
    } else {
        render_json(&mut out, &report)
    };

    if let Err(e) = render_err {
        eprintln!("{}", e);
        return ExitStatus::RenderError as i32;
    }

    status as i32
}
