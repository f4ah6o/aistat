use crate::providers::{Provider, ProviderOutput, ProviderError};
use crate::providers::claude::ClaudeClient;
use crate::providers::codex::CodexClient;

pub fn resolved_version() -> &'static str {
    env!("CARGO_PKG_VERSION")
}

/// Builds the live provider set. Claude + Codex with MemoryStore.
pub fn real_providers(cache_bypass: bool, debug: bool) -> Vec<Box<dyn Provider>> {
    let v = resolved_version();
    let make_debug = |prefix: &'static str| -> Option<Box<dyn Fn(&str) + Send + Sync>> {
        if debug {
            Some(Box::new(move |s: &str| eprint!("{}{}", prefix, s)))
        } else {
            None
        }
    };
    vec![
        Box::new(SingleAccountProvider::new(Box::new(ClaudeClient::new(
            crate::providers::claude::default_user_agent(v),
            make_debug(""),
            None,
            cache_bypass,
        )))),
        Box::new(SingleAccountProvider::new(Box::new(CodexClient::new(
            crate::providers::codex::default_user_agent(v),
            make_debug(""),
            None,
            cache_bypass,
        )))),
    ]
}

/// Collapses multi-account output to single active-account shape.
struct SingleAccountProvider {
    inner: Box<dyn Provider>,
}

impl SingleAccountProvider {
    fn new(inner: Box<dyn Provider>) -> Self {
        Self { inner }
    }
}

impl Provider for SingleAccountProvider {
    fn id(&self) -> &str {
        self.inner.id()
    }

    fn fetch(&self) -> Result<ProviderOutput, ProviderError> {
        let out = self.inner.fetch()?;
        if out.accounts.is_empty() {
            return Ok(out);
        }
        // Select active account (or first)
        let selected = out
            .accounts
            .iter()
            .find(|a| a.active)
            .or_else(|| out.accounts.first())
            .cloned();

        if let Some(acct) = selected {
            if acct.error.as_deref().map(|e| !e.is_empty()).unwrap_or(false) {
                let msg = acct.error.unwrap();
                return Err(ProviderError::Other(msg));
            }
            Ok(ProviderOutput {
                limits: acct.limits,
                accounts: vec![],
            })
        } else {
            Ok(ProviderOutput { limits: None, accounts: vec![] })
        }
    }
}
