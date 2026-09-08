//! Protocol-neutral upload/download filesystem guards.
//!
//! The Go Chrome engine remains the behavior oracle for this module.  In
//! particular, download collision handling is intentionally not invented here:
//! the current Go implementation records Chrome's suggested filename but
//! checksums the GUID-named file and does not reserve or rename collisions.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    error::Error,
    fmt, fs, io,
    path::{Component, Path, PathBuf},
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub const MAX_DOWNLOAD_EVENTS: usize = 500;

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct UploadRequest {
    pub selector: String,
    pub files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub allowed_dirs: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct UploadResult {
    pub uploaded: Vec<String>,
}

#[derive(Debug)]
pub enum UploadError {
    EmptySelector,
    NoFiles,
    EmptyPath,
    Traversal(String),
    NoAllowedDirectories,
    Resolve { path: String, source: io::Error },
    Missing(String),
    Inspect { path: String, source: io::Error },
    NotRegular(String),
    Outside(String),
}

impl fmt::Display for UploadError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::EmptySelector => formatter.write_str("upload requires a selector"),
            Self::NoFiles => formatter.write_str("upload requires at least one file"),
            Self::EmptyPath => formatter.write_str("upload path is empty"),
            Self::Traversal(path) => {
                write!(
                    formatter,
                    "upload path rejected: traversal segment in {path:?}"
                )
            }
            Self::NoAllowedDirectories => formatter
                .write_str("upload path rejected: no allowed upload directory is configured"),
            Self::Resolve { source, .. } => write!(formatter, "resolve upload path: {source}"),
            Self::Missing(path) => write!(formatter, "upload file does not exist: {path}"),
            Self::Inspect { source, .. } => write!(formatter, "inspect upload file: {source}"),
            Self::NotRegular(path) => {
                write!(formatter, "upload path is not a regular file: {path}")
            }
            Self::Outside(path) => {
                write!(formatter, "upload path outside allowed directories: {path}")
            }
        }
    }
}

impl Error for UploadError {
    fn source(&self) -> Option<&(dyn Error + 'static)> {
        match self {
            Self::Resolve { source, .. } | Self::Inspect { source, .. } => Some(source),
            _ => None,
        }
    }
}

/// Validate and canonicalize one upload path using the Go engine's ordering:
/// reject raw traversal first, then resolve the file and compare its resolved
/// target with each resolved allowed root. A root equal to the file is accepted
/// because that is what `filepath.Rel` in the oracle does.
pub fn guard_upload_path(path: &str, allowed_dirs: &[&str]) -> Result<PathBuf, UploadError> {
    if path.is_empty() {
        return Err(UploadError::EmptyPath);
    }
    if has_traversal(path) {
        return Err(UploadError::Traversal(path.to_owned()));
    }
    if allowed_dirs.is_empty() {
        return Err(UploadError::NoAllowedDirectories);
    }

    let absolute = absolute_path(path).map_err(|source| UploadError::Resolve {
        path: path.to_owned(),
        source,
    })?;
    let resolved = canonicalize_or_absolute(&absolute);
    let metadata = fs::metadata(&resolved).map_err(|source| {
        if source.kind() == io::ErrorKind::NotFound {
            UploadError::Missing(path.to_owned())
        } else {
            UploadError::Inspect {
                path: path.to_owned(),
                source,
            }
        }
    })?;
    if !metadata.is_file() {
        return Err(UploadError::NotRegular(path.to_owned()));
    }

    for allowed in allowed_dirs {
        let root_absolute = match absolute_path(allowed) {
            Ok(value) => value,
            Err(_) => continue,
        };
        let root = canonicalize_or_absolute(&root_absolute);
        if path_within(&root, &resolved) {
            return Ok(resolved);
        }
    }
    Err(UploadError::Outside(path.to_owned()))
}

/// Validate all files before a browser adapter sends any of them to Chrome.
pub fn guard_upload_request(request: &UploadRequest) -> Result<UploadResult, UploadError> {
    if request.selector.trim().is_empty() {
        return Err(UploadError::EmptySelector);
    }
    if request.files.is_empty() {
        return Err(UploadError::NoFiles);
    }
    let allowed: Vec<&str> = request.allowed_dirs.iter().map(String::as_str).collect();
    let mut uploaded = Vec::with_capacity(request.files.len());
    for file in &request.files {
        uploaded.push(path_to_string(guard_upload_path(file, &allowed)?));
    }
    Ok(UploadResult { uploaded })
}

fn has_traversal(path: &str) -> bool {
    Path::new(path)
        .components()
        .any(|component| component == Component::ParentDir)
}

fn absolute_path(path: &str) -> io::Result<PathBuf> {
    let path = Path::new(path);
    let joined = if path.is_absolute() {
        path.to_owned()
    } else {
        std::env::current_dir()?.join(path)
    };
    let mut clean = PathBuf::new();
    for component in joined.components() {
        match component {
            Component::Prefix(_) | Component::RootDir => clean.push(component.as_os_str()),
            Component::CurDir => {}
            Component::ParentDir => {
                if clean.file_name().is_some() {
                    clean.pop();
                }
            }
            Component::Normal(value) => clean.push(value),
        }
    }
    Ok(clean)
}

fn canonicalize_or_absolute(path: &Path) -> PathBuf {
    fs::canonicalize(path)
        .map(strip_verbatim_prefix)
        .unwrap_or_else(|_| path.to_owned())
}

fn strip_verbatim_prefix(path: PathBuf) -> PathBuf {
    let value = path.to_string_lossy();
    if let Some(rest) = value.strip_prefix(r"\\?\UNC\") {
        return PathBuf::from(format!(r"\\{rest}"));
    }
    if let Some(rest) = value.strip_prefix(r"\\?\") {
        return PathBuf::from(rest);
    }
    path
}

fn path_within(root: &Path, path: &Path) -> bool {
    #[cfg(windows)]
    {
        let mut root_components = root.components();
        let mut path_components = path.components();
        loop {
            match (root_components.next(), path_components.next()) {
                (Some(root), Some(path)) if component_equal(root, path) => {}
                (None, _) => return true,
                _ => return false,
            }
        }
    }
    #[cfg(not(windows))]
    {
        path.starts_with(root)
    }
}

#[cfg(windows)]
fn component_equal(left: Component<'_>, right: Component<'_>) -> bool {
    left.as_os_str()
        .to_string_lossy()
        .eq_ignore_ascii_case(&right.as_os_str().to_string_lossy())
}

fn path_to_string(path: PathBuf) -> String {
    path.to_string_lossy().into_owned()
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct DownloadBehavior {
    pub behavior: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub download_path: String,
    pub events_enabled: bool,
}

#[derive(Debug)]
pub enum DownloadError {
    Resolve {
        directory: String,
        source: io::Error,
    },
    Create {
        directory: String,
        source: io::Error,
    },
}

impl fmt::Display for DownloadError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Resolve { source, .. } => {
                write!(formatter, "resolve download directory: {source}")
            }
            Self::Create { source, .. } => write!(formatter, "create download directory: {source}"),
        }
    }
}

impl Error for DownloadError {
    fn source(&self) -> Option<&(dyn Error + 'static)> {
        match self {
            Self::Resolve { source, .. } | Self::Create { source, .. } => Some(source),
        }
    }
}

#[cfg(unix)]
fn missing_directories(path: &Path) -> Vec<PathBuf> {
    let mut current = PathBuf::new();
    let mut missing = Vec::new();
    for component in path.components() {
        current.push(component.as_os_str());
        if fs::symlink_metadata(&current).is_err() {
            missing.push(current.clone());
        }
    }
    missing
}

/// Build the Browser.setDownloadBehavior payload and create an allowed
/// directory. Empty (or whitespace-only) input is the Go deny-by-default path.
pub fn download_behavior(directory: &str) -> Result<DownloadBehavior, DownloadError> {
    if directory.trim().is_empty() {
        return Ok(DownloadBehavior {
            behavior: "deny".to_owned(),
            download_path: String::new(),
            events_enabled: true,
        });
    }
    let absolute = absolute_path(directory).map_err(|source| DownloadError::Resolve {
        directory: directory.to_owned(),
        source,
    })?;
    #[cfg(unix)]
    let missing = missing_directories(&absolute);
    fs::create_dir_all(&absolute).map_err(|source| DownloadError::Create {
        directory: directory.to_owned(),
        source,
    })?;
    #[cfg(unix)]
    for path in missing {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700)).map_err(|source| {
            DownloadError::Create {
                directory: directory.to_owned(),
                source,
            }
        })?;
    }
    Ok(DownloadBehavior {
        behavior: "allow".to_owned(),
        download_path: path_to_string(absolute),
        events_enabled: true,
    })
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct DownloadEvent {
    pub guid: String,
    pub url: String,
    pub filename: String,
    pub state: String,
    pub received_bytes: i64,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub total_bytes: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sha256: String,
    pub timestamp: String,
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

/// Session-local download state. It mirrors the Go engine's bounded event
/// buffers and intentionally uses GUID-named files for checksum lookup.
#[derive(Clone, Debug, Default)]
pub struct DownloadRegistry {
    directories: BTreeMap<String, PathBuf>,
    events: BTreeMap<String, Vec<DownloadEvent>>,
}

impl DownloadRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn set_download_behavior(
        &mut self,
        session_id: &str,
        directory: &str,
    ) -> Result<DownloadBehavior, DownloadError> {
        let behavior = download_behavior(directory)?;
        self.directories.insert(
            session_id.to_owned(),
            PathBuf::from(&behavior.download_path),
        );
        Ok(behavior)
    }

    pub fn record_download_will_begin(
        &mut self,
        session_id: &str,
        guid: impl Into<String>,
        url: impl Into<String>,
        suggested_filename: impl Into<String>,
        timestamp: impl Into<String>,
    ) {
        let event = DownloadEvent {
            guid: guid.into(),
            url: url.into(),
            filename: suggested_filename.into(),
            state: "inProgress".to_owned(),
            received_bytes: 0,
            total_bytes: 0,
            sha256: String::new(),
            timestamp: timestamp.into(),
        };
        let events = self.events.entry(session_id.to_owned()).or_default();
        if events.len() >= MAX_DOWNLOAD_EVENTS {
            events.drain(..events.len() - MAX_DOWNLOAD_EVENTS + 1);
        }
        events.push(event);
    }

    pub fn record_download_will_begin_now(
        &mut self,
        session_id: &str,
        guid: impl Into<String>,
        url: impl Into<String>,
        suggested_filename: impl Into<String>,
    ) {
        self.record_download_will_begin(session_id, guid, url, suggested_filename, timestamp_now());
    }

    pub fn record_download_progress(
        &mut self,
        session_id: &str,
        guid: &str,
        state: impl Into<String>,
        received_bytes: i64,
        total_bytes: i64,
    ) {
        let Some(events) = self.events.get_mut(session_id) else {
            return;
        };
        let Some(event) = events.iter_mut().rev().find(|event| event.guid == guid) else {
            return;
        };
        event.state = state.into();
        event.received_bytes = received_bytes;
        event.total_bytes = total_bytes;
    }

    pub fn events(&self, session_id: &str) -> Vec<DownloadEvent> {
        let directory = self
            .directories
            .get(session_id)
            .cloned()
            .unwrap_or_default();
        self.events
            .get(session_id)
            .into_iter()
            .flatten()
            .map(|event| {
                let mut event = event.clone();
                if event.state == "completed" && event.sha256.is_empty() {
                    let path = directory.join(&event.guid);
                    event.sha256 = checksum_of(&path);
                }
                event
            })
            .collect()
    }
}

fn checksum_of(path: &Path) -> String {
    let Ok(raw) = fs::read(path) else {
        return String::new();
    };
    let digest = Sha256::digest(raw);
    digest.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn timestamp_now() -> String {
    OffsetDateTime::now_local()
        .or_else(|_| Ok(OffsetDateTime::now_utc()))
        .and_then(|value| value.format(&Rfc3339))
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn traversal_is_checked_before_filesystem_access() {
        let error = guard_upload_path("missing/../secret", &["missing"]).unwrap_err();
        assert!(matches!(error, UploadError::Traversal(_)));
    }

    #[test]
    fn completed_event_uses_guid_path_and_not_suggested_filename() {
        let directory =
            std::env::temp_dir().join(format!("symbrowse-files-{}", std::process::id()));
        let _ = fs::remove_dir_all(&directory);
        fs::create_dir_all(&directory).unwrap();
        fs::write(directory.join("g1"), b"payload").unwrap();
        fs::write(directory.join("file.pdf"), b"collision").unwrap();

        let mut registry = DownloadRegistry::new();
        registry
            .set_download_behavior("s", &directory.to_string_lossy())
            .unwrap();
        registry.record_download_will_begin(
            "s",
            "g1",
            "https://example.test/file",
            "file.pdf",
            "fixed",
        );
        registry.record_download_progress("s", "g1", "completed", 7, 7);
        let event = &registry.events("s")[0];
        assert_eq!(event.filename, "file.pdf");
        assert_eq!(
            event.sha256,
            "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5"
        );
        let _ = fs::remove_dir_all(directory);
    }

    #[test]
    fn request_validation_matches_go_ordering() {
        let empty_selector = UploadRequest {
            selector: " ".to_owned(),
            files: vec!["missing".to_owned()],
            allowed_dirs: vec![".".to_owned()],
        };
        assert!(matches!(
            guard_upload_request(&empty_selector),
            Err(UploadError::EmptySelector)
        ));
        let empty_files = UploadRequest {
            selector: "input[type=file]".to_owned(),
            ..UploadRequest::default()
        };
        assert!(matches!(
            guard_upload_request(&empty_files),
            Err(UploadError::NoFiles)
        ));
    }
}
