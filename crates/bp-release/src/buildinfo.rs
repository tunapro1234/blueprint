//! Port of internal/buildinfo/buildinfo.go (+ stat_linux.go, stat_darwin.go,
//! stat_other.go).
//!
//! Identifies an executable, not the source tree beside it.

use crate::fsutil;
use crate::gojson;
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};
use std::fmt::Write as _;
use std::io::{self, Write as _};
use std::path::{Path, PathBuf};

/// Git revision stamped by build.rs (Go `vcs.revision`); empty without git.
pub const REVISION: &str = env!("BP_BUILD_REVISION");
/// Source tree had local modifications at build time (Go `vcs.modified`).
pub const SOURCE_MODIFIED: bool = matches!(env!("BP_BUILD_MODIFIED").as_bytes(), b"true");

/// Go `buildinfo.Identity`. Field order is the JSON order.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Identity {
    /// `process_start_ticks,omitempty`
    pub start_ticks: String,
    pub pid: i64,
    /// `executable,omitempty`
    pub executable: String,
    /// `sha256,omitempty`
    pub sha256: String,
    /// `executable_stat,omitempty`
    pub executable_stat: Option<ExecutableStat>,
    /// `revision,omitempty`
    pub revision: String,
    /// `source_modified`
    pub modified: bool,
}

/// Filesystem identity used to validate a recorded hash. ctime changes on
/// in-place writes even when a writer restores mtime.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ExecutableStat {
    pub device: u64,
    pub inode: u64,
    pub size: i64,
    pub mtime_ns: i64,
    pub ctime_ns: i64,
}

impl ExecutableStat {
    fn write_json(&self, out: &mut String) {
        let _ = write!(
            out,
            r#"{{"device":{},"inode":{},"size":{},"mtime_ns":{},"ctime_ns":{}}}"#,
            self.device, self.inode, self.size, self.mtime_ns, self.ctime_ns
        );
    }

    fn from_value(value: &Value) -> Result<Option<Self>, ()> {
        let map = match value {
            Value::Null => return Ok(None),
            Value::Object(map) => map,
            _ => return Err(()),
        };
        Ok(Some(Self {
            device: gojson::u64_field(map, "device")?.unwrap_or_default(),
            inode: gojson::u64_field(map, "inode")?.unwrap_or_default(),
            size: gojson::i64_field(map, "size")?.unwrap_or_default(),
            mtime_ns: gojson::i64_field(map, "mtime_ns")?.unwrap_or_default(),
            ctime_ns: gojson::i64_field(map, "ctime_ns")?.unwrap_or_default(),
        }))
    }

    fn field(map: &Map<String, Value>, name: &str) -> Result<Option<Self>, ()> {
        match gojson::field(map, name) {
            None => Ok(None),
            Some(v) => Self::from_value(v),
        }
    }
}

impl Identity {
    /// Appends the struct's fields (without braces) in Go's order.
    fn write_fields(&self, out: &mut String) {
        let mut first = true;
        let mut key = |out: &mut String, name: &str| {
            if !first {
                out.push(',');
            }
            first = false;
            out.push('"');
            out.push_str(name);
            out.push_str("\":");
        };
        if !self.start_ticks.is_empty() {
            key(out, "process_start_ticks");
            gojson::write_string(out, &self.start_ticks);
        }
        key(out, "pid");
        let _ = write!(out, "{}", self.pid);
        if !self.executable.is_empty() {
            key(out, "executable");
            gojson::write_string(out, &self.executable);
        }
        if !self.sha256.is_empty() {
            key(out, "sha256");
            gojson::write_string(out, &self.sha256);
        }
        if let Some(stat) = &self.executable_stat {
            key(out, "executable_stat");
            stat.write_json(out);
        }
        if !self.revision.is_empty() {
            key(out, "revision");
            gojson::write_string(out, &self.revision);
        }
        key(out, "source_modified");
        out.push_str(if self.modified { "true" } else { "false" });
    }

    /// `json.Marshal(identity)` (no trailing newline).
    pub fn to_json(&self) -> String {
        let mut out = String::from("{");
        self.write_fields(&mut out);
        out.push('}');
        out
    }

    /// Decodes like Go's `json.Unmarshal` into `Identity`; `None` on any error.
    pub fn from_json(data: &[u8]) -> Option<Self> {
        let Value::Object(map) = gojson::parse(data)? else {
            return None;
        };
        Self::from_map(&map).ok()
    }

    fn from_map(map: &Map<String, Value>) -> Result<Self, ()> {
        Ok(Self {
            start_ticks: gojson::string_field(map, "process_start_ticks")?.unwrap_or_default(),
            pid: gojson::i64_field(map, "pid")?.unwrap_or_default(),
            executable: gojson::string_field(map, "executable")?.unwrap_or_default(),
            sha256: gojson::string_field(map, "sha256")?.unwrap_or_default(),
            executable_stat: ExecutableStat::field(map, "executable_stat")?,
            revision: gojson::string_field(map, "revision")?.unwrap_or_default(),
            modified: gojson::bool_field(map, "source_modified")?.unwrap_or_default(),
        })
    }
}

/// The `bp version --json` line: `{"version":…,<Identity fields>}` plus the
/// trailing newline written by Go's `json.Encoder`.
pub fn version_json(version: &str, identity: &Identity) -> String {
    let mut out = String::from("{\"version\":");
    gojson::write_string(&mut out, version);
    out.push(',');
    identity.write_fields(&mut out);
    out.push_str("}\n");
    out
}

/// Hash function seam (Go's package-level `hashExecutable`).
pub type HashFn<'a> = &'a dyn Fn(&Path) -> io::Result<String>;

/// Writes the current identity; called only when the daemon starts, never by
/// status readers.
pub fn record(path: &Path) -> io::Result<()> {
    let data = current().to_json();
    let dir = parent_dir(path);
    fsutil::mkdir_all(dir)?;
    let (mut file, name) = fsutil::create_temp(dir, ".runtime-")?;
    file.write_all(data.as_bytes())?;
    drop(file);
    std::fs::rename(&name, path)
}

/// Verification status strings returned by [`recorded`].
pub const VERIFIED: &str = "verified executable";

/// Returns the startup claim and whether the running executable agrees. A
/// stale file or invisible host PID must not masquerade as daemon verification.
pub fn recorded(path: &Path) -> (Option<Identity>, String) {
    recorded_with(path, &hash_file)
}

pub fn recorded_with(path: &Path, hash: HashFn<'_>) -> (Option<Identity>, String) {
    let Ok(data) = std::fs::read(path) else {
        return (None, "unknown: no readable daemon identity".into());
    };
    let i = match Identity::from_json(&data) {
        Some(i) if i.pid > 0 && !i.sha256.is_empty() => i,
        _ => return (None, "unknown: invalid daemon identity".into()),
    };
    if !valid_sha256(&i.sha256) {
        return (Some(i), "unverified: running executable differs".into());
    }
    if i.start_ticks.is_empty()
        || process_start(Path::new(&format!("/proc/{}/stat", i.pid))) != i.start_ticks
    {
        return (
            Some(i),
            "unverified: daemon process identity not readable or changed".into(),
        );
    }
    let exe_path = PathBuf::from(format!("/proc/{}/exe", i.pid));
    let current = executable_stat(&exe_path);
    if let (Some(current), Some(recorded)) = (&current, &i.executable_stat)
        && current == recorded
    {
        return (Some(i), VERIFIED.into());
    }
    let mut cache_path = path.as_os_str().to_owned();
    cache_path.push(".verified-hash.json");
    let cache_path = PathBuf::from(cache_path);
    if let Some(current) = &current
        && verified_daemon_hash_cache(&cache_path, &i, current)
    {
        return (Some(i), VERIFIED.into());
    }
    let Ok(sum) = hash(&exe_path) else {
        return (Some(i), "unverified: daemon PID not readable".into());
    };
    if sum != i.sha256 {
        return (Some(i), "unverified: running executable differs".into());
    }
    if let Some(current) = current {
        let mut out = String::from("{\"pid\":");
        let _ = write!(out, "{},\"process_start_ticks\":", i.pid);
        gojson::write_string(&mut out, &i.start_ticks);
        out.push_str(",\"stat\":");
        current.write_json(&mut out);
        out.push_str(",\"sha256\":");
        gojson::write_string(&mut out, &i.sha256);
        out.push('}');
        let _ = write_atomic(&cache_path, out.as_bytes());
    }
    (Some(i), VERIFIED.into())
}

/// Identity of the running process (hashes the executable every time).
pub fn current() -> Identity {
    current_with(None, &hash_file)
}

/// [`current`] with a persistent executable hash cache. Status uses this form
/// because it starts a fresh CLI process for each poll.
pub fn current_cached(cache_path: &Path) -> Identity {
    current_with(Some(cache_path), &hash_file)
}

pub fn current_with(cache_path: Option<&Path>, hash: HashFn<'_>) -> Identity {
    let mut i = Identity {
        pid: i64::from(std::process::id()),
        start_ticks: process_start(Path::new("/proc/self/stat")),
        ..Identity::default()
    };
    i.executable = executable_path().unwrap_or_default();
    let self_exe = Path::new("/proc/self/exe");
    i.executable_stat = executable_stat(self_exe);
    i.sha256 = cached_hash_with(self_exe, i.executable_stat.as_ref(), cache_path, hash)
        .unwrap_or_default();
    if i.sha256.is_empty() {
        let exe = PathBuf::from(&i.executable);
        i.executable_stat = executable_stat(&exe);
        i.sha256 = cached_hash_with(&exe, i.executable_stat.as_ref(), cache_path, hash)
            .unwrap_or_default();
    }
    add_build_revision(i)
}

fn add_build_revision(mut i: Identity) -> Identity {
    i.revision = REVISION.to_string();
    i.modified = SOURCE_MODIFIED;
    i
}

/// Go `os.Executable()`: on Linux `/proc/self/exe` without the ` (deleted)`
/// suffix the kernel appends to removed executables.
pub fn executable_path() -> Option<String> {
    let path = std::env::current_exe().ok()?;
    let path = path.to_string_lossy().into_owned();
    Some(match path.strip_suffix(" (deleted)") {
        Some(stripped) if cfg!(target_os = "linux") => stripped.to_string(),
        _ => path,
    })
}

fn verified_daemon_hash_cache(path: &Path, identity: &Identity, stat: &ExecutableStat) -> bool {
    let Ok(data) = std::fs::read(path) else {
        return false;
    };
    let Some(Value::Object(map)) = gojson::parse(&data) else {
        return false;
    };
    let decoded = (|| -> Result<_, ()> {
        Ok((
            gojson::i64_field(&map, "pid")?.unwrap_or_default(),
            gojson::string_field(&map, "process_start_ticks")?.unwrap_or_default(),
            ExecutableStat::field(&map, "stat")?.unwrap_or_default(),
            gojson::string_field(&map, "sha256")?.unwrap_or_default(),
        ))
    })();
    let Ok((pid, ticks, cached_stat, sha)) = decoded else {
        return false;
    };
    pid == identity.pid
        && ticks == identity.start_ticks
        && cached_stat == *stat
        && sha == identity.sha256
        && valid_sha256(&sha)
}

/// Go `cachedHash` with the public hash function.
pub fn cached_hash(
    path: &Path,
    stat: Option<&ExecutableStat>,
    cache_path: Option<&Path>,
) -> io::Result<String> {
    cached_hash_with(path, stat, cache_path, &hash_file)
}

pub fn cached_hash_with(
    path: &Path,
    stat: Option<&ExecutableStat>,
    cache_path: Option<&Path>,
    hash: HashFn<'_>,
) -> io::Result<String> {
    let cache_path = cache_path.filter(|p| !p.as_os_str().is_empty());
    if let (Some(stat), Some(cache_path)) = (stat, cache_path)
        && let Ok(data) = std::fs::read(cache_path)
        && let Some(Value::Object(map)) = gojson::parse(&data)
        && let (Ok(cached_stat), Ok(sha)) = (
            ExecutableStat::field(&map, "stat"),
            gojson::string_field(&map, "sha256"),
        )
    {
        let sha = sha.unwrap_or_default();
        if cached_stat.unwrap_or_default() == *stat && valid_sha256(&sha) {
            return Ok(sha);
        }
    }
    let sum = hash(path)?;
    let (Some(stat), Some(cache_path)) = (stat, cache_path) else {
        return Ok(sum);
    };
    let mut out = String::from("{\"stat\":");
    stat.write_json(&mut out);
    out.push_str(",\"sha256\":");
    gojson::write_string(&mut out, &sum);
    out.push('}');
    let _ = write_atomic(cache_path, out.as_bytes());
    Ok(sum)
}

/// Lowercase 64-digit hex.
pub fn valid_sha256(hash: &str) -> bool {
    hash.len() == 64
        && hash
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// SHA-256 of a file as lowercase hex.
pub fn hash_file(path: &Path) -> io::Result<String> {
    let mut file = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; 64 << 10];
    loop {
        let n = match io::Read::read(&mut file, &mut buf) {
            Ok(0) => break,
            Ok(n) => n,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        };
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(hasher.finalize()))
}

fn write_atomic(path: &Path, data: &[u8]) -> io::Result<()> {
    let dir = parent_dir(path);
    fsutil::mkdir_all(dir)?;
    let (mut file, tmp) = fsutil::create_temp(dir, ".executable-hash-")?;
    let result = (|| {
        file.write_all(data)?;
        file.sync_all()?;
        drop(file);
        std::fs::rename(&tmp, path)
    })();
    let _ = std::fs::remove_file(&tmp);
    result
}

fn parent_dir(path: &Path) -> &Path {
    match path.parent() {
        Some(p) if !p.as_os_str().is_empty() => p,
        _ => Path::new("."),
    }
}

/// Field 22 (start time in clock ticks) of a `/proc/<pid>/stat` file, or "".
pub fn process_start(path: &Path) -> String {
    let Ok(data) = std::fs::read(path) else {
        return String::new();
    };
    let Some(end) = data.iter().rposition(|&b| b == b')') else {
        return String::new();
    };
    let rest = String::from_utf8_lossy(&data[end + 1..]);
    rest.split_whitespace()
        .nth(19)
        .map(str::to_string)
        .unwrap_or_default()
}

/// Filesystem identity of `path` (following symlinks), `None` when unavailable.
#[cfg(any(target_os = "linux", target_os = "macos"))]
pub fn executable_stat(path: &Path) -> Option<ExecutableStat> {
    use std::os::unix::fs::MetadataExt;
    let m = std::fs::metadata(path).ok()?;
    Some(ExecutableStat {
        device: m.dev(),
        inode: m.ino(),
        size: m.size() as i64,
        mtime_ns: m
            .mtime()
            .wrapping_mul(1_000_000_000)
            .wrapping_add(m.mtime_nsec()),
        ctime_ns: m
            .ctime()
            .wrapping_mul(1_000_000_000)
            .wrapping_add(m.ctime_nsec()),
    })
}

/// No portable inode/ctime identity here, so every caller falls back to
/// hashing the executable.
#[cfg(not(any(target_os = "linux", target_os = "macos")))]
pub fn executable_stat(_path: &Path) -> Option<ExecutableStat> {
    None
}
