use crate::snapshot::{SnapshotRef, SnapshotResult};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct SnapshotDiffResult {
    pub snapshot_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tree: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub refs: BTreeMap<String, SnapshotRef>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hint: String,
    pub added: Vec<SnapshotRef>,
    pub removed: Vec<SnapshotRef>,
    pub changed: Vec<SnapshotChange>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SnapshotChange {
    #[serde(rename = "ref")]
    pub ref_name: String,
    pub before: SnapshotRef,
    pub after: SnapshotRef,
}

/// Compare two rendered snapshots. Matching refs are compared by the same
/// fields as the Go oracle; same-epoch ref-key changes are paired structurally
/// so accessible-name changes are reported as `changed`, not add/remove.
pub fn diff_snapshots(
    previous: &SnapshotResult,
    current: &SnapshotResult,
    same_epoch: bool,
) -> (Vec<SnapshotRef>, Vec<SnapshotRef>, Vec<SnapshotChange>) {
    let mut matched_before = BTreeSet::new();
    let mut matched_after = BTreeSet::new();
    let mut changed = Vec::new();
    for (ref_name, after) in &current.refs {
        let Some(before) = previous.refs.get(ref_name) else {
            continue;
        };
        matched_before.insert(ref_name.clone());
        matched_after.insert(ref_name.clone());
        if snapshot_ref_changed(before, after) {
            changed.push(SnapshotChange {
                ref_name: ref_name.clone(),
                before: before.clone(),
                after: after.clone(),
            });
        }
    }

    if same_epoch {
        let before_keys: BTreeMap<String, String> = previous
            .refs
            .iter()
            .filter(|(name, _)| !matched_before.contains(*name))
            .map(|(name, item)| (snapshot_structural_key(item), name.clone()))
            .collect();
        for (ref_name, after) in &current.refs {
            if matched_after.contains(ref_name) {
                continue;
            }
            let Some(old_ref) = before_keys.get(&snapshot_structural_key(after)) else {
                continue;
            };
            if matched_before.contains(old_ref) {
                continue;
            }
            matched_before.insert(old_ref.clone());
            matched_after.insert(ref_name.clone());
            changed.push(SnapshotChange {
                ref_name: ref_name.clone(),
                before: previous.refs[old_ref].clone(),
                after: after.clone(),
            });
        }
    }

    let mut added: Vec<_> = current
        .refs
        .iter()
        .filter(|(name, _)| !matched_after.contains(*name))
        .map(|(_, item)| item.clone())
        .collect();
    let mut removed: Vec<_> = previous
        .refs
        .iter()
        .filter(|(name, _)| !matched_before.contains(*name))
        .map(|(_, item)| item.clone())
        .collect();
    added.sort_by(|left, right| left.refkey.cmp(&right.refkey));
    removed.sort_by(|left, right| left.refkey.cmp(&right.refkey));
    changed.sort_by(|left, right| left.ref_name.cmp(&right.ref_name));
    (added, removed, changed)
}

pub fn full_snapshot_diff(current: SnapshotResult) -> SnapshotDiffResult {
    SnapshotDiffResult {
        snapshot_id: current.snapshot_id.clone(),
        tree: current.tree.clone(),
        refs: current.refs.clone(),
        hint: "no previous snapshot available; returning the full snapshot".to_owned(),
        added: Vec::new(),
        removed: Vec::new(),
        changed: Vec::new(),
    }
}

pub fn snapshot_diff(
    previous: Option<&SnapshotResult>,
    current: &SnapshotResult,
    same_epoch: bool,
) -> SnapshotDiffResult {
    let Some(previous) = previous else {
        return full_snapshot_diff(current.clone());
    };
    let (added, removed, changed) = diff_snapshots(previous, current, same_epoch);
    SnapshotDiffResult {
        snapshot_id: current.snapshot_id.clone(),
        tree: String::new(),
        refs: BTreeMap::new(),
        hint: String::new(),
        added,
        removed,
        changed,
    }
}

fn snapshot_ref_changed(before: &SnapshotRef, after: &SnapshotRef) -> bool {
    before.name != after.name
        || before.state != after.state
        || before.value != after.value
        || before.visible != after.visible
}

fn snapshot_structural_key(item: &SnapshotRef) -> String {
    let mut path = item.dom_path.clone();
    while let Some(start) = path.rfind('[') {
        let Some(end) = path[start..].find(']') else {
            break;
        };
        path.replace_range(start..=start + end, "");
    }
    format!("{}|{}|{}", item.role, path, item.sibling_ordinal)
}
