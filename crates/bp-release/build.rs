//! Stamps the VCS state like Go's `-buildvcs` (`vcs.revision`, `vcs.modified`).
//!
//! Release builds may override with `BP_BUILD_REVISION` / `BP_BUILD_MODIFIED`
//! (`true`/`false`). Without git both stay empty/false, as in Go.

use std::path::{Path, PathBuf};
use std::process::Command;

fn git(dir: &Path, args: &[&str]) -> Option<String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(args)
        .output()
        .ok()?;
    output.status.success().then(|| {
        String::from_utf8_lossy(&output.stdout)
            .trim_end()
            .to_string()
    })
}

fn watch(path: PathBuf) {
    // A missing path would make Cargo rerun the script on every build.
    if path.exists() {
        println!("cargo:rerun-if-changed={}", path.display());
    }
}

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=BP_BUILD_REVISION");
    println!("cargo:rerun-if-env-changed=BP_BUILD_MODIFIED");
    let dir = PathBuf::from(std::env::var_os("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));

    let mut revision = String::new();
    let mut modified = false;
    if let Some(head) = git(&dir, &["rev-parse", "HEAD"]) {
        revision = head;
        // --no-optional-locks keeps `git status` from rewriting .git/index,
        // which would otherwise retrigger this script on every build.
        modified = git(&dir, &["--no-optional-locks", "status", "--porcelain"])
            .is_some_and(|out| !out.is_empty());
        if let Some(git_dir) = git(&dir, &["rev-parse", "--absolute-git-dir"]) {
            let git_dir = PathBuf::from(git_dir);
            watch(git_dir.join("HEAD"));
            watch(git_dir.join("index"));
            let common = git(&dir, &["rev-parse", "--git-common-dir"])
                .map(|c| {
                    let c = PathBuf::from(c);
                    if c.is_absolute() { c } else { dir.join(c) }
                })
                .unwrap_or_else(|| git_dir.clone());
            if let Some(reference) = git(&dir, &["symbolic-ref", "-q", "HEAD"]) {
                watch(git_dir.join(&reference));
                watch(common.join(&reference));
            }
            watch(common.join("packed-refs"));
        }
    }
    if let Ok(value) = std::env::var("BP_BUILD_REVISION") {
        revision = value;
    }
    if let Ok(value) = std::env::var("BP_BUILD_MODIFIED") {
        modified = value == "true";
    }
    println!("cargo:rustc-env=BP_BUILD_REVISION={revision}");
    println!("cargo:rustc-env=BP_BUILD_MODIFIED={modified}");
}
