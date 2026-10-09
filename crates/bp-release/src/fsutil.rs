//! Small file helpers shared by buildinfo and release (Go `os.CreateTemp`,
//! `syscall.Flock`).
//!
//! TODO(dedupe): move to `bp-core::fs` once it exists; kept local so this crate
//! does not wait on the concurrent bp-core port.

use std::fs::{File, OpenOptions};
use std::io;
use std::os::unix::fs::OpenOptionsExt;
use std::path::Path;

/// Creates a temporary file in `dir` named `<prefix><random>` with mode 0600,
/// like Go's `os.CreateTemp(dir, prefix+"*")`. The file is not removed on drop.
pub fn create_temp(dir: &Path, prefix: &str) -> io::Result<(File, std::path::PathBuf)> {
    let named = tempfile::Builder::new()
        .prefix(prefix)
        .rand_bytes(10)
        .tempfile_in(dir)?;
    let (file, path) = named.keep().map_err(|e| e.error)?;
    Ok((file, path))
}

/// An exclusive `flock(2)` held until drop (closing the descriptor unlocks).
#[derive(Debug)]
pub struct FlockGuard {
    _file: File,
}

/// Opens (creating with `mode`) and exclusively flocks `path`, blocking.
pub fn lock_exclusive(path: &Path, mode: u32) -> io::Result<FlockGuard> {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .mode(mode)
        .open(path)?;
    rustix::fs::flock(&file, rustix::fs::FlockOperation::LockExclusive)?;
    Ok(FlockGuard { _file: file })
}

/// `os.MkdirAll(dir, 0755)`.
pub fn mkdir_all(dir: &Path) -> io::Result<()> {
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o755)
        .create(dir)
}

use std::os::unix::fs::DirBuilderExt;
