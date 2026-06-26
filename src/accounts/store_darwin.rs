#![cfg(target_os = "macos")]

use super::{Account, AccountProvider, Store};
use std::collections::HashMap;
use std::process::Command;

pub struct KeychainStore {
    provider: AccountProvider,
}

impl KeychainStore {
    pub fn new(provider: AccountProvider) -> Self {
        Self { provider }
    }

    fn service_for(&self, uuid: &str) -> String {
        format!("aistat:accounts:{}:{}", self.provider.as_str(), uuid)
    }

    fn index_service(&self) -> String {
        format!("aistat:accounts:{}:index", self.provider.as_str())
    }

    fn security(&self, args: &[&str]) -> Result<Vec<u8>, String> {
        let out = Command::new("security")
            .args(args)
            .output()
            .map_err(|e| e.to_string())?;
        if !out.status.success() {
            if out.status.code() == Some(44) {
                return Err("not found".into());
            }
            return Err(String::from_utf8_lossy(&out.stderr).into_owned());
        }
        Ok(out.stdout)
    }

    fn read_index(&self) -> Vec<String> {
        let out = match self.security(&[
            "find-generic-password",
            "-s",
            &self.index_service(),
            "-w",
        ]) {
            Ok(o) => o,
            Err(_) => return vec![],
        };
        let s = String::from_utf8_lossy(&out);
        let s = s.trim();
        #[derive(serde::Deserialize)]
        struct Index {
            uuids: Vec<String>,
        }
        serde_json::from_str::<Index>(s)
            .map(|i| i.uuids)
            .unwrap_or_default()
    }

    fn write_index(&self, uuids: &[String]) -> Result<(), String> {
        if uuids.is_empty() {
            let _ = self.security(&[
                "delete-generic-password",
                "-s",
                &self.index_service(),
            ]);
            return Ok(());
        }
        let data = serde_json::json!({ "uuids": uuids }).to_string();
        let user = std::env::var("USER").unwrap_or_else(|_| "aistat".into());
        self.security(&[
            "add-generic-password",
            "-U",
            "-s",
            &self.index_service(),
            "-a",
            &user,
            "-w",
            &data,
        ])?;
        Ok(())
    }

    fn read_account(&self, uuid: &str) -> Option<Account> {
        let svc = self.service_for(uuid);
        let out = self.security(&["find-generic-password", "-s", &svc, "-w"]).ok()?;
        let s = String::from_utf8_lossy(&out);
        serde_json::from_str(s.trim()).ok()
    }

    fn write_account(&self, account: &Account) -> Result<(), String> {
        let svc = self.service_for(&account.uuid);
        // Delete first to handle email drift (D9)
        let _ = self.security(&["delete-generic-password", "-s", &svc]);
        let data = serde_json::to_string(account).map_err(|e| e.to_string())?;
        let user = std::env::var("USER").unwrap_or_else(|_| "aistat".into());
        self.security(&[
            "add-generic-password",
            "-U",
            "-s",
            &svc,
            "-a",
            &user,
            "-w",
            &data,
        ])?;
        Ok(())
    }
}

impl Store for KeychainStore {
    fn list(&self) -> Result<Vec<Account>, String> {
        let uuids = self.read_index();
        let accounts = uuids
            .iter()
            .filter_map(|uuid| self.read_account(uuid))
            .collect();
        Ok(accounts)
    }

    fn upsert(&self, account: Account) -> Result<(), String> {
        self.write_account(&account)?;
        let mut uuids = self.read_index();
        if !uuids.contains(&account.uuid) {
            uuids.push(account.uuid.clone());
        }
        self.write_index(&uuids)
    }

    fn delete(&self, uuid: &str) -> Result<(), String> {
        let mut uuids = self.read_index();
        uuids.retain(|u| u != uuid);
        self.write_index(&uuids)?;
        let svc = self.service_for(uuid);
        let _ = self.security(&["delete-generic-password", "-s", &svc]);
        Ok(())
    }
}
