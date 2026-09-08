//! Session-scoped settings validation with no browser dependency.

use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, fmt};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Device {
    pub name: String,
    pub width: u64,
    pub height: u64,
    pub scale: f64,
    pub mobile: bool,
    pub touch: bool,
    #[serde(default)]
    pub user_agent: String,
}
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Viewport {
    pub width: u64,
    pub height: u64,
    pub scale: f64,
}
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Geo {
    pub latitude: f64,
    pub longitude: f64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SettingsError(pub String);
impl fmt::Display for SettingsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}
impl std::error::Error for SettingsError {}

pub fn validate_viewport(width: u64, height: u64, scale: f64) -> Result<Viewport, SettingsError> {
    if width == 0 || height == 0 || !scale.is_finite() || scale <= 0.0 {
        return Err(SettingsError(format!(
            "viewport dimensions and scale must be positive: {width}x{height} @ {scale}"
        )));
    }
    Ok(Viewport {
        width,
        height,
        scale,
    })
}
pub fn validate_geo(latitude: f64, longitude: f64) -> Result<Geo, SettingsError> {
    if !latitude.is_finite()
        || !longitude.is_finite()
        || !(-90.0..=90.0).contains(&latitude)
        || !(-180.0..=180.0).contains(&longitude)
    {
        return Err(SettingsError(format!(
            "invalid coordinates: {latitude}, {longitude}"
        )));
    }
    Ok(Geo {
        latitude,
        longitude,
    })
}
pub fn validate_headers(headers: &BTreeMap<String, String>) -> Result<(), SettingsError> {
    for name in headers.keys() {
        if matches!(
            name.to_ascii_lowercase().as_str(),
            "authorization" | "proxy-authorization" | "cookie"
        ) {
            return Err(SettingsError(format!(
                "header {name:?} requires the credential risk class"
            )));
        }
    }
    Ok(())
}
pub fn validate_user_agent(value: &str) -> Result<String, SettingsError> {
    if value.trim().is_empty() {
        Err(SettingsError("user agent must not be empty".to_owned()))
    } else {
        Ok(value.to_owned())
    }
}
pub fn validate_device(device: &Device) -> Result<(), SettingsError> {
    if device.name.trim().is_empty()
        || device.width == 0
        || device.height == 0
        || !device.scale.is_finite()
        || device.scale <= 0.0
    {
        Err(SettingsError(format!("invalid device {:?}", device.name)))
    } else {
        Ok(())
    }
}

/// Accept only a secret-provider reference. The browser boundary resolves it
/// at execution time, so settings validation cannot leak or persist secrets.
pub fn validate_auth_entry(entry: &str) -> Result<String, SettingsError> {
    let entry = entry.trim();
    if entry.is_empty() {
        return Err(SettingsError("auth entry is required".to_owned()));
    }
    if entry.starts_with("op://") {
        Ok(entry.to_owned())
    } else {
        Err(SettingsError(
            "auth entry must be an op:// reference; plaintext credentials are denied".to_owned(),
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn auth_references_are_validated_without_resolving_credentials() {
        assert_eq!(
            validate_auth_entry(" op://vault/login/password ").unwrap(),
            "op://vault/login/password"
        );
        assert!(validate_auth_entry("plaintext-password").is_err());
        assert!(validate_auth_entry("").is_err());
    }

    #[test]
    fn rejects_credential_headers_and_invalid_coordinates() {
        let headers = [("Authorization".to_owned(), "redacted".to_owned())]
            .into_iter()
            .collect();
        assert!(validate_headers(&headers).is_err());
        assert!(validate_geo(91.0, 0.0).is_err());
        assert!(validate_viewport(100, 100, 1.0).is_ok());
    }
}
