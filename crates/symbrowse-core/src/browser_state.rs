//! Engine-neutral cookie and web-storage contracts.
//!
//! Adapters own browser protocol calls; this module owns origin validation,
//! deterministic ordering, and mutation semantics used by every adapter.

use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, fmt};
use url::Url;

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Cookie {
    pub name: String,
    pub value: String,
    #[serde(default)]
    pub domain: String,
    #[serde(default = "default_path")]
    pub path: String,
    #[serde(default)]
    pub secure: bool,
    #[serde(default)]
    pub http_only: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum StorageKind {
    Local,
    Session,
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct OriginState {
    pub cookies: Vec<Cookie>,
    pub local_storage: BTreeMap<String, String>,
    pub session_storage: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StateError(String);
impl fmt::Display for StateError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}
impl std::error::Error for StateError {}

fn default_path() -> String {
    "/".to_owned()
}

pub fn origin(value: &str) -> Result<String, StateError> {
    let url = Url::parse(value).map_err(|_| StateError(format!("invalid origin {value:?}")))?;
    if !matches!(url.scheme(), "http" | "https")
        || url.host_str().is_none()
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(StateError(format!("invalid origin {value:?}")));
    }
    Ok(url.origin().ascii_serialization())
}

pub fn validate_cookie(cookie: &Cookie) -> Result<(), StateError> {
    if cookie.name.trim().is_empty() {
        return Err(StateError("cookie name is required".into()));
    }
    if cookie.path.is_empty() || !cookie.path.starts_with('/') {
        return Err(StateError("cookie path must start with /".into()));
    }
    Ok(())
}

pub fn list_cookies(state: &OriginState) -> Result<Vec<Cookie>, StateError> {
    for cookie in &state.cookies {
        validate_cookie(cookie)?;
    }
    let mut result = state.cookies.clone();
    result.sort_by(|a, b| (&a.name, &a.domain, &a.path).cmp(&(&b.name, &b.domain, &b.path)));
    Ok(result)
}

pub fn set_cookie(state: &mut OriginState, cookie: Cookie) -> Result<(), StateError> {
    validate_cookie(&cookie)?;
    state.cookies.retain(|old| {
        old.name != cookie.name || old.domain != cookie.domain || old.path != cookie.path
    });
    state.cookies.push(cookie);
    Ok(())
}

pub fn clear_cookie(state: &mut OriginState, name: &str) -> Result<(), StateError> {
    if name.trim().is_empty() {
        return Err(StateError("cookie name is required".into()));
    }
    state.cookies.retain(|cookie| cookie.name != name);
    Ok(())
}

pub fn storage(state: &OriginState, kind: StorageKind) -> &BTreeMap<String, String> {
    match kind {
        StorageKind::Local => &state.local_storage,
        StorageKind::Session => &state.session_storage,
    }
}

pub fn storage_mut(state: &mut OriginState, kind: StorageKind) -> &mut BTreeMap<String, String> {
    match kind {
        StorageKind::Local => &mut state.local_storage,
        StorageKind::Session => &mut state.session_storage,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn cookie(name: &str) -> Cookie {
        Cookie {
            name: name.into(),
            value: "v".into(),
            domain: "example.test".into(),
            path: "/".into(),
            secure: false,
            http_only: false,
        }
    }
    #[test]
    fn cookie_mutations_are_origin_scoped_and_sorted() {
        let mut state = OriginState::default();
        set_cookie(&mut state, cookie("z")).unwrap();
        set_cookie(&mut state, cookie("a")).unwrap();
        assert_eq!(
            list_cookies(&state)
                .unwrap()
                .iter()
                .map(|c| c.name.as_str())
                .collect::<Vec<_>>(),
            ["a", "z"]
        );
        clear_cookie(&mut state, "a").unwrap();
        assert_eq!(list_cookies(&state).unwrap().len(), 1);
    }
    #[test]
    fn origins_reject_paths_and_non_http_schemes() {
        assert_eq!(
            origin("https://example.test/").unwrap(),
            "https://example.test"
        );
        assert!(origin("https://example.test/path").is_err());
        assert!(origin("file:///tmp/x").is_err());
    }
}
