use super::ProviderError;

pub fn default_classify(status: u16, url: &str, body: &str) -> ProviderError {
    let snipped = snip(body);
    match status {
        401 | 403 => ProviderError::AuthDenied(format!("HTTP {status} from {url}: {snipped}")),
        408 | 429 | 500..=599 => {
            ProviderError::Transient(format!("HTTP {status} from {url}: {snipped}"))
        }
        _ => ProviderError::Other(format!("HTTP {status} from {url}: {snipped}")),
    }
}

pub fn snip(s: &str) -> &str {
    if s.len() > 200 {
        &s[..200]
    } else {
        s
    }
}
