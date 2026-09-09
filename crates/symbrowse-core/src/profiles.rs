//! Deterministic browser-profile discovery and name/path validation.

use serde::{Deserialize, Serialize};
use std::{
    fs, io,
    path::{Path, PathBuf},
};

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Profile {
    pub name: String,
    pub path: PathBuf,
    pub browser: String,
    pub is_default: bool,
}

/// Discover profiles under an explicitly supplied browser data root. The
/// platform-specific root selection belongs to the caller; this makes fixture
/// and cross-platform tests deterministic.
pub fn discover(root: &Path) -> io::Result<Vec<Profile>> {
    let mut entries = match fs::read_dir(root) {
        Ok(entries) => entries.collect::<Result<Vec<_>, _>>()?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error),
    };
    entries.sort_by_key(|entry| entry.file_name());
    let mut profiles = entries
        .into_iter()
        .filter_map(|entry| {
            let file_type = entry.file_type().ok()?;
            if !file_type.is_dir() {
                return None;
            }
            let name = entry.file_name().to_string_lossy().into_owned();
            if !is_profile_name(&name) || !entry.path().join("Preferences").is_file() {
                return None;
            }
            Some(Profile {
                path: entry.path(),
                is_default: name == "Default",
                name,
                browser: "chrome".to_owned(),
            })
        })
        .collect::<Vec<_>>();
    profiles.sort_by(|a, b| (!a.is_default, &a.name).cmp(&(!b.is_default, &b.name)));
    Ok(profiles)
}

#[must_use]
pub fn is_profile_name(name: &str) -> bool {
    if name == "Default" {
        return true;
    }
    let Some(number) = name.strip_prefix("Profile ") else {
        return false;
    };
    matches!(
        number,
        "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" | "10"
    )
}

/// Resolve either a discovered profile name or an existing directory path.
pub fn resolve(root: &Path, argument: &str) -> io::Result<(PathBuf, bool)> {
    if argument.is_empty() {
        return Ok((PathBuf::new(), false));
    }
    let candidate = Path::new(argument);
    if candidate.is_absolute() || candidate.components().count() > 1 {
        let path = if candidate.is_absolute() {
            candidate.to_owned()
        } else {
            std::env::current_dir()?.join(candidate)
        };
        if path.is_dir() {
            return Ok((path, false));
        }
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "profile directory not found",
        ));
    }
    discover(root)?
        .into_iter()
        .find(|profile| profile.name == argument)
        .map(|profile| (profile.path, true))
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "profile name not found"))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn sorts_default_and_ignores_cache() {
        let root = tempfile_dir();
        for name in ["Profile 2", "Default", "Cache"] {
            let dir = root.join(name);
            fs::create_dir_all(&dir).unwrap();
            if name != "Cache" {
                fs::write(dir.join("Preferences"), b"{}").unwrap();
            }
        }
        let got = discover(&root).unwrap();
        assert_eq!(
            got.iter().map(|p| p.name.as_str()).collect::<Vec<_>>(),
            ["Default", "Profile 2"]
        );
        assert!(!is_profile_name("Profile 11"));
        assert!(discover(&root.join("missing")).unwrap().is_empty());
    }
    fn tempfile_dir() -> PathBuf {
        let path =
            std::env::temp_dir().join(format!("symbrowse-profile-test-{}", std::process::id()));
        let _ = fs::remove_dir_all(&path);
        fs::create_dir_all(&path).unwrap();
        path
    }
}
