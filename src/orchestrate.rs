use crate::providers::{Provider, ProviderError, ProviderResult, Report};
use chrono::Utc;
use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ExitStatus {
    Ok = 0,
    AnyFailed = 1,
    UsageError = 2,
    RenderError = 3,
}

pub struct RunOptions {
    pub debug: bool,
}

pub fn run(
    requested: &[&str],
    all: &[Box<dyn Provider>],
    _opts: RunOptions,
) -> (Report, ExitStatus) {
    let by_id: std::collections::HashMap<&str, &dyn Provider> =
        all.iter().map(|p| (p.id(), p.as_ref())).collect();

    let checked_at = Utc::now();

    let results: Arc<Mutex<BTreeMap<String, ProviderResult>>> =
        Arc::new(Mutex::new(BTreeMap::new()));
    let any_failed: Arc<Mutex<bool>> = Arc::new(Mutex::new(false));

    // Deduplicate requested
    let mut seen = std::collections::HashSet::new();
    let unique: Vec<&str> = requested
        .iter()
        .filter(|&&id| seen.insert(id))
        .copied()
        .collect();

    std::thread::scope(|s| {
        for &id in &unique {
            if let Some(&provider) = by_id.get(id) {
                let results = Arc::clone(&results);
                let any_failed = Arc::clone(&any_failed);
                let id = id.to_string();

                s.spawn(move || {
                    let result = provider.fetch();

                    let pr = match result {
                        Ok(out) => ProviderResult {
                            limits: out.limits,
                            accounts: out.accounts,
                            error: None,
                        },
                        Err(e) => {
                            // AuthMissing means the provider is not configured; treat
                            // it as an intentional skip in bulk runs so that users who
                            // haven't set up OpenCode Go don't get exit code 1 from
                            // the default `agent-usage usage` command.
                            if !matches!(e, ProviderError::AuthMissing(_)) {
                                *any_failed.lock().unwrap() = true;
                            }
                            ProviderResult {
                                limits: None,
                                accounts: vec![],
                                error: Some(e.to_string()),
                            }
                        }
                    };
                    results.lock().unwrap().insert(id, pr);
                });
            }
        }
    });

    let providers = Arc::try_unwrap(results).unwrap().into_inner().unwrap();
    let failed = *any_failed.lock().unwrap();

    let status = if failed { ExitStatus::AnyFailed } else { ExitStatus::Ok };

    (Report { checked_at, providers }, status)
}
