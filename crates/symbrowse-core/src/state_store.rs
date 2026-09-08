#![deny(unsafe_code)]

//! Atomic filesystem operations for named browser states.

use std::{
    fmt, fs,
    fs::OpenOptions,
    io::Write,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use serde::Serialize;
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};
use zeroize::Zeroize;

use crate::state::{
    SCHEMA_VERSION, State, StateError, StateHeader, decode, encode_current, read_header,
    validate_name,
};

const DEFAULT_EXPIRE_DAYS: i64 = 30;
static NEXT_TEMP: AtomicU64 = AtomicU64::new(1);

#[derive(Clone)]
pub struct KeyMaterial {
    key: [u8; 32],
    source: String,
}

impl Drop for KeyMaterial {
    fn drop(&mut self) {
        self.key.zeroize();
        self.source.zeroize();
    }
}

impl KeyMaterial {
    pub fn new(key: [u8; 32], source: impl Into<String>) -> Result<Self, StateError> {
        let source = source.into();
        if source.is_empty() || source == "none" {
            return Err(StateError::InvalidKeySource);
        }
        Ok(Self { key, source })
    }

    #[must_use]
    pub fn source(&self) -> &str {
        &self.source
    }

    pub(crate) fn matches(&self, expected: &[u8; 32]) -> bool {
        self.key == *expected
    }
}

pub struct Store {
    dir: PathBuf,
    expire_in: Duration,
    key: Option<KeyMaterial>,
}

#[derive(Debug)]
pub enum StoreError {
    State(StateError),
    Io(std::io::Error),
    NotFound(String),
    UnsafeFileType(PathBuf),
    InvalidTime(String),
}

impl fmt::Display for StoreError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::State(error) => error.fmt(formatter),
            Self::Io(error) => error.fmt(formatter),
            Self::NotFound(name) => write!(formatter, "state {name:?} not found"),
            Self::UnsafeFileType(path) => {
                write!(
                    formatter,
                    "state path is not a regular file: {}",
                    path.display()
                )
            }
            Self::InvalidTime(value) => write!(formatter, "invalid state timestamp {value:?}"),
        }
    }
}

impl std::error::Error for StoreError {}

impl From<StateError> for StoreError {
    fn from(value: StateError) -> Self {
        Self::State(value)
    }
}

impl From<std::io::Error> for StoreError {
    fn from(value: std::io::Error) -> Self {
        Self::Io(value)
    }
}

#[derive(Debug, Eq, PartialEq, Serialize)]
pub struct Metadata {
    pub name: String,
    pub schema_version: u32,
    pub saved_at: String,
    pub expires_at: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub key_source: String,
    #[serde(serialize_with = "serialize_go_slice")]
    pub origins: Vec<OriginMetadata>,
}

#[derive(Debug, Eq, PartialEq, Serialize)]
pub struct OriginMetadata {
    pub origin: String,
    pub cookie_count: usize,
    pub local_storage_keys: usize,
    pub session_storage_keys: usize,
}

impl Store {
    pub fn new(
        dir: impl Into<PathBuf>,
        expire_in: Duration,
        key: Option<KeyMaterial>,
    ) -> Result<Self, StoreError> {
        let dir = dir.into();
        if dir.as_os_str().is_empty() {
            return Err(StateError::InvalidName("state directory is required").into());
        }
        if let Ok(metadata) = fs::symlink_metadata(&dir)
            && (!metadata.is_dir() || metadata.file_type().is_symlink())
        {
            return Err(StoreError::UnsafeFileType(dir));
        }
        fs::create_dir_all(&dir)?;
        secure_directory(&dir)?;
        Ok(Self {
            dir,
            expire_in: if expire_in.is_positive() {
                expire_in
            } else {
                Duration::days(DEFAULT_EXPIRE_DAYS)
            },
            key,
        })
    }

    #[must_use]
    pub fn dir(&self) -> &Path {
        &self.dir
    }

    pub fn save_at(&self, state: &mut State, now: OffsetDateTime) -> Result<(), StoreError> {
        validate_name(&state.name)?;
        if self.key.is_none() && !matches!(state.key_source.as_str(), "" | "none") {
            return Err(StoreError::State(StateError::KeyRequired));
        }
        if state.schema_version < SCHEMA_VERSION {
            state.schema_version = SCHEMA_VERSION;
        }
        state.saved_at = format_time(now)?;
        state.expires_at = format_time(now + self.expire_in)?;
        match &self.key {
            Some(key) => state.key_source.clone_from(&key.source),
            None => state.key_source = "none".to_owned(),
        }
        let raw = encode_current(state, self.key.as_ref().map(|key| key.key.as_slice()))?;
        atomic_write(&self.path(&state.name), &raw)?;
        Ok(())
    }

    pub fn load(&self, name: &str) -> Result<State, StoreError> {
        validate_name(name)?;
        let path = self.path(name);
        let raw = read_regular(&path).map_err(|error| {
            if error.kind() == std::io::ErrorKind::NotFound {
                StoreError::NotFound(name.to_owned())
            } else {
                StoreError::Io(error)
            }
        })?;
        let mut state = decode(&raw, self.key.as_ref().map(|key| key.key.as_slice()))?;
        state.name = name.to_owned();
        Ok(state)
    }

    pub fn list(&self) -> Result<Vec<String>, StoreError> {
        let mut names = Vec::new();
        for entry in fs::read_dir(&self.dir)? {
            let entry = entry?;
            if !entry.file_type()?.is_file() {
                continue;
            }
            let name = entry.file_name();
            let name = name.to_string_lossy();
            if let Some(name) = name.strip_suffix(".json") {
                names.push(name.to_owned());
            }
        }
        names.sort();
        Ok(names)
    }

    pub fn remove(&self, name: &str) -> Result<(), StoreError> {
        validate_name(name)?;
        let path = self.path(name);
        match fs::symlink_metadata(&path) {
            Ok(metadata) if metadata.is_file() && !metadata.file_type().is_symlink() => {
                fs::remove_file(path)?;
                Ok(())
            }
            Ok(_) => Err(StoreError::UnsafeFileType(path)),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(error.into()),
        }
    }

    pub fn metadata(&self, name: &str) -> Result<Metadata, StoreError> {
        Ok(metadata_for(&self.load(name)?))
    }

    pub fn expired_at(&self, now: OffsetDateTime) -> Result<Vec<String>, StoreError> {
        let mut expired = Vec::new();
        for name in self.list()? {
            let header = self.read_header(&name)?;
            let Ok(expires) = parse_time(&header.expires_at) else {
                continue;
            };
            if expires < now {
                expired.push(name);
            }
        }
        Ok(expired)
    }

    pub fn clean_at(&self, now: OffsetDateTime) -> Result<Vec<String>, StoreError> {
        let expired = self.expired_at(now)?;
        for name in &expired {
            self.remove(name)?;
        }
        Ok(expired)
    }

    pub fn clean_older_than_at(
        &self,
        age: Duration,
        now: OffsetDateTime,
    ) -> Result<Vec<String>, StoreError> {
        if !age.is_positive() {
            return Err(StateError::InvalidName("age must be positive").into());
        }
        let cutoff = now - age;
        let mut removed = Vec::new();
        for name in self.list()? {
            let header = self.read_header(&name)?;
            let Ok(saved) = parse_time(&header.saved_at) else {
                continue;
            };
            if saved < cutoff {
                self.remove(&name)?;
                removed.push(name);
            }
        }
        Ok(removed)
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.join(format!("{name}.json"))
    }

    fn read_header(&self, name: &str) -> Result<StateHeader, StoreError> {
        let raw = read_regular(&self.path(name))?;
        Ok(read_header(
            &raw,
            self.key.as_ref().map(|key| key.key.as_slice()),
        )?)
    }
}

#[must_use]
pub fn metadata_for(state: &State) -> Metadata {
    Metadata {
        name: state.name.clone(),
        schema_version: state.schema_version,
        saved_at: state.saved_at.clone(),
        expires_at: state.expires_at.clone(),
        key_source: state.key_source.clone(),
        origins: state
            .origins
            .iter()
            .map(|(origin, entry)| OriginMetadata {
                origin: origin.clone(),
                cookie_count: entry.cookies.len(),
                local_storage_keys: entry.local_storage.len(),
                session_storage_keys: entry.session_storage.len(),
            })
            .collect(),
    }
}

fn serialize_go_slice<S>(values: &[OriginMetadata], serializer: S) -> Result<S::Ok, S::Error>
where
    S: serde::Serializer,
{
    if values.is_empty() {
        serializer.serialize_none()
    } else {
        values.serialize(serializer)
    }
}

fn format_time(value: OffsetDateTime) -> Result<String, StoreError> {
    value
        .format(&Rfc3339)
        .map_err(|_| StoreError::InvalidTime(value.to_string()))
}

fn parse_time(value: &str) -> Result<OffsetDateTime, StoreError> {
    OffsetDateTime::parse(value, &Rfc3339).map_err(|_| StoreError::InvalidTime(value.to_owned()))
}

#[cfg(unix)]
fn read_regular(path: &Path) -> std::io::Result<Vec<u8>> {
    use std::io::Read;

    use rustix::fs::{Mode, OFlags, open};

    let descriptor = open(
        path,
        OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW,
        Mode::empty(),
    )?;
    let mut file = fs::File::from(descriptor);
    if !file.metadata()?.is_file() {
        return Err(std::io::Error::other("state path is not a regular file"));
    }
    let mut data = Vec::new();
    file.read_to_end(&mut data)?;
    Ok(data)
}

#[cfg(not(unix))]
fn read_regular(path: &Path) -> std::io::Result<Vec<u8>> {
    let metadata = fs::symlink_metadata(path)?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(std::io::Error::other("state path is not a regular file"));
    }
    fs::read(path)
}

fn atomic_write(path: &Path, data: &[u8]) -> std::io::Result<()> {
    if let Ok(metadata) = fs::symlink_metadata(path)
        && (!metadata.is_file() || metadata.file_type().is_symlink())
    {
        return Err(std::io::Error::other("state target is not a regular file"));
    }
    let parent = path
        .parent()
        .ok_or_else(|| std::io::Error::other("state path has no parent"))?;
    let (temporary, mut file) = (0..128)
        .find_map(|_| {
            let id = NEXT_TEMP.fetch_add(1, Ordering::Relaxed);
            let temporary = parent.join(format!(".state-{}-{id}.tmp", std::process::id()));
            let mut options = OpenOptions::new();
            options.write(true).create_new(true);
            secure_file_options(&mut options);
            match options.open(&temporary) {
                Ok(file) => Some(Ok((temporary, file))),
                Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => None,
                Err(error) => Some(Err(error)),
            }
        })
        .unwrap_or_else(|| {
            Err(std::io::Error::new(
                std::io::ErrorKind::AlreadyExists,
                "could not allocate a unique state temporary file",
            ))
        })?;
    let result = (|| {
        file.write_all(data)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temporary, path)?;
        sync_directory(parent)?;
        Ok(())
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

#[cfg(unix)]
fn secure_file_options(options: &mut OpenOptions) {
    use std::os::unix::fs::OpenOptionsExt;
    options.mode(0o600);
}

#[cfg(not(unix))]
fn secure_file_options(_options: &mut OpenOptions) {}

#[cfg(unix)]
fn secure_directory(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
}

#[cfg(not(unix))]
fn secure_directory(_path: &Path) -> std::io::Result<()> {
    Ok(())
}

#[cfg(unix)]
fn sync_directory(path: &Path) -> std::io::Result<()> {
    fs::File::open(path)?.sync_all()
}

#[cfg(not(unix))]
fn sync_directory(_path: &Path) -> std::io::Result<()> {
    Ok(())
}
