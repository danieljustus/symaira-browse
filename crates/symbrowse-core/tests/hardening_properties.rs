use std::panic::{AssertUnwindSafe, catch_unwind};

use proptest::prelude::*;
use symbrowse_core::{
    flows,
    policy::{Allowlist, is_private_ip},
    state::{decrypt, encrypt, validate_name},
};

fn safe_state_name() -> impl Strategy<Value = String> {
    proptest::string::string_regex(r"[A-Za-z0-9_-]{1,32}").expect("valid state-name regex")
}

fn miri_config() -> ProptestConfig {
    let mut config = ProptestConfig::with_cases(if cfg!(miri) { 1 } else { 96 });
    // Miri isolates getcwd and intentionally cannot persist regression seeds.
    config.failure_persistence = None;
    config
}

proptest! {
    #![proptest_config(miri_config())]

    /// Untrusted YAML must resolve to a value, never panic while being bounded.
    #[test]
    fn flow_parser_never_panics(input in prop::collection::vec(any::<u8>(), 0..4096)) {
        let result = catch_unwind(AssertUnwindSafe(|| flows::parse(&input, "property")));
        prop_assert!(result.is_ok());
    }

    /// State encryption is a pure authenticated round trip for arbitrary bytes.
    #[test]
    fn state_crypto_round_trips(plaintext in prop::collection::vec(any::<u8>(), 0..4096), aad in prop::collection::vec(any::<u8>(), 0..128)) {
        let key = [0x5a; 32];
        let encrypted = encrypt(&plaintext, &aad, &key).expect("fixed key is valid");
        prop_assert_eq!(decrypt(&encrypted, &aad, &key).expect("ciphertext authenticates"), plaintext);
    }

    /// Valid state names remain path-free under arbitrary allowed characters.
    #[test]
    fn generated_state_names_are_accepted(name in safe_state_name()) {
        prop_assert!(validate_name(&name).is_ok());
    }

    /// A wildcard must not broaden into a substring match.
    #[test]
    fn allowlist_wildcards_respect_label_boundaries(label in safe_state_name()) {
        let allowlist = Allowlist::parse(&["*.example.test".to_owned()]).expect("valid pattern");
        let allowed = format!("{label}.example.test");
        let substring = format!("{label}notexample.test");
        prop_assert!(allowlist.allows_host(&allowed));
        prop_assert!(!allowlist.allows_host(&substring));
    }

    /// The public IP classifier is total for all parsed IPv4 values.
    #[test]
    fn private_ip_classifier_is_total(octets in any::<[u8; 4]>()) {
        let address = std::net::Ipv4Addr::from(octets);
        let _ = is_private_ip(address.into());
    }
}
