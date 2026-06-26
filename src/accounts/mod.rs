pub mod memory;
pub mod provider;
#[cfg(target_os = "macos")]
pub mod store_darwin;
#[cfg(target_os = "linux")]
pub mod store_linux;

pub use memory::MemoryStore;
pub use provider::AccountProvider;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Account {
    pub uuid: String,
    pub email: String,
    pub display_name: String,
    pub rate_limit_tier: String,
    pub last_seen_at: DateTime<Utc>,
    pub raw_blob: Box<RawValue>,
}

impl Account {
    pub fn new(
        uuid: String,
        email: String,
        display_name: String,
        rate_limit_tier: String,
        last_seen_at: DateTime<Utc>,
        raw_blob: Box<RawValue>,
    ) -> Result<Self, String> {
        if uuid.is_empty() {
            return Err("account UUID must not be empty".into());
        }
        // Validate raw_blob is valid JSON
        let _: serde_json::Value =
            serde_json::from_str(raw_blob.get()).map_err(|e| format!("invalid raw_blob: {e}"))?;
        Ok(Self {
            uuid,
            email,
            display_name,
            rate_limit_tier,
            last_seen_at,
            raw_blob,
        })
    }
}

pub trait Store: Send + Sync {
    fn list(&self) -> Result<Vec<Account>, String>;
    fn upsert(&self, account: Account) -> Result<(), String>;
    fn delete(&self, uuid: &str) -> Result<(), String>;
}

pub fn open_store(provider: AccountProvider) -> Box<dyn Store> {
    #[cfg(target_os = "macos")]
    {
        Box::new(store_darwin::KeychainStore::new(provider))
    }
    #[cfg(target_os = "linux")]
    {
        Box::new(store_linux::FileStore::new(provider))
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux")))]
    {
        let _ = provider;
        Box::new(MemoryStore::new())
    }
}
