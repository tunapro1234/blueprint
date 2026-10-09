//! File helpers shared by every crate (new module; no single Go origin).
//!
//! Gathers the write and lock patterns the Go code repeats per package:
//!
//! * [`create_temp`]: Go `os.CreateTemp` naming (`prefix*suffix`, the last `*`
//!   becomes a random decimal number; mode 0600, `O_EXCL`).
//! * [`atomic_write`]: temp file in the target directory, optional chmod,
//!   write, fsync, rename, optional directory fsync.
//! * [`publish_link`]: `link(2)` publish that fails with
//!   [`io::ErrorKind::AlreadyExists`] instead of replacing the target.
//! * [`FileLock`]: `flock(2)` RAII guards (exclusive/shared, blocking,
//!   non-blocking, poll with timeout). Never fcntl: OFD/POSIX locks do not
//!   exclude Go's `syscall.Flock` holders.

use std::fs::{File, OpenOptions};
use std::io::{self, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use rustix::fs::FlockOperation;

static RAND_STATE: AtomicU64 = AtomicU64::new(0);

/// A pseudo-random u32 like Go's `fastrand` (only used for temp names).
fn next_random() -> u32 {
    let mut seed = RAND_STATE.load(Ordering::Relaxed);
    if seed == 0 {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos() as u64)
            .unwrap_or(1);
        seed = nanos ^ (u64::from(std::process::id()) << 32) | 1;
    }
    // xorshift64*
    let mut x = seed;
    x ^= x >> 12;
    x ^= x << 25;
    x ^= x >> 27;
    RAND_STATE.store(x, Ordering::Relaxed);
    (x.wrapping_mul(0x2545_F491_4F6C_DD1D) >> 32) as u32
}

/// Go `os.CreateTemp(dir, pattern)`: creates a new file whose name is
/// `pattern` with its last `*` replaced by a random number (appended when
/// there is no `*`). Returns the open file and its path.
pub fn create_temp(dir: &Path, pattern: &str) -> io::Result<(File, PathBuf)> {
    if pattern.contains('/') {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("pattern contains path separator: {pattern:?}"),
        ));
    }
    let (prefix, suffix) = match pattern.rfind('*') {
        Some(i) => (&pattern[..i], &pattern[i + 1..]),
        None => (pattern, ""),
    };
    let mut attempts = 0;
    loop {
        let name = format!("{prefix}{}{suffix}", next_random());
        let path = dir.join(name);
        match OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
        {
            Ok(file) => return Ok((file, path)),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists && attempts < 10_000 => {
                attempts += 1;
            }
            Err(err) => return Err(err),
        }
    }
}

/// How [`atomic_write`] creates and publishes the file.
#[derive(Debug, Clone)]
pub struct AtomicOptions {
    /// `os.CreateTemp` pattern for the temp file in the target directory.
    pub temp_pattern: String,
    /// Permission bits applied to the temp file before writing.
    pub mode: Option<u32>,
    /// Use the existing target's permission bits when it exists (overrides
    /// `mode` in that case).
    pub preserve_mode: bool,
    /// fsync the parent directory after the rename.
    pub sync_dir: bool,
}

impl Default for AtomicOptions {
    fn default() -> Self {
        AtomicOptions {
            temp_pattern: ".tmp-*".to_string(),
            mode: None,
            preserve_mode: false,
            sync_dir: true,
        }
    }
}

/// Atomically replaces `path` with `data` (temp + fsync + rename). The temp
/// file is removed on any failure.
pub fn atomic_write(path: &Path, data: &[u8], options: &AtomicOptions) -> io::Result<()> {
    let dir = parent_dir(path);
    let mut mode = options.mode;
    if options.preserve_mode {
        match std::fs::metadata(path) {
            Ok(meta) => mode = Some(meta.permissions().mode() & 0o7777),
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(err),
        }
    }
    let (mut file, tmp_path) = create_temp(&dir, &options.temp_pattern)?;
    let guard = RemoveOnDrop(Some(tmp_path.clone()));
    if let Some(mode) = mode {
        file.set_permissions(std::fs::Permissions::from_mode(mode))?;
    }
    file.write_all(data)?;
    file.sync_all()?;
    drop(file);
    std::fs::rename(&tmp_path, path)?;
    guard.disarm();
    if options.sync_dir {
        sync_dir(&dir)?;
    }
    Ok(())
}

/// Publishes `tmp` at `dest` with `link(2)` and removes `tmp`. Fails with
/// [`io::ErrorKind::AlreadyExists`] when `dest` exists (never replaces).
pub fn publish_link(tmp: &Path, dest: &Path) -> io::Result<()> {
    std::fs::hard_link(tmp, dest)?;
    let _ = std::fs::remove_file(tmp);
    Ok(())
}

/// Writes `data` to a new temp file next to `dest`, fsyncs it, and
/// publishes it with [`publish_link`] and a directory fsync.
pub fn write_new(dest: &Path, data: &[u8], temp_pattern: &str, mode: Option<u32>) -> io::Result<()> {
    let dir = parent_dir(dest);
    let (mut file, tmp_path) = create_temp(&dir, temp_pattern)?;
    let guard = RemoveOnDrop(Some(tmp_path.clone()));
    if let Some(mode) = mode {
        file.set_permissions(std::fs::Permissions::from_mode(mode))?;
    }
    file.write_all(data)?;
    file.sync_all()?;
    drop(file);
    std::fs::hard_link(&tmp_path, dest)?;
    drop(guard);
    sync_dir(&dir)
}

/// fsyncs a directory so a rename/link inside it is durable.
pub fn sync_dir(dir: &Path) -> io::Result<()> {
    File::open(dir)?.sync_all()
}

fn parent_dir(path: &Path) -> PathBuf {
    match path.parent() {
        Some(p) if !p.as_os_str().is_empty() => p.to_path_buf(),
        _ => PathBuf::from("."),
    }
}

struct RemoveOnDrop(Option<PathBuf>);

impl RemoveOnDrop {
    fn disarm(mut self) {
        self.0 = None;
    }
}

impl Drop for RemoveOnDrop {
    fn drop(&mut self) {
        if let Some(path) = self.0.take() {
            let _ = std::fs::remove_file(path);
        }
    }
}

/// Lock mode for [`FileLock`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LockKind {
    /// `LOCK_EX`.
    Exclusive,
    /// `LOCK_SH`.
    Shared,
}

/// A held `flock(2)` lock; released (`LOCK_UN`) on drop.
#[derive(Debug)]
pub struct FileLock {
    file: Option<File>,
}

fn lock_op(kind: LockKind, nonblocking: bool) -> FlockOperation {
    match (kind, nonblocking) {
        (LockKind::Exclusive, false) => FlockOperation::LockExclusive,
        (LockKind::Exclusive, true) => FlockOperation::NonBlockingLockExclusive,
        (LockKind::Shared, false) => FlockOperation::LockShared,
        (LockKind::Shared, true) => FlockOperation::NonBlockingLockShared,
    }
}

/// Opens (creating with `mode`) a lock file like Go's
/// `os.OpenFile(path, O_CREATE|O_RDWR, mode)`.
pub fn open_lock_file(path: &Path, mode: u32) -> io::Result<File> {
    OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .mode(mode)
        .open(path)
}

impl FileLock {
    /// Blocks until `file` is locked.
    pub fn acquire(file: File, kind: LockKind) -> io::Result<FileLock> {
        loop {
            match rustix::fs::flock(&file, lock_op(kind, false)) {
                Ok(()) => return Ok(FileLock { file: Some(file) }),
                Err(rustix::io::Errno::INTR) => continue,
                Err(err) => return Err(err.into()),
            }
        }
    }

    /// Non-blocking attempt: `Ok(None)` when another holder conflicts.
    pub fn try_acquire(file: File, kind: LockKind) -> io::Result<Option<FileLock>> {
        loop {
            match rustix::fs::flock(&file, lock_op(kind, true)) {
                Ok(()) => return Ok(Some(FileLock { file: Some(file) })),
                Err(rustix::io::Errno::INTR) => continue,
                Err(rustix::io::Errno::WOULDBLOCK) => return Ok(None),
                Err(err) => return Err(err.into()),
            }
        }
    }

    /// Retries a non-blocking lock every `poll` until `timeout` elapses;
    /// `Ok(None)` on timeout.
    pub fn acquire_timeout(
        file: File,
        kind: LockKind,
        timeout: Duration,
        poll: Duration,
    ) -> io::Result<Option<FileLock>> {
        let deadline = Instant::now() + timeout;
        loop {
            match rustix::fs::flock(&file, lock_op(kind, true)) {
                Ok(()) => return Ok(Some(FileLock { file: Some(file) })),
                Err(rustix::io::Errno::INTR) => continue,
                Err(rustix::io::Errno::WOULDBLOCK) => {
                    let now = Instant::now();
                    if now >= deadline {
                        return Ok(None);
                    }
                    std::thread::sleep(poll.min(deadline - now));
                }
                Err(err) => return Err(err.into()),
            }
        }
    }

    /// Opens `path` (mode 0600 when created) and blocks until locked.
    pub fn lock_path(path: &Path, kind: LockKind) -> io::Result<FileLock> {
        Self::acquire(open_lock_file(path, 0o600)?, kind)
    }

    /// Opens `path` (mode 0600 when created) and tries once.
    pub fn try_lock_path(path: &Path, kind: LockKind) -> io::Result<Option<FileLock>> {
        Self::try_acquire(open_lock_file(path, 0o600)?, kind)
    }

    /// The locked file.
    pub fn file(&self) -> &File {
        self.file.as_ref().expect("lock file present until drop")
    }

    /// Releases the lock and returns the file.
    pub fn unlock(mut self) -> io::Result<File> {
        let file = self.file.take().expect("lock file present until drop");
        rustix::fs::flock(&file, FlockOperation::Unlock)?;
        Ok(file)
    }
}

impl Drop for FileLock {
    fn drop(&mut self) {
        if let Some(file) = &self.file {
            let _ = rustix::fs::flock(file, FlockOperation::Unlock);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn create_temp_naming() {
        let dir = tempfile::tempdir().unwrap();
        let (_f, path) = create_temp(dir.path(), ".config-*.tmp").unwrap();
        let name = path.file_name().unwrap().to_str().unwrap();
        assert!(name.starts_with(".config-") && name.ends_with(".tmp"), "{name}");
        let digits = &name[".config-".len()..name.len() - 4];
        assert!(!digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()), "{name}");
        let meta = std::fs::metadata(&path).unwrap();
        assert_eq!(meta.permissions().mode() & 0o777, 0o600);
        let (_f, path) = create_temp(dir.path(), "plain").unwrap();
        assert!(path.file_name().unwrap().to_str().unwrap().starts_with("plain"));
        assert!(create_temp(dir.path(), "a/b*").is_err());
    }

    #[test]
    fn atomic_write_replaces_and_preserves_mode() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.json");
        atomic_write(&path, b"one", &AtomicOptions::default()).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"one");
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o640)).unwrap();
        let opts = AtomicOptions {
            preserve_mode: true,
            mode: Some(0o600),
            ..AtomicOptions::default()
        };
        atomic_write(&path, b"two", &opts).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"two");
        assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o640);
        let names: Vec<_> = std::fs::read_dir(dir.path()).unwrap().map(|e| e.unwrap().file_name()).collect();
        assert_eq!(names.len(), 1, "temp file left behind: {names:?}");
    }

    #[test]
    fn publish_link_reports_eexist() {
        let dir = tempfile::tempdir().unwrap();
        let dest = dir.path().join("rec.json");
        write_new(&dest, b"a", ".rec-*", Some(0o644)).unwrap();
        let err = write_new(&dest, b"b", ".rec-*", None).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::AlreadyExists);
        assert_eq!(std::fs::read(&dest).unwrap(), b"a");
        let names: Vec<_> = std::fs::read_dir(dir.path()).unwrap().collect();
        assert_eq!(names.len(), 1);
        let tmp = dir.path().join("t");
        std::fs::write(&tmp, b"c").unwrap();
        assert_eq!(publish_link(&tmp, &dest).unwrap_err().kind(), io::ErrorKind::AlreadyExists);
    }

    #[test]
    fn flock_exclusive_and_shared() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("x.lock");
        let held = FileLock::lock_path(&path, LockKind::Exclusive).unwrap();
        assert!(FileLock::try_lock_path(&path, LockKind::Shared).unwrap().is_none());
        assert!(FileLock::try_lock_path(&path, LockKind::Exclusive).unwrap().is_none());
        let timed = FileLock::acquire_timeout(
            open_lock_file(&path, 0o600).unwrap(),
            LockKind::Exclusive,
            Duration::from_millis(30),
            Duration::from_millis(5),
        )
        .unwrap();
        assert!(timed.is_none());
        drop(held);
        let a = FileLock::try_lock_path(&path, LockKind::Shared).unwrap().unwrap();
        let b = FileLock::try_lock_path(&path, LockKind::Shared).unwrap().unwrap();
        assert!(FileLock::try_lock_path(&path, LockKind::Exclusive).unwrap().is_none());
        drop(a);
        let file = b.unlock().unwrap();
        drop(file);
        let c = FileLock::try_lock_path(&path, LockKind::Exclusive).unwrap();
        assert!(c.is_some());
    }

    #[test]
    fn flock_waits_for_release() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("w.lock");
        let held = FileLock::lock_path(&path, LockKind::Exclusive).unwrap();
        let p2 = path.clone();
        let waiter = std::thread::spawn(move || {
            FileLock::acquire_timeout(
                open_lock_file(&p2, 0o600).unwrap(),
                LockKind::Exclusive,
                Duration::from_secs(5),
                Duration::from_millis(5),
            )
            .unwrap()
            .is_some()
        });
        std::thread::sleep(Duration::from_millis(30));
        drop(held);
        assert!(waiter.join().unwrap());
    }
}
