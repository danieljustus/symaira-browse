//! File-backed output and response caches used by the static fetch pipeline.
use std::{
    collections::BTreeMap,
    fs::{self, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use sha2::{Digest, Sha256};

static NEXT_ID: AtomicU64 = AtomicU64::new(1);

#[derive(Debug)]
pub enum CacheError {
    Io(io::Error),
    InvalidId(String),
    NotFound(String),
    Expired(String),
    Serialize(String),
}

impl std::fmt::Display for CacheError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(error) => write!(f, "cache I/O: {error}"),
            Self::InvalidId(id) => write!(f, "invalid cache id: {id}"),
            Self::NotFound(id) => write!(f, "cache entry not found: {id}"),
            Self::Expired(id) => write!(f, "cache entry expired: {id}"),
            Self::Serialize(error) => write!(f, "cache metadata: {error}"),
        }
    }
}
impl std::error::Error for CacheError {}
impl From<io::Error> for CacheError {
    fn from(error: io::Error) -> Self {
        Self::Io(error)
    }
}

#[derive(Clone, Debug, Default, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct CacheMeta {
    pub id: String,
    pub created_at: String,
    pub expires_at: Option<String>,
}

/// Output cache for full text retained by truncate-and-store.
#[derive(Clone, Debug)]
pub struct OutputCache {
    pub root: PathBuf,
    pub ttl: Option<Duration>,
}

impl OutputCache {
    #[must_use]
    pub fn new(root: impl Into<PathBuf>, ttl: Option<Duration>) -> Self {
        Self {
            root: root.into(),
            ttl,
        }
    }

    pub fn store(&self, bytes: &[u8]) -> Result<String, CacheError> {
        fs::create_dir_all(&self.root)?;
        let sequence = NEXT_ID.fetch_add(1, Ordering::Relaxed);
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let mut hasher = Sha256::new();
        hasher.update(bytes);
        hasher.update(sequence.to_le_bytes());
        hasher.update(now.to_le_bytes());
        let digest = hasher.finalize();
        let id = format!("out_{}", hex_prefix(&digest, 12));
        let body = self.root.join(format!("{id}.json"));
        let meta = self.root.join(format!("{id}.meta.json"));
        atomic_write(&body, bytes)?;
        let created = format_system_time(SystemTime::now());
        let expires_at = self.ttl.filter(|ttl| !ttl.is_zero()).map(|ttl| {
            format_system_time(
                SystemTime::now()
                    .checked_add(ttl)
                    .unwrap_or(SystemTime::now()),
            )
        });
        let metadata = CacheMeta {
            id: id.clone(),
            created_at: created,
            expires_at,
        };
        let encoded =
            serde_json::to_vec(&metadata).map_err(|e| CacheError::Serialize(e.to_string()))?;
        atomic_write(&meta, &encoded)?;
        Ok(id)
    }

    pub fn load(&self, id: &str) -> Result<Vec<u8>, CacheError> {
        validate_cache_id(id)?;
        let meta_path = self.root.join(format!("{id}.meta.json"));
        let body_path = self.root.join(format!("{id}.json"));
        let raw = fs::read(&meta_path).map_err(|error| {
            if error.kind() == io::ErrorKind::NotFound {
                CacheError::NotFound(id.into())
            } else {
                CacheError::Io(error)
            }
        })?;
        let metadata: CacheMeta = serde_json::from_slice(&raw)
            .map_err(|error| CacheError::Serialize(error.to_string()))?;
        if let Some(expires) = metadata.expires_at.as_deref()
            && parse_system_time(expires).is_some_and(|when| SystemTime::now() > when)
        {
            let _ = self.remove(id);
            return Err(CacheError::Expired(id.into()));
        }
        fs::read(body_path).map_err(|error| {
            if error.kind() == io::ErrorKind::NotFound {
                CacheError::NotFound(id.into())
            } else {
                CacheError::Io(error)
            }
        })
    }

    pub fn remove(&self, id: &str) -> Result<(), CacheError> {
        validate_cache_id(id)?;
        for suffix in [".json", ".meta.json"] {
            let path = self.root.join(format!("{id}{suffix}"));
            match fs::remove_file(path) {
                Ok(()) => {}
                Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                Err(e) => return Err(CacheError::Io(e)),
            }
        }
        Ok(())
    }

    pub fn clear(&self) -> Result<(), CacheError> {
        if !self.root.exists() {
            return Ok(());
        }
        for entry in fs::read_dir(&self.root)? {
            let path = entry?.path();
            if path.is_file() {
                fs::remove_file(path)?;
            }
        }
        Ok(())
    }
}

/// A content-addressed response cache. Keys include every rendering input.
#[derive(Clone, Debug)]
pub struct ResponseCache {
    pub root: PathBuf,
    pub ttl: Option<Duration>,
}

impl ResponseCache {
    #[must_use]
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self {
            root: root.into(),
            ttl: Some(Duration::from_secs(24 * 60 * 60)),
        }
    }

    #[must_use]
    pub fn with_ttl(mut self, ttl: Duration) -> Self {
        self.ttl = (!ttl.is_zero()).then_some(ttl);
        self
    }

    #[must_use]
    pub fn key(url: &str, profile: &str, format: &str, session: &str, content_key: &str) -> String {
        let mut hasher = Sha256::new();
        for part in ["v3", url, profile, format, session, content_key] {
            hasher.update(part.as_bytes());
            hasher.update(*b"|");
        }
        format!("{:x}", hasher.finalize())
    }

    pub fn put(&self, key: &str, body: &[u8], meta: &serde_json::Value) -> Result<(), CacheError> {
        if key.len() != 64 || !key.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(CacheError::InvalidId(key.into()));
        }
        let dir = self.root.join(&key[..2]);
        fs::create_dir_all(&dir)?;
        atomic_write(&dir.join(format!("{key}.body")), body)?;
        let encoded =
            serde_json::to_vec_pretty(meta).map_err(|e| CacheError::Serialize(e.to_string()))?;
        atomic_write(&dir.join(format!("{key}.meta.json")), &encoded)
    }

    pub fn get(&self, key: &str) -> Result<(Vec<u8>, serde_json::Value), CacheError> {
        if key.len() != 64 || !key.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(CacheError::InvalidId(key.into()));
        }
        let dir = self.root.join(&key[..2]);
        let body_path = dir.join(format!("{key}.body"));
        let meta_path = dir.join(format!("{key}.meta.json"));
        if let Some(ttl) = self.ttl {
            let modified = fs::metadata(&meta_path)?.modified()?;
            if modified
                .checked_add(ttl)
                .is_some_and(|expires| SystemTime::now() > expires)
            {
                let _ = fs::remove_file(&body_path);
                let _ = fs::remove_file(&meta_path);
                return Err(CacheError::Expired(key.into()));
            }
        }
        let body = fs::read(body_path).map_err(|e| {
            if e.kind() == io::ErrorKind::NotFound {
                CacheError::NotFound(key.into())
            } else {
                CacheError::Io(e)
            }
        })?;
        let meta = fs::read(meta_path)?;
        let meta =
            serde_json::from_slice(&meta).map_err(|e| CacheError::Serialize(e.to_string()))?;
        Ok((body, meta))
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StoreOptions {
    pub char_limit: usize,
    pub max_stored_bytes: usize,
    pub head_ratio_percent: usize,
    pub tail_ratio_percent: usize,
}
impl Default for StoreOptions {
    fn default() -> Self {
        Self {
            char_limit: 15_000,
            max_stored_bytes: 2 * 1024 * 1024,
            head_ratio_percent: 80,
            tail_ratio_percent: 20,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StoreResult {
    pub output: String,
    pub stored: bool,
    pub cache_id: Option<String>,
}

pub fn truncate_and_store(
    text: &str,
    options: StoreOptions,
    cache: Option<&OutputCache>,
) -> Result<StoreResult, CacheError> {
    let options = normalize_options(options);
    let count = text.chars().count();
    if count <= options.char_limit {
        return Ok(StoreResult {
            output: text.to_owned(),
            stored: false,
            cache_id: None,
        });
    }
    let total_window = options.char_limit;
    let head_count = total_window.saturating_mul(options.head_ratio_percent)
        / (options.head_ratio_percent + options.tail_ratio_percent);
    let tail_count = total_window.saturating_sub(head_count);
    let head: String = text.chars().take(head_count.max(1)).collect();
    let tail: String = text
        .chars()
        .rev()
        .take(tail_count.max(1))
        .collect::<String>()
        .chars()
        .rev()
        .collect();
    let stored_text = cap_utf8_bytes(text, options.max_stored_bytes);
    let id = cache
        .ok_or_else(|| {
            CacheError::Io(io::Error::new(
                io::ErrorKind::InvalidInput,
                "store directory not specified",
            ))
        })?
        .store(stored_text.as_bytes())?;
    let output = format!(
        "{}\n\n...\n\n{}\n\n--- Full text stored: cache_id={} ---",
        head, tail, id
    );
    Ok(StoreResult {
        output,
        stored: true,
        cache_id: Some(id),
    })
}

#[must_use]
pub fn cache_id_from_output(output: &str) -> Option<&str> {
    let value = output
        .split("cache_id=")
        .nth(1)?
        .split_whitespace()
        .next()?;
    (is_valid_cache_id(value)).then_some(value)
}

#[must_use]
pub fn line_range(content: &[u8], start: usize, end: usize) -> String {
    let text = String::from_utf8_lossy(content);
    let lines: Vec<_> = text.split('\n').collect();
    let first = start.max(1);
    if first > lines.len() {
        return String::new();
    }
    let last = if end < first || end == 0 {
        lines.len()
    } else {
        end.min(lines.len())
    };
    lines[first - 1..last].join("\n")
}

fn normalize_options(mut options: StoreOptions) -> StoreOptions {
    if options.char_limit == 0 {
        options.char_limit = StoreOptions::default().char_limit;
    }
    if options.max_stored_bytes == 0 {
        options.max_stored_bytes = StoreOptions::default().max_stored_bytes;
    }
    if options.head_ratio_percent == 0 || options.tail_ratio_percent == 0 {
        options.head_ratio_percent = 80;
        options.tail_ratio_percent = 20;
    }
    options
}
fn cap_utf8_bytes(text: &str, max: usize) -> String {
    text.chars()
        .scan(0, |size, ch| {
            let next = *size + ch.len_utf8();
            if next > max {
                None
            } else {
                *size = next;
                Some(ch)
            }
        })
        .collect()
}
fn atomic_write(path: &Path, bytes: &[u8]) -> Result<(), CacheError> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(parent, fs::Permissions::from_mode(0o700))?;
        }
    }
    let tmp = path.with_extension(format!(
        "tmp-{}-{}",
        std::process::id(),
        NEXT_ID.fetch_add(1, Ordering::Relaxed)
    ));
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&tmp)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    drop(file);
    if let Err(error) = fs::rename(&tmp, path) {
        let _ = fs::remove_file(&tmp);
        return Err(error.into());
    }
    #[cfg(unix)]
    if let Some(parent) = path.parent() {
        fs::File::open(parent)?.sync_all()?;
    }
    Ok(())
}
fn is_valid_cache_id(id: &str) -> bool {
    id.len() == 16 && id.starts_with("out_") && id[4..].bytes().all(|b| b.is_ascii_hexdigit())
}
fn validate_cache_id(id: &str) -> Result<(), CacheError> {
    if is_valid_cache_id(id) {
        Ok(())
    } else {
        Err(CacheError::InvalidId(id.into()))
    }
}
fn hex_prefix(bytes: &[u8], digits: usize) -> String {
    let full = bytes.iter().map(|b| format!("{b:02x}")).collect::<String>();
    full.chars().take(digits).collect()
}
fn format_system_time(time: SystemTime) -> String {
    time.duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos()
        .to_string()
}
fn parse_system_time(value: &str) -> Option<SystemTime> {
    value
        .parse::<u128>()
        .ok()
        .map(|n| UNIX_EPOCH + Duration::from_nanos(n.min(u64::MAX as u128) as u64))
}

/// Stable content-key helper for render options.
#[must_use]
pub fn content_key(
    max_chars: usize,
    include_links: bool,
    threshold: usize,
    max_island_bytes: usize,
) -> String {
    format!("mc={max_chars} il={include_links} ct={threshold} mi={max_island_bytes}")
}

/// Encode arbitrary option fields without relying on map iteration order.
#[must_use]
pub fn options_key(fields: &BTreeMap<String, String>) -> String {
    fields
        .iter()
        .map(|(k, v)| format!("{k}={v}"))
        .collect::<Vec<_>>()
        .join(" ")
}
