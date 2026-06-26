use super::account::stored_access_token_owned;
use super::profile::Profile;
use crate::accounts::Account;
use crate::cred::Credential;
use crate::providers::ProviderError;
use chrono::{DateTime, Utc};
use serde_json::value::RawValue;

pub struct ReconcileInput<'a, F>
where
    F: Fn(&str) -> Result<Profile, ProviderError>,
{
    pub live_blob: Option<&'a Credential>,
    pub stored: &'a [Account],
    pub lookup_profile: &'a F,
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

pub fn reconcile<F>(input: ReconcileInput<'_, F>) -> ReconcileOutput
where
    F: Fn(&str) -> Result<Profile, ProviderError>,
{
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

    // Step 1: byte-match against stored accounts
    for acct in input.stored {
        if stored_access_token_owned(acct) == live.access_token {
            let mut updated = acct.clone();
            updated.last_seen_at = input.now;
            updated.raw_blob = RawValue::from_string(
                String::from_utf8_lossy(&live.raw).into_owned(),
            )
            .unwrap_or_else(|_| acct.raw_blob.clone());

            let mut accounts = input.stored.to_vec();
            // Update the matched account
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

    // Step 2: profile lookup
    match (input.lookup_profile)(&live.access_token) {
        Ok(profile) => {
            let mut accounts = input.stored.to_vec();
            let existing = accounts.iter_mut().find(|a| a.uuid == profile.account_uuid);

            let raw_blob = RawValue::from_string(
                String::from_utf8_lossy(&live.raw).into_owned(),
            )
            .unwrap_or_else(|_| serde_json::value::RawValue::from_string("{}".into()).unwrap());

            let (inserted, upserted) = if let Some(acct) = existing {
                acct.email = profile.email.clone();
                acct.display_name = profile.display_name.clone();
                acct.rate_limit_tier = profile.rate_limit_tier.clone();
                acct.last_seen_at = input.now;
                acct.raw_blob = raw_blob;
                (false, true)
            } else {
                let new_acct = Account {
                    uuid: profile.account_uuid.clone(),
                    email: profile.email.clone(),
                    display_name: profile.display_name.clone(),
                    rate_limit_tier: profile.rate_limit_tier.clone(),
                    last_seen_at: input.now,
                    raw_blob,
                };
                accounts.push(new_acct);
                (true, false)
            };

            ReconcileOutput {
                accounts,
                active_uuid: profile.account_uuid,
                capture_warn: None,
                inserted,
                upserted,
                live_unstored: None,
            }
        }
        Err(_) => {
            // Step 4: fallback — render unstored live row
            ReconcileOutput {
                accounts: input.stored.to_vec(),
                active_uuid: String::new(),
                capture_warn: Some(
                    "aistat: claude: could not identify live credential via profile endpoint; rendering as unstored account".into(),
                ),
                inserted: false,
                upserted: false,
                live_unstored: Some(live.clone()),
            }
        }
    }
}
