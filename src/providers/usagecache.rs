use super::Limit;
use chrono::{DateTime, Utc};
use std::collections::BTreeMap;
use std::path::PathBuf;
use std::time::Duration;

const DEFAULT_TTL_SECS: u64 = 90;

#[derive(serde::Serialize, serde::Deserialize)]
struct CacheEntry {
    fetched_at: DateTime<Utc>,
    limits: BTreeMap<String, CachedLimit>,
}

#[derive(serde::Serialize, serde::Deserialize, Clone)]
struct CachedLimit {
    used_percent: f64,
    remaining_percent: f64,
    resets_at: DateTime<Utc>,
    reset_after_seconds: i64,
}

#[derive(serde::Serialize, serde::Deserialize, Default)]
struct CacheFile {
    entries: BTreeMap<String, CacheEntry>,
}

pub struct Cache {
    provider: String,
    ttl: Duration,
    disabled: bool,
    path: Option<PathBuf>,
    lock_path: Option<PathBuf>,
}

impl Cache {
    pub fn new(provider: &str) -> Self {
        let ttl_secs = std::env::var("AISTAT_USAGE_CACHE_TTL")
            .ok()
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or(DEFAULT_TTL_SECS);

        let (path, lock_path) = match cache_paths(provider) {
            Some(p) => p,
            None => return Self { provider: provider.into(), ttl: Duration::from_secs(ttl_secs), disabled: true, path: None, lock_path: None },
        };

        Self {
            provider: provider.into(),
            ttl: Duration::from_secs(ttl_secs),
            disabled: false,
            path: Some(path),
            lock_path: Some(lock_path),
        }
    }

    pub fn get_with_age(&self, uuid: &str) -> Option<(BTreeMap<String, Limit>, Duration)> {
        if self.disabled {
            return None;
        }
        let path = self.path.as_ref()?;
        let lock_path = self.lock_path.as_ref()?;

        // Shared lock
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .open(lock_path)
            .ok()?;
        use fs2::FileExt;
        lock.lock_shared().ok()?;

        let data = std::fs::read(path).ok()?;
        let cache: CacheFile = serde_json::from_slice(&data).ok()?;
        let entry = cache.entries.get(uuid)?;

        let age = (Utc::now() - entry.fetched_at).to_std().ok()?;
        if age > self.ttl {
            return None;
        }

        let limits = entry
            .limits
            .iter()
            .map(|(k, v)| {
                (
                    k.clone(),
                    Limit {
                        used_percent: v.used_percent,
                        remaining_percent: v.remaining_percent,
                        resets_at: v.resets_at,
                        reset_after_seconds: v.reset_after_seconds,
                    },
                )
            })
            .collect();
        Some((limits, age))
    }

    pub fn put(&self, uuid: &str, limits: &BTreeMap<String, Limit>) {
        if self.disabled {
            return;
        }
        let (path, lock_path) = match (self.path.as_ref(), self.lock_path.as_ref()) {
            (Some(p), Some(l)) => (p, l),
            _ => return,
        };

        // Exclusive lock
        let lock = match std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .open(lock_path)
        {
            Ok(f) => f,
            Err(_) => return,
        };
        use fs2::FileExt;
        if lock.lock_exclusive().is_err() {
            return;
        }

        let mut cache: CacheFile = std::fs::read(path)
            .ok()
            .and_then(|d| serde_json::from_slice(&d).ok())
            .unwrap_or_default();

        let cached_limits = limits
            .iter()
            .map(|(k, v)| {
                (
                    k.clone(),
                    CachedLimit {
                        used_percent: v.used_percent,
                        remaining_percent: v.remaining_percent,
                        resets_at: v.resets_at,
                        reset_after_seconds: v.reset_after_seconds,
                    },
                )
            })
            .collect();

        cache.entries.insert(
            uuid.to_string(),
            CacheEntry {
                fetched_at: Utc::now(),
                limits: cached_limits,
            },
        );

        if let Ok(data) = serde_json::to_vec(&cache) {
            if let Some(dir) = path.parent() {
                let tmp = dir.join(format!(".{}-cache-tmp-{}", self.provider, std::process::id()));
                if std::fs::write(&tmp, &data).is_ok() {
                    let _ = std::fs::rename(&tmp, path);
                }
            }
        }
    }
}

fn cache_paths(provider: &str) -> Option<(PathBuf, PathBuf)> {
    let base = dirs::cache_dir()?;
    let dir = base.join("aistat").join("usage");
    std::fs::create_dir_all(&dir).ok()?;
    let path = dir.join(format!("{}-v1.json", provider));
    let lock_path = dir.join(format!("{}.cache.lock", provider));
    Some((path, lock_path))
}
