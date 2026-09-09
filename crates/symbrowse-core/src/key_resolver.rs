#![deny(unsafe_code)]

//! Fail-closed state-key resolution with injectable runtime providers.

use std::sync::{Condvar, Mutex};

use zeroize::Zeroizing;

use crate::{state::StateError, state_store::KeyMaterial};

pub const ENV_KEY_NAME: &str = "SYMBROWSE_ENCRYPTION_KEY";
pub const VAULT_ENTRY_NAME: &str = "symbrowse/encryption-key";
pub const KEYCHAIN_SERVICE: &str = "symbrowse";
pub const KEYCHAIN_ACCOUNT: &str = "encryption-key";

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum MissingReason {
    Unavailable,
    NotFound,
    NotInitialized,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum ProbeError {
    Missing(MissingReason),
    Failed(String),
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ProvisionOutcome {
    Created,
    Existing,
}

pub trait KeySources: Send + Sync {
    fn vault(&self, entry: &str) -> Result<Option<Vec<u8>>, ProbeError>;
    fn keychain(&self, service: &str, account: &str) -> Result<Option<Vec<u8>>, ProbeError>;
    fn environment(&self, name: &str) -> Option<String>;
}

pub trait KeyProvisioner: KeySources {
    fn provision_vault(&self, entry: &str, key: &[u8; 32]) -> Result<ProvisionOutcome, ProbeError>;
    fn provision_keychain(
        &self,
        service: &str,
        account: &str,
        key: &[u8; 32],
    ) -> Result<ProvisionOutcome, ProbeError>;
}

#[derive(serde::Serialize)]
pub struct KeyInitResult {
    pub action: String,
    pub configured: bool,
    pub key_source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub instruction: String,
}

impl Drop for KeyInitResult {
    fn drop(&mut self) {
        use zeroize::Zeroize;
        self.instruction.zeroize();
    }
}

#[derive(Debug, Eq, PartialEq)]
pub struct ResolveError(pub String);

impl core::fmt::Display for ResolveError {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for ResolveError {}

struct CacheState {
    generation: u64,
    resolving: bool,
    value: Option<Option<KeyMaterial>>,
}

pub struct KeyResolver<S> {
    sources: S,
    state: Mutex<CacheState>,
    ready: Condvar,
    initializing: Mutex<()>,
}

impl<S: KeySources> KeyResolver<S> {
    #[must_use]
    pub fn new(sources: S) -> Self {
        Self {
            sources,
            state: Mutex::new(CacheState {
                generation: 0,
                resolving: false,
                value: None,
            }),
            ready: Condvar::new(),
            initializing: Mutex::new(()),
        }
    }

    pub fn resolve(&self) -> Result<Option<KeyMaterial>, ResolveError> {
        loop {
            let mut state = self
                .state
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner);
            if let Some(value) = &state.value {
                return Ok(value.clone());
            }
            if state.resolving {
                state = self
                    .ready
                    .wait(state)
                    .unwrap_or_else(std::sync::PoisonError::into_inner);
                drop(state);
                continue;
            }
            state.resolving = true;
            let generation = state.generation;
            drop(state);

            let resolved = self.resolve_uncached();

            let mut state = self
                .state
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner);
            if let Ok(value) = &resolved
                && state.generation == generation
            {
                state.value = Some(value.clone());
            }
            state.resolving = false;
            self.ready.notify_all();
            return resolved;
        }
    }

    pub fn invalidate(&self) {
        let mut state = self
            .state
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        state.value = None;
        state.generation = state.generation.wrapping_add(1);
        self.ready.notify_all();
    }

    fn resolve_uncached(&self) -> Result<Option<KeyMaterial>, ResolveError> {
        match self.sources.vault(VAULT_ENTRY_NAME) {
            Ok(Some(raw)) if !raw.iter().all(u8::is_ascii_whitespace) => {
                let text = String::from_utf8_lossy(&raw);
                let token = first_hex_token(text.trim());
                let key = parse_hex_key(token).map_err(|error| {
                    ResolveError(format!("symvault entry {VAULT_ENTRY_NAME:?}: {error}"))
                })?;
                return KeyMaterial::new(key, "symvault")
                    .map(Some)
                    .map_err(state_error);
            }
            Ok(_) | Err(ProbeError::Missing(_)) => {}
            Err(ProbeError::Failed(error)) => {
                return Err(ResolveError(format!(
                    "symvault entry {VAULT_ENTRY_NAME:?}: {error}"
                )));
            }
        }

        match self.sources.keychain(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT) {
            Ok(Some(raw)) => {
                let key = parse_keychain_key(&raw).map_err(|error| {
                    ResolveError(format!("{KEYCHAIN_SERVICE}/{KEYCHAIN_ACCOUNT}: {error}"))
                })?;
                return KeyMaterial::new(key, "keychain")
                    .map(Some)
                    .map_err(state_error);
            }
            Ok(None) | Err(ProbeError::Missing(_)) => {}
            Err(ProbeError::Failed(error)) => return Err(ResolveError(error)),
        }

        if let Some(raw) = self.sources.environment(ENV_KEY_NAME)
            && !raw.is_empty()
        {
            let key = parse_hex_key(&raw)
                .map_err(|error| ResolveError(format!("{ENV_KEY_NAME}: {error}")))?;
            return KeyMaterial::new(key, "environment")
                .map(Some)
                .map_err(state_error);
        }
        Ok(None)
    }
}

impl<S: KeyProvisioner> KeyResolver<S> {
    pub fn initialize(&self) -> Result<KeyInitResult, ResolveError> {
        let _initializing = self
            .initializing
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some(existing) = self.resolve()? {
            return Ok(already_configured(&existing));
        }

        let mut key = Zeroizing::new([0_u8; 32]);
        getrandom::fill(&mut *key)
            .map_err(|error| ResolveError(format!("generate state encryption key: {error}")))?;

        match self.sources.provision_vault(VAULT_ENTRY_NAME, &key) {
            Ok(ProvisionOutcome::Created) => return self.verify_provisioned(&key, "symvault"),
            Ok(ProvisionOutcome::Existing) => return self.resolve_existing(),
            Err(ProbeError::Missing(_)) => {}
            Err(ProbeError::Failed(error)) => {
                return Err(ResolveError(format!(
                    "provision state encryption key in symvault: {error}"
                )));
            }
        }
        match self
            .sources
            .provision_keychain(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT, &key)
        {
            Ok(ProvisionOutcome::Created) => return self.verify_provisioned(&key, "keychain"),
            Ok(ProvisionOutcome::Existing) => return self.resolve_existing(),
            Err(ProbeError::Missing(_)) => {}
            Err(ProbeError::Failed(error)) => {
                return Err(ResolveError(format!(
                    "provision state encryption key in keychain: {error}"
                )));
            }
        }

        Ok(KeyInitResult {
            action: "configure_environment".to_owned(),
            configured: false,
            key_source: "environment".to_owned(),
            instruction: format!("export {ENV_KEY_NAME}='{}'", encode_hex(&key)),
        })
    }

    fn verify_provisioned(
        &self,
        expected: &[u8; 32],
        source: &str,
    ) -> Result<KeyInitResult, ResolveError> {
        self.invalidate();
        let Some(actual) = self.resolve()? else {
            return Err(verification_error());
        };
        if actual.source() != source || !actual.matches(expected) {
            return Err(verification_error());
        }
        Ok(KeyInitResult {
            action: "initialized".to_owned(),
            configured: true,
            key_source: source.to_owned(),
            instruction: String::new(),
        })
    }

    fn resolve_existing(&self) -> Result<KeyInitResult, ResolveError> {
        self.invalidate();
        self.resolve()?
            .as_ref()
            .map(already_configured)
            .ok_or_else(verification_error)
    }
}

fn already_configured(existing: &KeyMaterial) -> KeyInitResult {
    KeyInitResult {
        action: "already_configured".to_owned(),
        configured: true,
        key_source: existing.source().to_owned(),
        instruction: String::new(),
    }
}

fn verification_error() -> ResolveError {
    ResolveError(
        "verify state encryption key provisioning: provider did not return the new key".to_owned(),
    )
}

fn state_error(error: StateError) -> ResolveError {
    ResolveError(error.to_string())
}

pub fn parse_hex_key(raw: &str) -> Result<[u8; 32], ResolveError> {
    let raw = raw.trim();
    if raw.len() != 64 {
        return Err(invalid_key());
    }
    let mut key = Zeroizing::new([0_u8; 32]);
    for (index, pair) in raw.as_bytes().as_chunks::<2>().0.iter().enumerate() {
        let pair = std::str::from_utf8(pair).map_err(|_| invalid_key())?;
        key[index] = u8::from_str_radix(pair, 16).map_err(|_| invalid_key())?;
    }
    Ok(*key)
}

fn parse_keychain_key(raw: &[u8]) -> Result<[u8; 32], ResolveError> {
    if let Ok(raw) = <&[u8; 32]>::try_from(raw) {
        return Ok(*raw);
    }
    parse_hex_key(&String::from_utf8_lossy(raw)).map_err(|_| {
        ResolveError("keychain value must be a 64-character hex string or 32 raw bytes".to_owned())
    })
}

pub fn first_hex_token(raw: &str) -> &str {
    raw.split(|character: char| !character.is_ascii_hexdigit())
        .find(|field| field.len() >= 64)
        .map_or("", |field| &field[..64])
}

fn invalid_key() -> ResolveError {
    ResolveError("key must be a 64-character hex string (32 bytes)".to_owned())
}

fn encode_hex(key: &[u8; 32]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut encoded = String::with_capacity(64);
    for byte in key {
        encoded.push(HEX[(byte >> 4) as usize] as char);
        encoded.push(HEX[(byte & 0x0f) as usize] as char);
    }
    encoded
}
