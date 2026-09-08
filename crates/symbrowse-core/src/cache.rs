#![deny(unsafe_code)]

//! Persistent truncate-and-store cache compatible with the Go layout.

use std::{
    fmt, fs,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use time::{OffsetDateTime, UtcOffset, format_description::well_known::Rfc3339};

use crate::budget;

const ZERO_TIME: &str = "0001-01-01T00:00:00Z";

#[derive(Clone, Debug)]
pub struct Cache {
    root: PathBuf,
    ttl: Duration,
    lock: Arc<Mutex<()>>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Entry {
    pub id: String,
    pub bytes: u64,
    pub created_at: String,
    pub expires_at: String,
    pub expired: bool,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Marker {
    pub truncated: bool,
    pub tokens_returned: usize,
    pub tokens_total: usize,
    pub cache_id: String,
    pub hint: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub head: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub foot: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct CacheMeta {
    id: String,
    #[serde(default = "zero_time")]
    created_at: String,
    #[serde(default = "zero_time")]
    expires_at: String,
}

#[derive(Debug)]
pub enum CacheError {
    NotFound(String),
    Expired(String),
    Io(std::io::Error),
    Json(serde_json::Error),
    Time(String),
    Missing,
}

impl fmt::Display for CacheError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotFound(id) => write!(formatter, "cache entry not found: {id}"),
            Self::Expired(id) => write!(formatter, "cache entry expired: {id}"),
            Self::Io(error) => write!(formatter, "cache I/O: {error}"),
            Self::Json(error) => write!(formatter, "cache JSON: {error}"),
            Self::Time(error) => write!(formatter, "cache time: {error}"),
            Self::Missing => {
                formatter.write_str("token budget exceeded but no output cache is configured")
            }
        }
    }
}

impl std::error::Error for CacheError {}

impl From<std::io::Error> for CacheError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error)
    }
}

impl From<serde_json::Error> for CacheError {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

impl Cache {
    #[must_use]
    pub fn new(root: impl Into<PathBuf>, ttl: Duration) -> Self {
        Self {
            root: root.into(),
            ttl,
            lock: Arc::new(Mutex::new(())),
        }
    }

    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn store(&self, data: &[u8]) -> Result<String, CacheError> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        self.store_at(data, SystemTime::now())
    }

    pub fn load(&self, id: &str) -> Result<Vec<u8>, CacheError> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        self.load_at(id, SystemTime::now())
    }

    pub fn list(&self) -> Result<Vec<Entry>, CacheError> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        self.list_at(SystemTime::now())
    }

    pub fn clear(&self) -> Result<(), CacheError> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let entries = match fs::read_dir(&self.root) {
            Ok(entries) => entries,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(error.into()),
        };
        for entry in entries {
            let path = entry?.path();
            let _ = fs::remove_file(path);
        }
        Ok(())
    }

    pub fn purge_expired(&self) -> Result<(), CacheError> {
        let _guard = self
            .lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        self.purge_expired_at(SystemTime::now())
    }

    fn store_at(&self, data: &[u8], now: SystemTime) -> Result<String, CacheError> {
        fs::create_dir_all(&self.root)?;
        secure_directory(&self.root)?;
        let id = new_id(now)?;
        let expires = if self.ttl.is_zero() {
            None
        } else {
            now.checked_add(self.ttl)
        };
        write_private(&self.content_path(&id), data)?;
        let meta = CacheMeta {
            id: id.clone(),
            created_at: format_time(now)?,
            expires_at: expires.map_or_else(|| Ok(ZERO_TIME.to_owned()), format_time)?,
        };
        write_private(&self.meta_path(&id), &serde_json::to_vec(&meta)?)?;
        Ok(id)
    }

    fn load_at(&self, id: &str, now: SystemTime) -> Result<Vec<u8>, CacheError> {
        if !valid_id(id) {
            return Err(CacheError::NotFound(id.to_owned()));
        }
        let meta = self.load_meta(id)?;
        if is_expired(&meta, now)? {
            self.remove(id);
            return Err(CacheError::Expired(id.to_owned()));
        }
        match fs::read(self.content_path(id)) {
            Ok(data) => Ok(data),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                Err(CacheError::NotFound(id.to_owned()))
            }
            Err(error) => Err(error.into()),
        }
    }

    fn list_at(&self, now: SystemTime) -> Result<Vec<Entry>, CacheError> {
        self.purge_expired_at(now)?;
        let entries = match fs::read_dir(&self.root) {
            Ok(entries) => entries,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(error) => return Err(error.into()),
        };
        let mut result = Vec::new();
        for entry in entries {
            let path = entry?.path();
            let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
                continue;
            };
            let Some(id) = name.strip_suffix(".meta.json") else {
                continue;
            };
            let Ok(meta) = self.load_meta(id) else {
                continue;
            };
            let Ok(info) = fs::metadata(self.content_path(&meta.id)) else {
                continue;
            };
            let expired = is_expired(&meta, now).unwrap_or(false);
            result.push(Entry {
                id: meta.id,
                bytes: info.len(),
                created_at: meta.created_at,
                expires_at: meta.expires_at,
                expired,
            });
        }
        result.sort_by(|left, right| left.created_at.cmp(&right.created_at));
        Ok(result)
    }

    fn purge_expired_at(&self, now: SystemTime) -> Result<(), CacheError> {
        if self.ttl.is_zero() {
            return Ok(());
        }
        let entries = match fs::read_dir(&self.root) {
            Ok(entries) => entries,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(error.into()),
        };
        for entry in entries {
            let path = entry?.path();
            let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
                continue;
            };
            let Some(id) = name.strip_suffix(".meta.json") else {
                continue;
            };
            if let Ok(meta) = self.load_meta(id)
                && is_expired(&meta, now).unwrap_or(false)
            {
                self.remove(id);
            }
        }
        Ok(())
    }

    fn load_meta(&self, id: &str) -> Result<CacheMeta, CacheError> {
        let raw = match fs::read(self.meta_path(id)) {
            Ok(raw) => raw,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                return Err(CacheError::NotFound(id.to_owned()));
            }
            Err(error) => return Err(error.into()),
        };
        let meta: CacheMeta = serde_json::from_slice(&raw)?;
        let _ = parse_time(&meta.created_at)?;
        let _ = parse_time(&meta.expires_at)?;
        Ok(meta)
    }

    fn remove(&self, id: &str) {
        let _ = fs::remove_file(self.content_path(id));
        let _ = fs::remove_file(self.meta_path(id));
    }

    fn content_path(&self, id: &str) -> PathBuf {
        self.root.join(format!("{id}.json"))
    }

    fn meta_path(&self, id: &str) -> PathBuf {
        self.root.join(format!("{id}.meta.json"))
    }
}

pub fn apply(
    cache: Option<&Cache>,
    data: &Value,
    max_tokens: usize,
    surface: &str,
) -> Result<Value, CacheError> {
    if max_tokens == 0 {
        return Ok(data.clone());
    }
    let full = serde_json::to_vec(data)?;
    let truncation = budget::truncate(&String::from_utf8_lossy(&full), max_tokens);
    if !truncation.truncated {
        return Ok(data.clone());
    }
    let cache = cache.ok_or(CacheError::Missing)?;
    let id = cache.store(&full)?;
    let hint = if surface == "mcp" {
        format!("Call the cache_get MCP tool with cache_id={id} and range=40-120")
    } else {
        format!("symbrowse cache get {id} --range 40-120")
    };
    Ok(serde_json::to_value(Marker {
        truncated: true,
        tokens_returned: truncation.tokens_returned,
        tokens_total: truncation.tokens_total,
        cache_id: id,
        hint,
        head: truncation.head,
        foot: truncation.foot,
    })?)
}

fn valid_id(id: &str) -> bool {
    id.len() == 16
        && id.starts_with("out_")
        && id[4..]
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn new_id(now: SystemTime) -> Result<String, CacheError> {
    let nanos = now
        .duration_since(UNIX_EPOCH)
        .map_err(|error| CacheError::Time(error.to_string()))?
        .as_nanos();
    let mut encoded = String::with_capacity(12);
    for index in 0..6 {
        encoded.push_str(&format!("{:02x}", ((nanos >> (8 * index)) & 0xff) as u8));
    }
    Ok(format!("out_{encoded}"))
}

fn format_time(value: SystemTime) -> Result<String, CacheError> {
    let datetime = OffsetDateTime::from(value);
    let offset = UtcOffset::local_offset_at(datetime)
        .map_err(|error| CacheError::Time(error.to_string()))?;
    datetime
        .to_offset(offset)
        .format(&Rfc3339)
        .map_err(|error| CacheError::Time(error.to_string()))
}

fn zero_time() -> String {
    ZERO_TIME.to_owned()
}

fn parse_time(value: &str) -> Result<Option<SystemTime>, CacheError> {
    if value == ZERO_TIME {
        return Ok(None);
    }
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .map_err(|error| CacheError::Time(error.to_string()))?;
    Ok(Some(parsed.into()))
}

fn is_expired(meta: &CacheMeta, now: SystemTime) -> Result<bool, CacheError> {
    Ok(parse_time(&meta.expires_at)?.is_some_and(|expires| now > expires))
}

#[cfg(unix)]
fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    use std::{fs::OpenOptions, io::Write, os::unix::fs::OpenOptionsExt};
    let mut file = OpenOptions::new()
        .create(true)
        .truncate(true)
        .write(true)
        .mode(0o600)
        .open(path)?;
    file.write_all(data)
}

#[cfg(not(unix))]
fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    fs::write(path, data)
}

#[cfg(unix)]
fn secure_directory(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
}

#[cfg(not(unix))]
fn secure_directory(_path: &Path) -> std::io::Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{Cache, CacheError, ZERO_TIME, apply, new_id};
    use serde_json::{Value, json};
    use std::{
        fs,
        time::{Duration, UNIX_EPOCH},
    };

    fn root(name: &str) -> std::path::PathBuf {
        let path =
            std::env::temp_dir().join(format!("symbrowse-cache-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&path);
        path
    }

    #[test]
    fn id_matches_go_little_endian_layout() {
        let now = UNIX_EPOCH + Duration::from_nanos(0x0102_0304_0506);
        assert_eq!(new_id(now).unwrap(), "out_060504030201");
    }

    #[test]
    fn store_load_list_clear_round_trip() {
        let root = root("round-trip");
        let cache = Cache::new(&root, Duration::from_secs(3600));
        let id = cache.store(b"full content").unwrap();
        assert_eq!(cache.load(&id).unwrap(), b"full content");
        let entries = cache.list().unwrap();
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].id, id);
        assert_eq!(entries[0].bytes, 12);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&root).unwrap().permissions().mode() & 0o777,
                0o700
            );
            assert_eq!(
                fs::metadata(root.join(format!("{id}.json")))
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
        }
        cache.clear().unwrap();
        assert!(matches!(cache.load(&id), Err(CacheError::NotFound(_))));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn apply_fails_closed_and_stores_full_json() {
        let payload = json!({"body": "x".repeat(20_000)});
        assert!(matches!(
            apply(None, &payload, 100, "cli"),
            Err(CacheError::Missing)
        ));
        let root = root("apply");
        let cache = Cache::new(&root, Duration::from_secs(3600));
        let marker = apply(Some(&cache), &payload, 100, "mcp").unwrap();
        let id = marker["cache_id"].as_str().unwrap();
        let stored: Value = serde_json::from_slice(&cache.load(id).unwrap()).unwrap();
        assert_eq!(stored, payload);
        assert!(marker["hint"].as_str().unwrap().contains("cache_get"));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn expired_entry_is_removed() {
        let root = root("expiry");
        let cache = Cache::new(&root, Duration::from_secs(10));
        let created = UNIX_EPOCH + Duration::from_secs(1_000);
        let id = cache.store_at(b"expired", created).unwrap();
        assert!(matches!(
            cache.load_at(&id, created + Duration::from_secs(11)),
            Err(CacheError::Expired(_))
        ));
        assert!(!root.join(format!("{id}.json")).exists());
        assert!(!root.join(format!("{id}.meta.json")).exists());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn missing_times_decode_as_go_zero_values() {
        let root = root("missing-times");
        fs::create_dir_all(&root).unwrap();
        let id = "out_060504030201";
        fs::write(root.join(format!("{id}.json")), b"content").unwrap();
        fs::write(
            root.join(format!("{id}.meta.json")),
            format!("{{\"id\":\"{id}\"}}"),
        )
        .unwrap();
        let cache = Cache::new(&root, Duration::ZERO);
        assert_eq!(cache.load(id).unwrap(), b"content");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn malformed_created_at_is_rejected() {
        let root = root("malformed-time");
        fs::create_dir_all(&root).unwrap();
        let id = "out_060504030201";
        fs::write(root.join(format!("{id}.json")), b"content").unwrap();
        fs::write(
            root.join(format!("{id}.meta.json")),
            format!(
                "{{\"id\":\"{id}\",\"created_at\":\"not-a-time\",\"expires_at\":\"{ZERO_TIME}\"}}"
            ),
        )
        .unwrap();
        let cache = Cache::new(&root, Duration::ZERO);
        assert!(matches!(cache.load(id), Err(CacheError::Time(_))));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn ttl_disabled_lists_expired_metadata_without_purging() {
        let root = root("list-expired");
        let created = UNIX_EPOCH + Duration::from_secs(1_000);
        let writer = Cache::new(&root, Duration::from_secs(10));
        let id = writer.store_at(b"expired", created).unwrap();
        let reader = Cache::new(&root, Duration::ZERO);
        let entries = reader.list_at(created + Duration::from_secs(11)).unwrap();
        assert_eq!(entries.len(), 1);
        assert!(entries[0].expired);
        assert!(root.join(format!("{id}.json")).exists());
        fs::remove_dir_all(root).unwrap();
    }
}
