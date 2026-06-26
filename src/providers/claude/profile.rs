use crate::httpx::Doer;
use crate::providers::ProviderError;
use serde::Deserialize;
use std::sync::Arc;

const PROFILE_ENDPOINT: &str = "https://api.anthropic.com/api/oauth/profile";
const PROFILE_TIMEOUT_SECS: u64 = 3;

#[derive(Debug, Clone)]
pub struct Profile {
    pub account_uuid: String,
    pub email: String,
    pub display_name: String,
    pub rate_limit_tier: String,
}

#[derive(Deserialize)]
struct ProfileResp {
    account: Option<ProfileAccount>,
    organization: Option<ProfileOrg>,
}

#[derive(Deserialize)]
struct ProfileAccount {
    uuid: Option<String>,
    email: Option<String>,
    display_name: Option<String>,
}

#[derive(Deserialize)]
struct ProfileOrg {
    rate_limit_tier: Option<String>,
}

pub fn get_profile(doer: &Arc<Doer>, access_token: &str) -> Result<Profile, ProviderError> {
    let resp: ProfileResp = doer.get(PROFILE_ENDPOINT, access_token, PROFILE_TIMEOUT_SECS)?;
    let acct = resp.account.as_ref();
    let uuid = acct
        .and_then(|a| a.uuid.as_deref())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| {
            ProviderError::Other(
                "profile response missing required fields (account.uuid/account.email)".into(),
            )
        })?;
    let email = acct
        .and_then(|a| a.email.as_deref())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| {
            ProviderError::Other(
                "profile response missing required fields (account.uuid/account.email)".into(),
            )
        })?;
    Ok(Profile {
        account_uuid: uuid.to_string(),
        email: email.to_string(),
        display_name: acct
            .and_then(|a| a.display_name.clone())
            .unwrap_or_default(),
        rate_limit_tier: resp
            .organization
            .and_then(|o| o.rate_limit_tier)
            .unwrap_or_default(),
    })
}
