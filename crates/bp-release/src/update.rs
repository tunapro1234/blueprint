//! Port of the self-update pieces of cmd/bp/update.go that belong to the
//! release mechanics: npm/legacy refusals, the candidate `setup --check` run
//! and the post-install `setup` run around [`crate::release::replace`].
//!
//! The command flow itself (argument parsing, `update.json`, output) stays in
//! bp-cli.

use crate::gojson;
use crate::release::{self, BoxError, ReplaceError};
use serde_json::Value;
use std::path::{Path, PathBuf};
use std::process::Command;

/// Refusal for an npm-managed install (`bp update` without `--check`).
pub const NPM_REFUSAL: &str = "npm-managed installation: run npm install -g @tunapro/blueprint@latest; bp update --check remains available";

/// Refusal for a server (legacy) installation.
pub const LEGACY_REFUSAL: &str = "server installation: use the host release workflow to update both CLI and daemon; agents will not be restarted automatically";

/// Suffix appended to a failed download.
pub const INSTALLER_HINT: &str = "if the update keeps failing, reinstall with the signed installer: curl -fsSL https://github.com/tunapro1234/blueprint/releases/latest/download/install.sh | sh -s -- --local";

/// The npm package that ships native bp binaries.
pub const NPM_PACKAGE: &str = "@tunapro/blueprint";

/// Process seam for running the candidate and the installed binary.
pub trait CommandRunner {
    fn run(&self, path: &Path, args: &[&str]) -> Result<(), BoxError>;
}

/// Runs commands with inherited stdio, failing on a non-zero exit (Go
/// `exec.Cmd.Run` with stdout/stderr attached to the CLI's).
#[derive(Debug, Default, Clone, Copy)]
pub struct ExecRunner;

#[derive(Debug, thiserror::Error)]
#[error("{0}")]
struct ExitError(String);

impl CommandRunner for ExecRunner {
    fn run(&self, path: &Path, args: &[&str]) -> Result<(), BoxError> {
        let status = Command::new(path).args(args).status()?;
        if status.success() {
            return Ok(());
        }
        // Go's *exec.ExitError text.
        let text = match status.code() {
            Some(code) => format!("exit status {code}"),
            None => {
                use std::os::unix::process::ExitStatusExt;
                match status.signal() {
                    Some(sig) => format!("signal: {}", signal_name(sig)),
                    None => status.to_string(),
                }
            }
        };
        Err(Box::new(ExitError(text)))
    }
}

fn signal_name(sig: i32) -> String {
    match sig {
        1 => "hangup".into(),
        2 => "interrupt".into(),
        3 => "quit".into(),
        6 => "aborted".into(),
        9 => "killed".into(),
        11 => "segmentation fault".into(),
        13 => "broken pipe".into(),
        15 => "terminated".into(),
        n => format!("signal {n}"),
    }
}

/// Installs downloaded release bytes over `self_path`: the candidate must pass
/// `<candidate> setup --check`, then `<self_path> setup` runs on the new binary;
/// failure restores the previous one. Returns the backup path.
pub fn install(
    self_path: &Path,
    data: &[u8],
    runner: &dyn CommandRunner,
) -> Result<PathBuf, ReplaceError> {
    release::replace(
        self_path,
        data,
        |candidate| runner.run(candidate, &["setup", "--check"]),
        || runner.run(self_path, &["setup"]),
    )
}

/// Whether the running executable is the native binary of the npm package.
pub fn npm_managed_executable() -> bool {
    std::env::current_exe().is_ok_and(|p| npm_managed_path(&p))
}

/// `<pkg>/bin/bp-<os>-<arch>` with `<pkg>/package.json` naming the package.
pub fn npm_managed_path(self_path: &Path) -> bool {
    let Ok(resolved) = std::fs::canonicalize(self_path) else {
        return false;
    };
    let platform = release::platform();
    if resolved.file_name().and_then(|n| n.to_str()) != Some(platform.as_str()) {
        return false;
    }
    let Some(bin) = resolved.parent() else {
        return false;
    };
    if bin.file_name().and_then(|n| n.to_str()) != Some("bin") {
        return false;
    }
    let root = bin.parent().unwrap_or(Path::new("/"));
    let Ok(data) = std::fs::read(root.join("package.json")) else {
        return false;
    };
    let Some(value) = gojson::parse(&data) else {
        return false;
    };
    let name = match &value {
        Value::Object(map) => match gojson::string_field(map, "name") {
            Ok(name) => name.unwrap_or_default(),
            Err(()) => return false,
        },
        Value::Null => String::new(),
        _ => return false,
    };
    name == NPM_PACKAGE
}

/// The command a user should run to update this installation.
pub fn update_command() -> &'static str {
    if npm_managed_executable() {
        "npm install -g @tunapro/blueprint@latest"
    } else {
        "bp update"
    }
}
