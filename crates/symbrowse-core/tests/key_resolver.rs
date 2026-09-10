#![deny(unsafe_code)]

use std::{
    fs,
    path::PathBuf,
    sync::{Arc, Barrier, Mutex},
    thread,
    time::Duration,
};

use serde::Deserialize;
use symbrowse_core::key_resolver::{
    KeyProvisioner, KeyResolver, KeySources, MissingReason, ProbeError, ProvisionOutcome,
    first_hex_token, parse_hex_key,
};

#[derive(Clone)]
struct FakeSources {
    state: Arc<Mutex<FakeState>>,
    delay: Duration,
}

struct FakeState {
    vault: Result<Option<Vec<u8>>, ProbeError>,
    keychain: Result<Option<Vec<u8>>, ProbeError>,
    environment: Option<String>,
    provision_vault: Result<ProvisionOutcome, ProbeError>,
    provision_keychain: Result<ProvisionOutcome, ProbeError>,
    calls: [usize; 3],
}

impl FakeSources {
    fn new(
        vault: Result<Option<Vec<u8>>, ProbeError>,
        keychain: Result<Option<Vec<u8>>, ProbeError>,
        environment: Option<String>,
    ) -> Self {
        Self {
            state: Arc::new(Mutex::new(FakeState {
                vault,
                keychain,
                environment,
                provision_vault: Err(ProbeError::Missing(MissingReason::Unavailable)),
                provision_keychain: Err(ProbeError::Missing(MissingReason::Unavailable)),
                calls: [0; 3],
            })),
            delay: Duration::ZERO,
        }
    }

    fn delayed(mut self) -> Self {
        self.delay = Duration::from_millis(25);
        self
    }

    fn with_vault_provision(self) -> Self {
        self.state.lock().unwrap().provision_vault = Ok(ProvisionOutcome::Created);
        self
    }

    fn with_keychain_provision(self) -> Self {
        self.state.lock().unwrap().provision_keychain = Ok(ProvisionOutcome::Created);
        self
    }

    fn calls(&self) -> [usize; 3] {
        self.state.lock().unwrap().calls
    }
}

impl KeySources for FakeSources {
    fn vault(&self, _entry: &str) -> Result<Option<Vec<u8>>, ProbeError> {
        {
            let mut state = self.state.lock().unwrap();
            state.calls[0] += 1;
        }
        if !self.delay.is_zero() {
            thread::sleep(self.delay);
        }
        self.state.lock().unwrap().vault.clone()
    }

    fn keychain(&self, _service: &str, _account: &str) -> Result<Option<Vec<u8>>, ProbeError> {
        let mut state = self.state.lock().unwrap();
        state.calls[1] += 1;
        state.keychain.clone()
    }

    fn environment(&self, _name: &str) -> Option<String> {
        let mut state = self.state.lock().unwrap();
        state.calls[2] += 1;
        state.environment.clone()
    }
}

impl KeyProvisioner for FakeSources {
    fn provision_vault(
        &self,
        _entry: &str,
        key: &[u8; 32],
    ) -> Result<ProvisionOutcome, ProbeError> {
        let mut state = self.state.lock().unwrap();
        let outcome = state.provision_vault.clone()?;
        if outcome == ProvisionOutcome::Existing {
            return Ok(outcome);
        }
        state.vault = Ok(Some(
            format!("{{\"value\":\"{}\"}}", encode(key)).into_bytes(),
        ));
        Ok(outcome)
    }

    fn provision_keychain(
        &self,
        _service: &str,
        _account: &str,
        key: &[u8; 32],
    ) -> Result<ProvisionOutcome, ProbeError> {
        let mut state = self.state.lock().unwrap();
        let outcome = state.provision_keychain.clone()?;
        if outcome == ProvisionOutcome::Existing {
            return Ok(outcome);
        }
        state.keychain = Ok(Some(key.to_vec()));
        Ok(outcome)
    }
}

fn missing() -> Result<Option<Vec<u8>>, ProbeError> {
    Err(ProbeError::Missing(MissingReason::NotFound))
}

fn hex(byte: &str) -> String {
    byte.repeat(32)
}

fn encode(key: &[u8; 32]) -> String {
    key.iter().map(|byte| format!("{byte:02x}")).collect()
}

#[test]
fn provider_precedence_and_absence_match_go() {
    let vault = format!("{{\"value\":\"{}\"}}", hex("ab")).into_bytes();
    let sources = FakeSources::new(
        Ok(Some(vault)),
        Ok(Some(hex("cd").into_bytes())),
        Some(hex("ef")),
    );
    let resolver = KeyResolver::new(sources.clone());
    assert_eq!(resolver.resolve().unwrap().unwrap().source(), "symvault");
    assert_eq!(sources.calls(), [1, 0, 0]);

    let sources = FakeSources::new(missing(), Ok(Some(hex("cd").into_bytes())), Some(hex("ef")));
    assert_eq!(
        KeyResolver::new(sources.clone())
            .resolve()
            .unwrap()
            .unwrap()
            .source(),
        "keychain"
    );
    assert_eq!(sources.calls(), [1, 1, 0]);

    let sources = FakeSources::new(missing(), Ok(None), Some(hex("ef")));
    assert_eq!(
        KeyResolver::new(sources.clone())
            .resolve()
            .unwrap()
            .unwrap()
            .source(),
        "environment"
    );
    assert_eq!(sources.calls(), [1, 1, 1]);

    let sources = FakeSources::new(missing(), Ok(None), None);
    assert!(KeyResolver::new(sources).resolve().unwrap().is_none());
}

#[test]
fn provider_failures_do_not_silently_fallback() {
    let sources = FakeSources::new(
        Err(ProbeError::Failed("vault locked".to_owned())),
        Ok(None),
        Some(hex("ef")),
    );
    let resolver = KeyResolver::new(sources.clone());
    let Err(error) = resolver.resolve() else {
        panic!("vault failure silently fell back");
    };
    assert!(error.0.contains("vault locked"));
    assert_eq!(sources.calls(), [1, 0, 0]);

    let sources = FakeSources::new(
        missing(),
        Err(ProbeError::Failed("keychain denied".to_owned())),
        Some(hex("ef")),
    );
    let resolver = KeyResolver::new(sources.clone());
    let Err(error) = resolver.resolve() else {
        panic!("keychain failure silently fell back");
    };
    assert_eq!(error.0, "keychain denied");
    assert_eq!(sources.calls(), [1, 1, 0]);
}

#[test]
fn key_formats_match_go() {
    assert!(parse_hex_key("short").is_err());
    assert!(parse_hex_key(&format!("zz{}", "ab".repeat(31))).is_err());
    assert_eq!(
        parse_hex_key(&format!(" {}\n", hex("ab"))).unwrap(),
        [0xab; 32]
    );
    assert_eq!(
        first_hex_token(&format!("prefix:{}:suffix", hex("cd"))),
        hex("cd")
    );

    let sources = FakeSources::new(missing(), Ok(Some(vec![0x11; 32])), None);
    assert_eq!(
        KeyResolver::new(sources)
            .resolve()
            .unwrap()
            .unwrap()
            .source(),
        "keychain"
    );
}

#[test]
fn successful_results_are_cached_and_invalidation_reprobes() {
    let sources = FakeSources::new(missing(), Ok(None), Some(hex("ab")));
    let resolver = KeyResolver::new(sources.clone());
    for _ in 0..5 {
        assert_eq!(resolver.resolve().unwrap().unwrap().source(), "environment");
    }
    assert_eq!(sources.calls(), [1, 1, 1]);
    resolver.invalidate();
    assert_eq!(resolver.resolve().unwrap().unwrap().source(), "environment");
    assert_eq!(sources.calls(), [2, 2, 2]);
}

#[test]
fn concurrent_callers_share_one_in_flight_resolution() {
    let sources = FakeSources::new(missing(), Ok(None), Some(hex("ab"))).delayed();
    let resolver = Arc::new(KeyResolver::new(sources.clone()));
    let barrier = Arc::new(Barrier::new(17));
    let mut threads = Vec::new();
    for _ in 0..16 {
        let resolver = Arc::clone(&resolver);
        let barrier = Arc::clone(&barrier);
        threads.push(thread::spawn(move || {
            barrier.wait();
            resolver.resolve().unwrap().unwrap().source().to_owned()
        }));
    }
    barrier.wait();
    for thread in threads {
        assert_eq!(thread.join().unwrap(), "environment");
    }
    assert_eq!(sources.calls(), [1, 1, 1]);
}

#[test]
fn initialization_never_rotates_an_existing_key() {
    let resolver = KeyResolver::new(FakeSources::new(missing(), Ok(None), Some(hex("ab"))));
    let result = resolver.initialize().unwrap();
    assert_eq!(result.action, "already_configured");
    assert!(result.configured);
    assert_eq!(result.key_source, "environment");
    assert!(result.instruction.is_empty());
}

#[test]
fn initialization_provisions_and_verifies_secure_providers() {
    let vault =
        KeyResolver::new(FakeSources::new(missing(), Ok(None), None).with_vault_provision());
    let result = vault.initialize().unwrap();
    assert_eq!(result.action, "initialized");
    assert_eq!(result.key_source, "symvault");

    let keychain =
        KeyResolver::new(FakeSources::new(missing(), Ok(None), None).with_keychain_provision());
    let result = keychain.initialize().unwrap();
    assert_eq!(result.action, "initialized");
    assert_eq!(result.key_source, "keychain");
}

#[test]
fn initialization_falls_back_to_one_time_environment_instruction() {
    let resolver = KeyResolver::new(FakeSources::new(missing(), Ok(None), None));
    let result = resolver.initialize().unwrap();
    assert_eq!(result.action, "configure_environment");
    assert!(!result.configured);
    assert_eq!(result.key_source, "environment");
    let encoded = result
        .instruction
        .strip_prefix("export SYMBROWSE_ENCRYPTION_KEY='")
        .and_then(|value| value.strip_suffix('\''))
        .unwrap();
    assert!(parse_hex_key(encoded).is_ok());
}

#[test]
fn initialization_fails_closed_on_provisioning_error() {
    let sources = FakeSources::new(missing(), Ok(None), None);
    sources.state.lock().unwrap().provision_vault =
        Err(ProbeError::Failed("vault locked".to_owned()));
    let resolver = KeyResolver::new(sources);
    let Err(error) = resolver.initialize() else {
        panic!("provisioning error silently fell back");
    };
    assert!(error.0.contains("vault locked"));
}

#[test]
fn concurrent_initialization_creates_only_one_key() {
    let sources = FakeSources::new(missing(), Ok(None), None).with_vault_provision();
    let resolver = Arc::new(KeyResolver::new(sources));
    let barrier = Arc::new(Barrier::new(9));
    let mut threads = Vec::new();
    for _ in 0..8 {
        let resolver = Arc::clone(&resolver);
        let barrier = Arc::clone(&barrier);
        threads.push(thread::spawn(move || {
            barrier.wait();
            resolver.initialize().unwrap().action.clone()
        }));
    }
    barrier.wait();
    let actions: Vec<_> = threads
        .into_iter()
        .map(|thread| thread.join().unwrap())
        .collect();
    assert_eq!(
        actions
            .iter()
            .filter(|action| *action == "initialized")
            .count(),
        1
    );
    assert_eq!(
        actions
            .iter()
            .filter(|action| *action == "already_configured")
            .count(),
        7
    );
}

#[derive(Deserialize)]
struct ResolutionManifest {
    resolution_cases: Vec<ResolutionCase>,
    init_cases: Vec<InitCase>,
}

#[derive(Deserialize)]
struct ResolutionCase {
    name: String,
    source: String,
    #[serde(default)]
    error: String,
}

#[derive(Deserialize)]
struct InitCase {
    name: String,
    #[serde(default)]
    action: String,
    configured: bool,
    #[serde(default)]
    source: String,
    instruction_present: bool,
    #[serde(default)]
    error: String,
}

#[test]
fn outcomes_match_go_generated_resolution_fixture() {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/state/manifest.json");
    let fixture: ResolutionManifest = serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
    assert_eq!(fixture.resolution_cases.len(), 9);
    for case in fixture.resolution_cases {
        let sources = match case.name.as_str() {
            "vault" => FakeSources::new(
                Ok(Some(
                    format!("{{\"value\":\"{}\"}}", hex("ab")).into_bytes(),
                )),
                Ok(None),
                None,
            ),
            "vault-missing-env" => FakeSources::new(missing(), Ok(None), Some(hex("cd"))),
            "vault-uninitialized-env" => FakeSources::new(
                Err(ProbeError::Missing(MissingReason::NotInitialized)),
                Ok(None),
                Some(hex("cd")),
            ),
            "vault-failure" => FakeSources::new(
                Err(ProbeError::Failed("provider failed".to_owned())),
                Ok(None),
                Some(hex("cd")),
            ),
            "keychain-raw" => FakeSources::new(missing(), Ok(Some(vec![0xab; 32])), None),
            "keychain-invalid" => FakeSources::new(missing(), Ok(Some(b"bad".to_vec())), None),
            "environment" => FakeSources::new(missing(), Ok(None), Some(hex("cd"))),
            "environment-invalid" => FakeSources::new(missing(), Ok(None), Some("bad".to_owned())),
            "none" => FakeSources::new(missing(), Ok(None), None),
            other => panic!("unknown Go fixture case {other}"),
        };
        match KeyResolver::new(sources).resolve() {
            Ok(Some(key)) => {
                assert!(case.error.is_empty(), "{}", case.name);
                assert_eq!(key.source(), case.source, "{}", case.name);
            }
            Ok(None) => {
                assert!(case.error.is_empty(), "{}", case.name);
                assert_eq!(case.source, "none", "{}", case.name);
            }
            Err(error) => assert_eq!(error.0, case.error, "{}", case.name),
        }
    }
}

#[test]
fn initialization_matches_go_generated_fixture() {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/state/manifest.json");
    let fixture: ResolutionManifest = serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
    assert_eq!(fixture.init_cases.len(), 4);
    for case in fixture.init_cases {
        let sources = match case.name.as_str() {
            "existing" => FakeSources::new(missing(), Ok(None), Some(hex("ab"))),
            "environment-fallback" => FakeSources::new(missing(), Ok(None), None),
            "vault" => FakeSources::new(missing(), Ok(None), None).with_vault_provision(),
            "vault-failure" => {
                let sources = FakeSources::new(missing(), Ok(None), None);
                sources.state.lock().unwrap().provision_vault =
                    Err(ProbeError::Failed("provider failed".to_owned()));
                sources
            }
            other => panic!("unknown Go init fixture {other}"),
        };
        match KeyResolver::new(sources).initialize() {
            Ok(actual) => {
                assert!(case.error.is_empty(), "{}", case.name);
                assert_eq!(actual.action, case.action, "{}", case.name);
                assert_eq!(actual.configured, case.configured, "{}", case.name);
                assert_eq!(actual.key_source, case.source, "{}", case.name);
                assert_eq!(
                    !actual.instruction.is_empty(),
                    case.instruction_present,
                    "{}",
                    case.name
                );
            }
            Err(error) => assert_eq!(error.0, case.error, "{}", case.name),
        }
    }
}
