use super::account::stored_access_token_owned;
use crate::accounts::Account;
use crate::cred::{self, Credential};
use chrono::{DateTime, Utc};
use serde_json::value::RawValue;

pub struct ReconcileInput<'a> {
    pub live_blob: Option<&'a Credential>,
    pub stored: &'a [Account],
    pub now: DateTime<Utc>,
}

pub struct ReconcileOutput {
    pub accounts: Vec<Account>,
    pub active_uuid: String,
    pub capture_warn: Option<String>,
    pub inserted: bool,
    pub upserted: bool,
    pub live_unstored: Option<Credential>,
}

pub fn reconcile(input: ReconcileInput<'_>) -> ReconcileOutput {
    let live = match input.live_blob {
        Some(l) => l,
        None => {
            return ReconcileOutput {
                accounts: input.stored.to_vec(),
                active_uuid: String::new(),
                capture_warn: None,
                inserted: false,
                upserted: false,
                live_unstored: None,
            };
        }
    };

    // Step 1: byte-match
    for acct in input.stored {
        if stored_access_token_owned(acct) == live.access_token {
            let mut updated = acct.clone();
            updated.last_seen_at = input.now;
            if let Ok(blob) = RawValue::from_string(String::from_utf8_lossy(&live.raw).into_owned()) {
                updated.raw_blob = blob;
            }
            let mut accounts = input.stored.to_vec();
            for a in &mut accounts {
                if a.uuid == acct.uuid {
                    *a = updated.clone();
                }
            }
            return ReconcileOutput {
                accounts,
                active_uuid: acct.uuid.clone(),
                capture_warn: None,
                inserted: false,
                upserted: true,
                live_unstored: None,
            };
        }
    }

    // Step 2: decode id_token from live blob
    let id_token = cred::codex::extract_id_token(&live.raw);
    if let Some(ref token) = id_token {
        if let Ok((sub, email, _exp)) = cred::parse_codex_id_token(token) {
            let mut accounts = input.stored.to_vec();
            let existing = accounts.iter_mut().find(|a| a.uuid == sub);

            let raw_blob = RawValue::from_string(
                String::from_utf8_lossy(&live.raw).into_owned(),
            )
            .unwrap_or_else(|_| serde_json::value::RawValue::from_string("{}".into()).unwrap());

            let (inserted, upserted) = if let Some(acct) = existing {
                acct.email = email.clone();
                acct.last_seen_at = input.now;
                acct.raw_blob = raw_blob;
                (false, true)
            } else {
                accounts.push(Account {
                    uuid: sub.clone(),
                    email: email.clone(),
                    display_name: String::new(),
                    rate_limit_tier: String::new(),
                    last_seen_at: input.now,
                    raw_blob,
                });
                (true, false)
            };

            return ReconcileOutput {
                accounts,
                active_uuid: sub,
                capture_warn: None,
                inserted,
                upserted,
                live_unstored: None,
            };
        }
    }

    // Fallback
    ReconcileOutput {
        accounts: input.stored.to_vec(),
        active_uuid: String::new(),
        capture_warn: Some(
            "aistat: codex: could not identify live credential; rendering as unstored account".into(),
        ),
        inserted: false,
        upserted: false,
        live_unstored: Some(live.clone()),
    }
}
