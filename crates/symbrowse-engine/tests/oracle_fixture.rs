use serde::Deserialize;
use serde_json::Value;
use std::collections::BTreeMap;
use symbrowse_engine::capabilities::{Capabilities, OPTIONAL_INTERFACE_NAMES, capabilities_for};
use symbrowse_engine::diff::diff_snapshots;
use symbrowse_engine::refs::{RefResolution, StableRefRegistry, ref_key};
use symbrowse_engine::snapshot::{SnapshotOptions, SnapshotResult, render_snapshot};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    capabilities: CapabilityFixture,
    ref_keys: BTreeMap<String, String>,
    registry: RegistryFixture,
    snapshot: SnapshotFixture,
    routes: Vec<RouteFixture>,
    diff: DiffFixture,
}

#[derive(Deserialize)]
struct CapabilityFixture {
    empty: Capabilities,
    partial: Capabilities,
}

#[derive(Deserialize)]
struct RegistryFixture {
    first: SnapshotResult,
    stable: SnapshotResult,
    removed: symbrowse_engine::refs::RefTombstone,
    navigated: SnapshotResult,
}

#[derive(Deserialize)]
struct SnapshotFixture {
    nodes: Vec<Value>,
    full: SnapshotResult,
    interactive_compact: SnapshotResult,
    depth_1: SnapshotResult,
    urls: SnapshotResult,
}

#[derive(Deserialize)]
struct RouteFixture {
    name: String,
    nodes: Vec<Value>,
    after_nodes: Vec<Value>,
    before: SnapshotResult,
    after: SnapshotResult,
    diff: DiffFixture,
}

#[derive(Deserialize)]
struct DiffFixture {
    added: Vec<symbrowse_engine::snapshot::SnapshotRef>,
    removed: Vec<symbrowse_engine::snapshot::SnapshotRef>,
    changed: Vec<symbrowse_engine::diff::SnapshotChange>,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/engine/engine-neutral.json"
    ))
    .unwrap()
}

#[test]
fn go_oracle_fixture_has_pinned_surface() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(OPTIONAL_INTERFACE_NAMES.len(), 19);
    assert_eq!(fixture.routes.len(), 17);
    assert_eq!(fixture.snapshot.nodes.len(), 8);
    assert_eq!(
        fixture.capabilities.empty,
        capabilities_for("static", ["__none__"])
    );
}

#[test]
fn capabilities_and_ref_keys_match_go_oracle() {
    let fixture = fixture();
    assert_eq!(
        fixture.capabilities.empty,
        capabilities_for("static", ["__none__"])
    );
    assert_eq!(
        fixture.capabilities.partial,
        capabilities_for("chrome", ["TabManager", "FileTransfer"])
    );
    assert_eq!(
        fixture.ref_keys["button|Save|/document/button|0"],
        ref_key("button", "Save", "/document/button", 0)
    );
    assert_eq!(
        fixture.ref_keys["button|Save|/document/button|1"],
        ref_key("button", "Save", "/document/button", 1)
    );
}

#[test]
fn stable_ref_registry_matches_go_oracle_transitions() {
    let fixture = fixture();
    let mut registry = StableRefRegistry::new();
    let first = registry.apply(SnapshotResult {
        tree: "- button [ref=e1]".into(),
        refs: BTreeMap::from([(
            "e1".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "button".into(),
                name: "Save".into(),
                refkey: "key-save".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    assert_eq!(first, fixture.registry.first);
    let stable = registry.apply(SnapshotResult {
        tree: "- button [ref=e7]".into(),
        refs: BTreeMap::from([(
            "e7".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "button".into(),
                name: "Save".into(),
                refkey: "key-save".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    assert_eq!(stable, fixture.registry.stable);
    registry.apply(SnapshotResult {
        refs: BTreeMap::from([(
            "e2".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "link".into(),
                name: "Home".into(),
                refkey: "key-home".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    assert_eq!(
        registry.resolve("e1"),
        Some(RefResolution::Tombstone(fixture.registry.removed.clone()))
    );
    registry.invalidate("navigated");
    let navigated = registry.apply(SnapshotResult {
        refs: BTreeMap::from([(
            "e1".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "button".into(),
                refkey: "key-save".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    assert_eq!(navigated, fixture.registry.navigated);

    let mut permanent = StableRefRegistry::new();
    permanent.apply(SnapshotResult {
        refs: BTreeMap::from([(
            "e1".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "button".into(),
                refkey: "same-key".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    permanent.invalidate("navigated");
    let current = permanent.apply(SnapshotResult {
        refs: BTreeMap::from([(
            "e1".into(),
            symbrowse_engine::snapshot::SnapshotRef {
                role: "button".into(),
                refkey: "same-key".into(),
                ..Default::default()
            },
        )]),
        ..Default::default()
    });
    assert!(current.refs.contains_key("e2"));
    assert!(matches!(
        permanent.resolve("e1"),
        Some(RefResolution::Tombstone(tombstone)) if tombstone.reason == "navigated"
    ));
}

#[test]
fn snapshot_rendering_matches_go_oracle_bytes() {
    let fixture = fixture();
    assert_eq!(
        render_snapshot(&fixture.snapshot.nodes, &SnapshotOptions::default()).unwrap(),
        fixture.snapshot.full
    );
    assert_eq!(
        render_snapshot(
            &fixture.snapshot.nodes,
            &SnapshotOptions {
                interactive: true,
                compact: true,
                ..Default::default()
            }
        )
        .unwrap(),
        fixture.snapshot.interactive_compact
    );
    assert_eq!(
        render_snapshot(
            &fixture.snapshot.nodes,
            &SnapshotOptions {
                depth: 1,
                ..Default::default()
            }
        )
        .unwrap(),
        fixture.snapshot.depth_1
    );
    assert_eq!(
        render_snapshot(
            &fixture.snapshot.nodes,
            &SnapshotOptions {
                urls: true,
                ..Default::default()
            }
        )
        .unwrap(),
        fixture.snapshot.urls
    );
}

#[test]
fn all_registered_fixture_routes_match_go_oracle() {
    let fixture = fixture();
    for route in fixture.routes {
        let before = render_snapshot(&route.nodes, &SnapshotOptions::default()).unwrap();
        let mut registry = StableRefRegistry::new();
        let mut before = registry.apply(before);
        before.tree.clear();
        assert_eq!(
            before, route.before,
            "before snapshot mismatch for {}",
            route.name
        );
        let mut after = registry
            .apply(render_snapshot(&route.after_nodes, &SnapshotOptions::default()).unwrap());
        after.tree.clear();
        assert_eq!(
            after, route.after,
            "after snapshot mismatch for {}",
            route.name
        );
        let (added, removed, changed) = diff_snapshots(&before, &after, true);
        assert_eq!(added, route.diff.added, "added mismatch for {}", route.name);
        assert_eq!(
            removed, route.diff.removed,
            "removed mismatch for {}",
            route.name
        );
        assert_eq!(
            changed, route.diff.changed,
            "changed mismatch for {}",
            route.name
        );
    }
}

#[test]
fn snapshot_diff_classification_matches_go_oracle() {
    let fixture = fixture();
    let before = SnapshotResult {
        refs: BTreeMap::from([
            (
                "e1".into(),
                symbrowse_engine::snapshot::SnapshotRef {
                    refkey: "key-1".into(),
                    role: "button".into(),
                    name: "Save".into(),
                    value: "old".into(),
                    visible: true,
                    ..Default::default()
                },
            ),
            (
                "e2".into(),
                symbrowse_engine::snapshot::SnapshotRef {
                    refkey: "key-2".into(),
                    role: "link".into(),
                    name: "Old".into(),
                    visible: true,
                    ..Default::default()
                },
            ),
        ]),
        ..Default::default()
    };
    let after = SnapshotResult {
        refs: BTreeMap::from([
            (
                "e1".into(),
                symbrowse_engine::snapshot::SnapshotRef {
                    refkey: "key-1".into(),
                    role: "button".into(),
                    name: "Save".into(),
                    value: "new".into(),
                    visible: true,
                    ..Default::default()
                },
            ),
            (
                "e3".into(),
                symbrowse_engine::snapshot::SnapshotRef {
                    refkey: "key-3".into(),
                    role: "textbox".into(),
                    name: "Search".into(),
                    visible: true,
                    ..Default::default()
                },
            ),
        ]),
        ..Default::default()
    };
    let (added, removed, changed) = diff_snapshots(&before, &after, true);
    assert_eq!(added, fixture.diff.added);
    assert_eq!(removed, fixture.diff.removed);
    assert_eq!(changed, fixture.diff.changed);
}
