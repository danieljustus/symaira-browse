//! Append-only JSONL journal and secret redaction.

use serde::{Deserialize, Serialize};
use serde_json::Value;
#[cfg(not(unix))]
use std::fs::OpenOptions;
use std::{
    collections::BTreeSet,
    fs::{self, File},
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
};

pub const SCHEMA_VERSION: i64 = 1;
const MASK: &str = "••••";
const MAX_ENTRY_BYTES: usize = 1 << 20;

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct Entry {
    pub schema_version: i64,
    pub timestamp: String,
    pub session: String,
    pub command: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub ref_key: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub url_before: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub url_after: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub args: Option<Value>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub result: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub risk_class: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub decider: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u64>,
}

#[derive(Clone, Debug, Default)]
pub struct Redactor {
    pub keys: BTreeSet<String>,
    pub values: Vec<String>,
}
impl Redactor {
    #[must_use]
    pub fn standard() -> Self {
        Self {
            keys: [
                "password",
                "pass",
                "secret",
                "token",
                "authorization",
                "api_key",
                "apikey",
                "cookie",
                "credential",
                "value",
            ]
            .into_iter()
            .map(str::to_owned)
            .collect(),
            values: Vec::new(),
        }
    }
    #[must_use]
    pub fn redact(&self, value: &Value) -> Value {
        let mut redacted = self.redact_value(value);
        if !self.values.is_empty() {
            replace_values(&mut redacted, &self.values);
        }
        redacted
    }
    #[must_use]
    pub fn redact_text(&self, mut text: String) -> String {
        for value in &self.values {
            if !value.is_empty() {
                text = text.replace(value, MASK);
            }
        }
        text
    }
    fn redact_value(&self, value: &Value) -> Value {
        match value {
            Value::Object(object) => Value::Object(
                object
                    .iter()
                    .map(|(key, value)| {
                        if self.masks_key(key) {
                            (key.clone(), Value::String(MASK.to_owned()))
                        } else {
                            (key.clone(), self.redact_value(value))
                        }
                    })
                    .collect(),
            ),
            Value::Array(values) => Value::Array(
                values
                    .iter()
                    .map(|value| self.redact_value(value))
                    .collect(),
            ),
            value => value.clone(),
        }
    }
    fn masks_key(&self, key: &str) -> bool {
        let key = key.to_ascii_lowercase();
        self.keys.iter().any(|candidate| {
            key == *candidate
                || key.ends_with(&format!("_{candidate}"))
                || key.ends_with(&format!("-{candidate}"))
        })
    }
}
fn replace_values(value: &mut Value, values: &[String]) {
    match value {
        Value::String(text) => {
            for secret in values {
                if !secret.is_empty() {
                    *text = text.replace(secret, MASK);
                }
            }
        }
        Value::Array(items) => {
            for item in items {
                replace_values(item, values);
            }
        }
        Value::Object(items) => {
            for item in items.values_mut() {
                replace_values(item, values);
            }
        }
        _ => {}
    }
}

#[derive(Clone, Debug)]
pub struct Store {
    path: PathBuf,
    redactor: Redactor,
    timestamp: String,
}
impl Store {
    pub fn new(
        dir: &Path,
        session: &str,
        redactor: Redactor,
        timestamp: impl Into<String>,
    ) -> std::io::Result<Self> {
        if session.trim().is_empty()
            || session.contains('/')
            || session.contains('\\')
            || session.contains("..")
        {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "invalid journal session",
            ));
        }
        fs::create_dir_all(dir)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(dir, fs::Permissions::from_mode(0o700))?;
        }
        if fs::symlink_metadata(dir)?.file_type().is_symlink() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "journal directory must not be a symlink",
            ));
        }
        Ok(Self {
            path: dir.join(format!("{session}.jsonl")),
            redactor,
            timestamp: timestamp.into(),
        })
    }
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }
    pub fn append(&self, mut entry: Entry) -> std::io::Result<Entry> {
        entry.schema_version = SCHEMA_VERSION;
        if entry.timestamp.is_empty() {
            entry.timestamp = self.timestamp.clone();
        }
        if let Some(args) = entry.args.as_mut() {
            *args = self.redactor.redact(args);
        }
        entry.reason = self.redactor.redact_text(std::mem::take(&mut entry.reason));
        if !entry.result.is_empty() && !entry.result.starts_with("error") {
            entry.result = "ok".to_owned();
        }
        let mut line = serde_json::to_vec(&entry).map_err(std::io::Error::other)?;
        line.push(b'\n');
        if line.len() > MAX_ENTRY_BYTES {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "journal entry exceeds size limit",
            ));
        }
        let mut file = open_append(&self.path)?;
        fs2::FileExt::lock_exclusive(&file)?;
        let result = (|| {
            file.write_all(&line)?;
            file.sync_all()
        })();
        let unlock = fs2::FileExt::unlock(&file);
        result?;
        unlock?;
        Ok(entry)
    }
    pub fn read(&self) -> std::io::Result<Vec<Entry>> {
        let file = match open_read(&self.path) {
            Ok(file) => file,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(error) => return Err(error),
        };
        let mut entries = Vec::new();
        for line in BufReader::new(file).lines() {
            let line = line?;
            if line.trim().is_empty() {
                continue;
            }
            entries.push(serde_json::from_str(&line).unwrap_or_else(|_| Entry {
                schema_version: SCHEMA_VERSION,
                command: "<corrupt>".to_owned(),
                result: "error:corrupt".to_owned(),
                args: Some(Value::String(self.redactor.redact_text(line))),
                ..Entry::default()
            }));
        }
        Ok(entries)
    }
    pub fn tail(&self, count: usize) -> std::io::Result<Vec<Entry>> {
        let entries = self.read()?;
        if count == 0 || count >= entries.len() {
            Ok(entries)
        } else {
            Ok(entries[entries.len() - count..].to_vec())
        }
    }

    /// List journal sessions in deterministic order, ignoring unrelated files.
    pub fn sessions(dir: &Path) -> std::io::Result<Vec<String>> {
        let mut names = match fs::read_dir(dir) {
            Ok(entries) => entries
                .filter_map(Result::ok)
                .filter(|entry| entry.file_type().is_ok_and(|kind| kind.is_file()))
                .filter_map(|entry| {
                    let name = entry.file_name().to_string_lossy().into_owned();
                    name.strip_suffix(".jsonl").map(ToOwned::to_owned)
                })
                .collect::<Vec<_>>(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(error) => return Err(error),
        };
        names.sort();
        Ok(names)
    }
}

#[cfg(unix)]
fn open_append(path: &Path) -> std::io::Result<File> {
    use rustix::fs::{Mode, OFlags, open};
    open(
        path,
        OFlags::WRONLY | OFlags::APPEND | OFlags::CREATE | OFlags::NOFOLLOW,
        Mode::RUSR | Mode::WUSR,
    )
    .map(File::from)
    .map_err(std::io::Error::other)
}

#[cfg(not(unix))]
fn open_append(path: &Path) -> std::io::Result<File> {
    let mut options = OpenOptions::new();
    options.create(true).append(true);
    options.open(path)
}

#[cfg(unix)]
fn open_read(path: &Path) -> std::io::Result<File> {
    use rustix::fs::{Mode, OFlags, open};
    open(path, OFlags::RDONLY | OFlags::NOFOLLOW, Mode::empty())
        .map(File::from)
        .map_err(std::io::Error::other)
}

#[cfg(not(unix))]
fn open_read(path: &Path) -> std::io::Result<File> {
    File::open(path)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn redacts_nested_values_without_masking_names() {
        let mut r = Redactor::standard();
        r.values.push("fixture-secret-value".to_owned());
        let out = r.redact(&serde_json::json!({"username":"ada", "password":"fixture-secret-value", "nested":{"keep":"fixture-secret-value-x"}}));
        assert_eq!(out["password"], MASK);
        assert_eq!(out["username"], "ada");
        assert!(!out.to_string().contains("fixture-secret-value"));
    }

    #[cfg(unix)]
    #[test]
    fn store_rejects_symlinks_and_preserves_concurrent_lines() {
        use std::os::unix::fs::{PermissionsExt, symlink};
        use std::sync::Arc;

        let root = std::env::temp_dir().join(format!(
            "symbrowse-journal-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let store = Arc::new(Store::new(&root, "session", Redactor::standard(), "fixed").unwrap());
        assert_eq!(
            fs::metadata(&root).unwrap().permissions().mode() & 0o777,
            0o700
        );
        let mut workers = Vec::new();
        for index in 0..32 {
            let store = Arc::clone(&store);
            workers.push(std::thread::spawn(move || {
                store
                    .append(Entry {
                        command: format!("command-{index}"),
                        ..Entry::default()
                    })
                    .unwrap();
            }));
        }
        for worker in workers {
            worker.join().unwrap();
        }
        assert_eq!(store.read().unwrap().len(), 32);
        assert_eq!(
            fs::metadata(store.path()).unwrap().permissions().mode() & 0o777,
            0o600
        );

        let outside = root.with_extension("outside");
        fs::write(&outside, b"untouched").unwrap();
        fs::remove_file(store.path()).unwrap();
        symlink(&outside, store.path()).unwrap();
        assert!(store.append(Entry::default()).is_err());
        assert_eq!(fs::read(&outside).unwrap(), b"untouched");
        let _ = fs::remove_dir_all(&root);
        let _ = fs::remove_file(&outside);
    }

    #[test]
    fn oversized_entry_is_rejected_before_write() {
        let root =
            std::env::temp_dir().join(format!("symbrowse-journal-limit-{}", std::process::id()));
        let store = Store::new(&root, "limit", Redactor::standard(), "fixed").unwrap();
        let error = store
            .append(Entry {
                command: "x".repeat(MAX_ENTRY_BYTES),
                ..Entry::default()
            })
            .unwrap_err();
        assert_eq!(error.kind(), std::io::ErrorKind::InvalidData);
        assert!(!store.path().exists());
        let _ = fs::remove_dir_all(root);
    }
}
