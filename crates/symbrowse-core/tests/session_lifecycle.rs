use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use serde::Deserialize;
use serde_json::Value;
use symbrowse_core::session::{
    Clock, ControlState, HardStopError, Journal, Manager, Session, SessionError, Transition,
};

#[derive(Deserialize)]
struct Fixture {
    oracle_commit: String,
    generated_by: String,
    snapshots: BTreeMap<String, Value>,
    errors: BTreeMap<String, HardStopError>,
    transitions: Vec<Transition>,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/session/lifecycle.json"
    ))
    .expect("session fixture")
}

fn fixed_clock() -> Clock {
    Arc::new(|| "2026-08-06T12:00:00Z".to_owned())
}

fn hard_stop(error: SessionError) -> HardStopError {
    match error {
        SessionError::HardStop(error) => error,
        other => panic!("expected hard stop, got {other:?}"),
    }
}

#[test]
fn lifecycle_matches_go_generated_fixture() {
    let expected = fixture();
    assert_eq!(
        expected.oracle_commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(expected.generated_by, "scripts/rust-port/cmd/sessiongen");

    let transitions = Arc::new(Mutex::new(Vec::<Transition>::new()));
    let journal_transitions = Arc::clone(&transitions);
    let journal: Journal = Arc::new(move |transition| {
        journal_transitions
            .lock()
            .expect("transition lock")
            .push(transition.clone());
        Ok(())
    });
    let manager = Manager::new(Some(fixed_clock()), Some(journal.clone()));
    let mut snapshots = BTreeMap::new();
    let mut errors = BTreeMap::new();

    snapshots.insert(
        "created".to_owned(),
        serde_json::to_value(manager.create("session-1", "agent-a").unwrap()).unwrap(),
    );
    snapshots.insert(
        "delegated".to_owned(),
        serde_json::to_value(
            manager
                .handoff("session-1", "agent-a", "2FA required")
                .unwrap(),
        )
        .unwrap(),
    );
    errors.insert(
        "delegated_access".to_owned(),
        hard_stop(
            manager
                .check_agent_access("session-1", "agent-a")
                .unwrap_err(),
        ),
    );
    snapshots.insert(
        "claimed".to_owned(),
        serde_json::to_value(manager.claim("session-1", "human-a").unwrap()).unwrap(),
    );
    errors.insert(
        "unconfirmed_takeover".to_owned(),
        hard_stop(manager.takeover("session-1", "agent-b", false).unwrap_err()),
    );
    snapshots.insert(
        "after_denied_takeover".to_owned(),
        serde_json::to_value(manager.snapshot("session-1").unwrap()).unwrap(),
    );
    snapshots.insert(
        "taken_over".to_owned(),
        serde_json::to_value(manager.takeover("session-1", "agent-b", true).unwrap()).unwrap(),
    );
    let completed = manager.complete("session-1", "agent-b", true).unwrap();
    snapshots.insert(
        "completed".to_owned(),
        serde_json::to_value(&completed).unwrap(),
    );
    errors.insert(
        "completed_access".to_owned(),
        hard_stop(
            manager
                .check_agent_access("session-1", "agent-b")
                .unwrap_err(),
        ),
    );
    let restored = Manager::new(Some(fixed_clock()), None);
    restored.restore(completed).unwrap();
    snapshots.insert(
        "restored".to_owned(),
        serde_json::to_value(restored.reconnect("session-1").unwrap()).unwrap(),
    );

    let timeout_manager = Manager::new(Some(fixed_clock()), Some(journal));
    timeout_manager
        .create("session-timeout", "agent-a")
        .unwrap();
    timeout_manager
        .handoff("session-timeout", "agent-a", "CAPTCHA")
        .unwrap();
    let (timed_out, timeout_error) = timeout_manager.timeout("session-timeout").unwrap();
    snapshots.insert(
        "timed_out".to_owned(),
        serde_json::to_value(timed_out).unwrap(),
    );
    errors.insert("timeout".to_owned(), timeout_error);

    assert_eq!(snapshots, expected.snapshots);
    assert_eq!(errors, expected.errors);
    assert_eq!(
        *transitions.lock().expect("transition lock"),
        expected.transitions
    );
}

#[test]
fn journal_failure_prevents_state_mutation() {
    let calls = Arc::new(Mutex::new(0usize));
    let journal_calls = Arc::clone(&calls);
    let journal: Journal = Arc::new(move |_| {
        let mut calls = journal_calls.lock().expect("journal lock");
        *calls += 1;
        if *calls == 2 {
            Err("journal unavailable".to_owned())
        } else {
            Ok(())
        }
    });
    let manager = Manager::new(Some(fixed_clock()), Some(journal));
    manager.create("session", "agent").unwrap();
    assert!(matches!(
        manager.handoff("session", "agent", "2FA"),
        Err(SessionError::Journal(message)) if message == "journal unavailable"
    ));
    let snapshot = manager.snapshot("session").unwrap();
    assert_eq!(snapshot.control_id, "agent");
    assert_eq!(snapshot.handoff_reason, "");
    manager.check_agent_access("session", "agent").unwrap();
}

#[test]
fn restore_rehydrates_without_emitting_a_new_transition() {
    let transitions = Arc::new(Mutex::new(Vec::<Transition>::new()));
    let journal_transitions = Arc::clone(&transitions);
    let journal: Journal = Arc::new(move |transition| {
        journal_transitions
            .lock()
            .expect("transition lock")
            .push(transition.clone());
        Ok(())
    });
    let manager = Manager::new(Some(fixed_clock()), Some(journal));
    let snapshot = Session {
        id: "restored".to_owned(),
        control_id: "agent".to_owned(),
        control_state: ControlState::Agent,
        active: true,
        handoff_reason: String::new(),
        updated_at: "2026-08-06T12:00:00Z".to_owned(),
        completion: None,
    };
    manager.restore(snapshot.clone()).unwrap();
    assert_eq!(manager.snapshot("restored").unwrap(), snapshot);
    assert!(transitions.lock().expect("transition lock").is_empty());
}

#[test]
fn restore_rejects_the_transition_only_initial_state() {
    let manager = Manager::new(Some(fixed_clock()), None);
    let invalid = Session {
        id: "session".to_owned(),
        control_id: "agent".to_owned(),
        control_state: ControlState::Initial,
        active: true,
        handoff_reason: String::new(),
        updated_at: "2026-08-06T12:00:00Z".to_owned(),
        completion: None,
    };
    assert!(matches!(
        manager.restore(invalid),
        Err(SessionError::Invalid(message)) if message == "invalid control state \"\""
    ));
}
