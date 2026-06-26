#![cfg(target_os = "linux")]

use super::{Account, AccountProvider, Store};
use fs2::FileExt;
use std::collections::HashMap;
use std::path::PathBuf;

pub struct FileStore {
    provider: AccountProvider,
}

impl FileStore {
    pub fn new(provider: AccountProvider) -> Self {
        Self { provider }
    }

    fn data_path(&self) -> Result<PathBuf, String> {
        let base = dirs::config_dir()
            .ok_or_else(|| "cannot find config directory".to_string())?;
        Ok(base
            .join("aistat")
            .join("accounts")
            .join(format!("{}.json", self.provider.as_str())))
    }

    fn lock_path(&self) -> Result<PathBuf, String> {
        let base = dirs::config_dir()
            .ok_or_else(|| "cannot find config directory".to_string())?;
        Ok(base
            .join("aistat")
            .join("accounts")
            .join(format!(".{}.lock", self.provider.as_str())))
    }

    fn ensure_dir(&self) -> Result<(), String> {
        let path = self.data_path()?;
        let dir = path.parent().unwrap();
        std::fs::create_dir_all(dir)
            .map_err(|e| format!("creating accounts dir: {e}"))
    }

    fn read_map(path: &PathBuf) -> HashMap<String, Account> {
        let data = match std::fs::read(path) {
            Ok(d) => d,
            Err(_) => return HashMap::new(),
        };
        serde_json::from_slice(&data).unwrap_or_default()
    }

    fn write_map(path: &PathBuf, map: &HashMap<String, Account>) -> Result<(), String> {
        use std::os::unix::fs::PermissionsExt;
        let dir = path.parent().unwrap();
        let tmp = dir.join(format!(".tmp-{}.json", std::process::id()));
        let data = serde_json::to_vec(map).map_err(|e| e.to_string())?;
        std::fs::write(&tmp, &data).map_err(|e| format!("writing accounts file: {e}"))?;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600))
            .map_err(|e| format!("setting accounts file mode: {e}"))?;
        std::fs::rename(&tmp, path).map_err(|e| format!("installing accounts file: {e}"))
    }
}

impl Store for FileStore {
    fn list(&self) -> Result<Vec<Account>, String> {
        self.ensure_dir()?;
        let lock_path = self.lock_path()?;
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .open(&lock_path)
            .map_err(|e| format!("opening lock file: {e}"))?;
        lock.lock_shared().map_err(|e| format!("acquiring shared lock: {e}"))?;
        let data_path = self.data_path()?;
        let map = Self::read_map(&data_path);
        Ok(map.into_values().collect())
    }

    fn upsert(&self, account: Account) -> Result<(), String> {
        self.ensure_dir()?;
        let lock_path = self.lock_path()?;
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .open(&lock_path)
            .map_err(|e| format!("opening lock file: {e}"))?;
        lock.lock_exclusive().map_err(|e| format!("acquiring exclusive lock: {e}"))?;
        let data_path = self.data_path()?;
        let mut map = Self::read_map(&data_path);
        map.insert(account.uuid.clone(), account);
        Self::write_map(&data_path, &map)
    }

    fn delete(&self, uuid: &str) -> Result<(), String> {
        self.ensure_dir()?;
        let lock_path = self.lock_path()?;
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .open(&lock_path)
            .map_err(|e| format!("opening lock file: {e}"))?;
        lock.lock_exclusive().map_err(|e| format!("acquiring exclusive lock: {e}"))?;
        let data_path = self.data_path()?;
        let mut map = Self::read_map(&data_path);
        map.remove(uuid);
        if map.is_empty() {
            let _ = std::fs::remove_file(&data_path);
            return Ok(());
        }
        Self::write_map(&data_path, &map)
    }
}
