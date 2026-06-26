use super::{Account, Store};
use std::collections::HashMap;
use std::sync::Mutex;

pub struct MemoryStore {
    accounts: Mutex<HashMap<String, Account>>,
}

impl MemoryStore {
    pub fn new() -> Self {
        Self {
            accounts: Mutex::new(HashMap::new()),
        }
    }
}

impl Store for MemoryStore {
    fn list(&self) -> Result<Vec<Account>, String> {
        let guard = self.accounts.lock().unwrap();
        Ok(guard.values().cloned().collect())
    }

    fn upsert(&self, account: Account) -> Result<(), String> {
        let mut guard = self.accounts.lock().unwrap();
        guard.insert(account.uuid.clone(), account);
        Ok(())
    }

    fn delete(&self, uuid: &str) -> Result<(), String> {
        let mut guard = self.accounts.lock().unwrap();
        guard.remove(uuid);
        Ok(())
    }
}
