#![deny(unsafe_code)]

#[cfg(unix)]
mod unix {
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        path::{Path, PathBuf},
        sync::{
            Arc,
            atomic::{AtomicU64, Ordering},
        },
        time::{Duration, Instant},
    };

    use symbrowse_core::{
        key_resolver::{
            KeyProvisioner, KeyResolver, KeySources, MissingReason, ProbeError, ProvisionOutcome,
        },
        key_sources::SystemKeySources,
    };

    static NEXT_ROOT: AtomicU64 = AtomicU64::new(1);

    #[test]
    fn system_vault_output_flows_through_resolver() {
        let root = root("success");
        let vault = script(
            &root,
            "vault-success",
            "#!/bin/sh\nprintf '{\"value\":\"abababababababababababababababababababababababababababababababab\"}\\n'\n",
        );
        let sources =
            SystemKeySources::with_programs(&vault, "/missing/security", Duration::from_secs(1));
        let resolved = KeyResolver::new(sources).resolve().unwrap().unwrap();
        assert_eq!(resolved.source(), "symvault");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn system_vault_exit_codes_preserve_fallback_semantics() {
        for (code, expected) in [
            (2, ProbeError::Missing(MissingReason::NotFound)),
            (3, ProbeError::Missing(MissingReason::NotInitialized)),
            (4, ProbeError::Failed("exit status 4".to_owned())),
        ] {
            let root = root(&format!("exit-{code}"));
            let vault = script(&root, "vault-exit", &format!("#!/bin/sh\nexit {code}\n"));
            let sources = SystemKeySources::with_programs(
                &vault,
                "/missing/security",
                Duration::from_secs(1),
            );
            assert_eq!(sources.vault("symbrowse/encryption-key"), Err(expected));
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn vault_set_passes_key_only_through_stdin() {
        let root = root("set");
        let input_path = root.join("input");
        let args_path = root.join("args");
        let vault = script(
            &root,
            "vault-set",
            &format!(
                "#!/bin/sh\nprintf '%s' \"$*\" > '{}'\nIFS= read -r value\nprintf '%s' \"$value\" > '{}'\n",
                args_path.display(),
                input_path.display()
            ),
        );
        let sources =
            SystemKeySources::with_programs(&vault, "/missing/security", Duration::from_secs(1));
        sources
            .set_vault("symbrowse/encryption-key", &[0xab; 32])
            .unwrap();
        let encoded = "ab".repeat(32);
        assert_eq!(fs::read_to_string(input_path).unwrap(), encoded);
        let args = fs::read_to_string(args_path).unwrap();
        assert_eq!(args, "set symbrowse/encryption-key --stdin-value");
        assert!(!args.contains(&encoded));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn command_timeout_is_bounded() {
        let root = root("timeout");
        let vault = script(&root, "vault-sleep", "#!/bin/sh\nsleep 1\n");
        let sources =
            SystemKeySources::with_programs(&vault, "/missing/security", Duration::from_millis(20));
        let started = Instant::now();
        let error = sources.vault("symbrowse/encryption-key").unwrap_err();
        assert!(matches!(error, ProbeError::Failed(message) if message.contains("timed out")));
        assert!(started.elapsed() < Duration::from_millis(500));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn descendant_holding_stdout_cannot_extend_timeout() {
        let root = root("descendant");
        let vault = script(&root, "vault-child", "#!/bin/sh\n(sleep 5) &\nexit 0\n");
        let sources =
            SystemKeySources::with_programs(&vault, "/missing/security", Duration::from_millis(20));
        let started = Instant::now();
        let error = sources.vault("symbrowse/encryption-key").unwrap_err();
        assert!(
            matches!(error, ProbeError::Failed(message) if message.contains("stdout pipe") || message.contains("timed out"))
        );
        assert!(started.elapsed() < Duration::from_millis(500));
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn keychain_set_prompts_and_receives_key_only_on_stdin() {
        let root = root("keychain-set");
        let input_path = root.join("input");
        let args_path = root.join("args");
        let security = script(
            &root,
            "security-set",
            &format!(
                "#!/bin/sh\nprintf '%s' \"$*\" > '{}'\nIFS= read -r value\nprintf '%s' \"$value\" > '{}'\n",
                args_path.display(),
                input_path.display()
            ),
        );
        let sources =
            SystemKeySources::with_programs("/missing/symvault", &security, Duration::from_secs(1));
        sources
            .set_keychain("symbrowse", "encryption-key", &[0xab; 32])
            .unwrap();
        let encoded = "ab".repeat(32);
        assert_eq!(fs::read_to_string(input_path).unwrap(), encoded);
        let args = fs::read_to_string(args_path).unwrap();
        assert_eq!(
            args,
            "add-generic-password -s symbrowse -a encryption-key -w"
        );
        assert!(!args.contains(&encoded));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn interprocess_lock_prevents_vault_key_rotation() {
        let root = root("cross-process");
        let value_path = root.join("value");
        let vault = script(
            &root,
            "vault-atomic",
            &format!(
                "#!/bin/sh\ncase \"$1\" in\nget) [ -f '{}' ] || exit 2; printf '{{\"value\":\"'; cat '{}'; printf '\"}}\\n' ;;\nset) IFS= read -r value; printf '%s' \"$value\" > '{}' ;;\nesac\n",
                value_path.display(),
                value_path.display(),
                value_path.display()
            ),
        );
        let first = Arc::new(SystemKeySources::with_programs(
            &vault,
            "/missing/security",
            Duration::from_secs(1),
        ));
        let second = Arc::new(SystemKeySources::with_programs(
            &vault,
            "/missing/security",
            Duration::from_secs(1),
        ));
        let left = {
            let first = Arc::clone(&first);
            std::thread::spawn(move || {
                first.provision_vault("symbrowse/encryption-key", &[0xaa; 32])
            })
        };
        let right = {
            let second = Arc::clone(&second);
            std::thread::spawn(move || {
                second.provision_vault("symbrowse/encryption-key", &[0xbb; 32])
            })
        };
        let outcomes = [
            left.join().unwrap().unwrap(),
            right.join().unwrap().unwrap(),
        ];
        assert_eq!(
            outcomes
                .iter()
                .filter(|outcome| **outcome == ProvisionOutcome::Created)
                .count(),
            1
        );
        assert_eq!(
            outcomes
                .iter()
                .filter(|outcome| **outcome == ProvisionOutcome::Existing)
                .count(),
            1
        );
        let stored = fs::read_to_string(value_path).unwrap();
        assert!(stored == "aa".repeat(32) || stored == "bb".repeat(32));
        fs::remove_dir_all(root).unwrap();
    }

    fn root(name: &str) -> PathBuf {
        let id = NEXT_ROOT.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "symbrowse-key-sources-{name}-{}-{id}",
            std::process::id()
        ));
        fs::create_dir_all(&root).unwrap();
        root
    }

    fn script(root: &Path, name: &str, content: &str) -> PathBuf {
        let path = root.join(name);
        fs::write(&path, content).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        path
    }
}
