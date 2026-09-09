#![deny(unsafe_code)]

#[cfg(unix)]
mod unix {
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        path::PathBuf,
        sync::atomic::{AtomicU64, Ordering},
        time::Duration,
    };

    use symbrowse_core::{
        policy::{Decision, RiskClass},
        policy_guard::{Guard, GuardInput},
    };

    static NEXT: AtomicU64 = AtomicU64::new(1);

    #[test]
    fn guard_receives_stdin_json_and_validates_verdict() {
        let root = root("allow");
        let sent = root.join("sent.json");
        let executable = script(
            &root,
            &format!(
                "cat > {:?}\nprintf '%s\\n' '{{\"decision\":\"allow\",\"reason\":\"ok\"}}'\n",
                sent
            ),
        );
        let guard = Guard {
            executable,
            subcommand: vec!["guard".to_owned()],
            timeout: Duration::from_secs(2),
        };
        let outcome = guard
            .decide(&GuardInput {
                command: "open".to_owned(),
                class: RiskClass::Navigate,
                domain: "example.com".to_owned(),
                warnings: vec!["warning".to_owned()],
            })
            .unwrap();
        assert_eq!(outcome.decision, Decision::Allow);
        assert_eq!(outcome.reason, "ok");
        let payload: serde_json::Value = serde_json::from_slice(&fs::read(sent).unwrap()).unwrap();
        assert_eq!(payload["risk_class"], "medium");
        assert_eq!(payload["warnings"][0], "warning");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn guard_errors_fail_closed_and_timeout_is_bounded() {
        let root = root("invalid");
        let invalid = Guard {
            executable: script(&root, "printf '%s\\n' '{\"decision\":\"maybe\"}'\n"),
            subcommand: Vec::new(),
            timeout: Duration::from_secs(1),
        };
        assert!(invalid.decide(&input()).is_err());
        let timeout = Guard {
            executable: script(
                &root,
                "sleep 5\nprintf '%s\\n' '{\"decision\":\"allow\"}'\n",
            ),
            subcommand: Vec::new(),
            timeout: Duration::from_millis(20),
        };
        let error = timeout.decide(&input()).unwrap_err();
        assert!(error.to_string().contains("timed out"));
        let descendant = Guard {
            executable: script(
                &root,
                "sleep 5 &\nprintf '%s\\n' '{\"decision\":\"allow\"}'\nexit 0\n",
            ),
            subcommand: Vec::new(),
            timeout: Duration::from_millis(50),
        };
        let error = descendant.decide(&input()).unwrap_err();
        assert!(error.to_string().contains("timed out"));
        fs::remove_dir_all(root).unwrap();
    }

    fn input() -> GuardInput {
        GuardInput {
            command: "eval".to_owned(),
            class: RiskClass::Eval,
            domain: String::new(),
            warnings: Vec::new(),
        }
    }

    fn root(name: &str) -> PathBuf {
        let path = std::env::temp_dir().join(format!(
            "symbrowse-policy-guard-{name}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir_all(&path).unwrap();
        path
    }

    fn script(root: &std::path::Path, body: &str) -> PathBuf {
        let path = root.join(format!("guard-{}", NEXT.fetch_add(1, Ordering::Relaxed)));
        fs::write(&path, format!("#!/bin/sh\n{body}")).unwrap();
        let mut permissions = fs::metadata(&path).unwrap().permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(&path, permissions).unwrap();
        path
    }
}
