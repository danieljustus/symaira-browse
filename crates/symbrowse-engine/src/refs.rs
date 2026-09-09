use crate::snapshot::{SnapshotRef, SnapshotResult};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, BTreeSet};

/// Records why a previously issued ref can no longer be used.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct RefTombstone {
    pub refkey: String,
    #[serde(rename = "ref")]
    pub ref_name: String,
    pub role: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    pub reason: String,
}

/// Result of resolving a ref. A tombstone is returned instead of allowing a
/// dead ref to resolve to an unrelated live element.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum RefResolution {
    Live(SnapshotRef),
    Tombstone(RefTombstone),
}

#[derive(Clone, Debug)]
struct RefRecord {
    key: String,
    ref_name: String,
    snapshot: SnapshotRef,
    tombstone: Option<RefTombstone>,
    permanent: bool,
}

/// Session-local registry that preserves refs across snapshots and never
/// recycles a ref number after navigation.
#[derive(Clone, Debug)]
pub struct StableRefRegistry {
    by_key: BTreeMap<String, RefRecord>,
    by_ref: BTreeMap<String, RefRecord>,
    current: BTreeMap<String, String>,
    next: u64,
}

impl Default for StableRefRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl StableRefRegistry {
    pub fn new() -> Self {
        Self {
            by_key: BTreeMap::new(),
            by_ref: BTreeMap::new(),
            current: BTreeMap::new(),
            next: 1,
        }
    }

    /// Apply a freshly rendered snapshot, replacing temporary refs with the
    /// stable session-local refs allocated for their content-addressed keys.
    pub fn apply(&mut self, mut result: SnapshotResult) -> SnapshotResult {
        if !self.current.is_empty() {
            let seen: BTreeSet<String> = result
                .refs
                .values()
                .filter(|item| !item.refkey.is_empty())
                .map(|item| item.refkey.clone())
                .collect();
            let removed: Vec<String> = self
                .current
                .keys()
                .filter(|key| !seen.contains(*key))
                .cloned()
                .collect();
            for key in removed {
                if let Some(ref_name) = self.current.get(&key).cloned() {
                    self.tombstone_by_ref(&ref_name, "removed", false);
                }
            }
        }

        let mut old_to_new = BTreeMap::new();
        let mut stable_refs = BTreeMap::new();
        let mut by_key: BTreeMap<String, (String, SnapshotRef)> = BTreeMap::new();
        for (temporary_ref, snapshot_ref) in result.refs {
            if !snapshot_ref.refkey.is_empty() {
                by_key.insert(snapshot_ref.refkey.clone(), (temporary_ref, snapshot_ref));
            }
        }
        for (key, (temporary_ref, snapshot_ref)) in by_key {
            let ref_name = if let Some(record) = self.by_key.get_mut(&key) {
                if record.permanent {
                    self.new_record(key.clone())
                } else {
                    record.tombstone = None;
                    record.ref_name.clone()
                }
            } else {
                self.new_record(key.clone())
            };
            let record = self
                .by_key
                .get_mut(&key)
                .expect("record was inserted above");
            record.snapshot = snapshot_ref.clone();
            self.by_ref.insert(ref_name.clone(), record.clone());
            self.current.insert(key, ref_name.clone());
            stable_refs.insert(ref_name.clone(), snapshot_ref);
            old_to_new.insert(temporary_ref, ref_name);
        }
        let live_refs: BTreeSet<String> = stable_refs.keys().cloned().collect();
        self.current
            .retain(|_, ref_name| live_refs.contains(ref_name));
        result.refs = stable_refs;
        result.tree = replace_snapshot_refs(&result.tree, &old_to_new);
        result
    }

    /// Invalidate all current refs. `navigated` makes tombstones permanent;
    /// other reasons permit a matching key to become live again.
    pub fn invalidate(&mut self, reason: &str) {
        let refs: Vec<String> = self.current.values().cloned().collect();
        for ref_name in refs {
            self.tombstone_by_ref(&ref_name, reason, reason == "navigated");
        }
    }

    pub fn resolve(&self, ref_name: &str) -> Option<RefResolution> {
        let record = self.by_ref.get(ref_name)?;
        match &record.tombstone {
            Some(tombstone) => Some(RefResolution::Tombstone(tombstone.clone())),
            None => Some(RefResolution::Live(record.snapshot.clone())),
        }
    }

    pub fn next_ref_number(&self) -> u64 {
        self.next
    }

    fn new_record(&mut self, key: String) -> String {
        let ref_name = format!("e{}", self.next);
        self.next += 1;
        let record = RefRecord {
            key: key.clone(),
            ref_name: ref_name.clone(),
            snapshot: SnapshotRef::default(),
            tombstone: None,
            permanent: false,
        };
        self.by_key.insert(key, record.clone());
        self.by_ref.insert(ref_name.clone(), record);
        ref_name
    }

    fn tombstone_by_ref(&mut self, ref_name: &str, reason: &str, permanent: bool) {
        let Some(record) = self.by_ref.get_mut(ref_name) else {
            return;
        };
        let tombstone = record.tombstone.get_or_insert_with(|| RefTombstone {
            refkey: record.key.clone(),
            ref_name: record.ref_name.clone(),
            role: record.snapshot.role.clone(),
            name: record.snapshot.name.clone(),
            reason: reason.to_owned(),
        });
        tombstone.reason = reason.to_owned();
        record.permanent = permanent;
        let key = record.key.clone();
        if let Some(current) = self.by_key.get_mut(&key)
            && current.ref_name == ref_name
        {
            *current = record.clone();
        }
        self.current.remove(&key);
    }
}

/// Compute the content-addressed identity used by snapshot refs.
pub fn ref_key(
    role: &str,
    accessible_name: &str,
    normalized_dom_path: &str,
    sibling_ordinal: i32,
) -> String {
    let payload = [
        normalize_ref_part(role),
        normalize_ref_part(accessible_name),
        normalize_ref_part(normalized_dom_path),
        sibling_ordinal.to_string(),
    ]
    .join("\0");
    let digest = Sha256::digest(payload.as_bytes());
    hex_lower(&digest)
}

pub fn normalize_ref_part(value: &str) -> String {
    value.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn hex_lower(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        output.push(HEX[(byte >> 4) as usize] as char);
        output.push(HEX[(byte & 0x0f) as usize] as char);
    }
    output
}

fn replace_snapshot_refs(tree: &str, replacements: &BTreeMap<String, String>) -> String {
    let mut output = String::with_capacity(tree.len());
    let mut remaining = tree;
    while let Some(start) = remaining.find("[ref=") {
        output.push_str(&remaining[..start]);
        let token = &remaining[start..];
        let Some(end) = token.find(']') else {
            output.push_str(token);
            return output;
        };
        let old_ref = &token[5..end];
        if let Some(new_ref) = replacements.get(old_ref) {
            output.push_str("[ref=");
            output.push_str(new_ref);
            output.push(']');
        } else {
            output.push_str(&token[..=end]);
        }
        remaining = &token[end + 1..];
    }
    output.push_str(remaining);
    output
}

use serde::{Deserialize, Serialize};
