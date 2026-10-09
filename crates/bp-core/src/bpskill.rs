//! Port of internal/bpskill: installs the bundled Blueprint skill for Codex and
//! Claude without overwriting user-owned copies.

use std::io;
use std::path::Path;

use crate::{fs, gopath};

/// The bundled `SKILL.md` (Go `bpskill.Content`).
pub const CONTENT: &str = include_str!("../../../internal/bpskill/SKILL.md");

const MANAGED_MARKER: &str = "<!-- Managed by bp setup. -->";

/// Install failures; the results gathered so far are kept.
#[derive(Debug, thiserror::Error)]
#[error("{message}")]
pub struct InstallError {
    pub message: String,
    /// Lines produced before the failure.
    pub results: Vec<String>,
    #[source]
    pub source: Option<io::Error>,
}

fn is_symlink(path: &str) -> bool {
    std::fs::symlink_metadata(path).is_ok_and(|m| m.file_type().is_symlink())
}

/// Go `bpskill.Install`: updates only bp-managed skills. Unmarked user
/// skills and symlinks stay untouched; modified managed copies get a backup
/// before replacement. Empty `codex_home`/`claude_home` default to
/// `<home>/.codex` and `<home>/.claude`.
pub fn install(
    home: &str,
    codex_home: &str,
    claude_home: &str,
) -> Result<Vec<String>, InstallError> {
    let codex_home = if codex_home.is_empty() {
        gopath::join2(home, ".codex")
    } else {
        codex_home.to_string()
    };
    let claude_home = if claude_home.is_empty() {
        gopath::join2(home, ".claude")
    } else {
        claude_home.to_string()
    };
    let mut results = Vec::new();
    let mut seen: Vec<String> = Vec::new();
    for root in [codex_home, claude_home] {
        let dir = gopath::join(&[root.as_str(), "skills", "blueprint"]);
        let path = gopath::join2(&dir, "SKILL.md");
        if seen.contains(&path) {
            continue;
        }
        seen.push(path.clone());
        let fail = |results: &Vec<String>, err: io::Error, message: String| InstallError {
            message,
            results: results.clone(),
            source: Some(err),
        };
        if is_symlink(&dir) || is_symlink(&path) {
            results.push(format!("preserved user skill: {path}"));
            continue;
        }
        match std::fs::read(&path) {
            Ok(old) => {
                if old == CONTENT.as_bytes() {
                    results.push(format!("skill ready: {path}"));
                    continue;
                }
                if !contains(&old, MANAGED_MARKER.as_bytes()) {
                    results.push(format!("preserved user skill: {path}"));
                    continue;
                }
                let (mut backup, _) = fs::create_temp(Path::new(&dir), "SKILL.md.before-bp-*")
                    .map_err(|e| {
                        let message = e.to_string();
                        fail(&results, e, message)
                    })?;
                io::Write::write_all(&mut backup, &old).map_err(|e| {
                    let message = e.to_string();
                    fail(&results, e, message)
                })?;
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => {
                let message = err.to_string();
                return Err(fail(&results, err, message));
            }
        }
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&dir)
            .map_err(|e| {
                let message = e.to_string();
                fail(&results, e, message)
            })?;
        let options = fs::AtomicOptions {
            temp_pattern: ".skill-*".to_string(),
            mode: None,
            preserve_mode: false,
            sync_dir: false,
        };
        fs::atomic_write(Path::new(&path), CONTENT.as_bytes(), &options).map_err(|e| {
            let message = format!("install skill {path}: {e}");
            fail(&results, e, message)
        })?;
        results.push(format!("skill installed: {path}"));
    }
    Ok(results)
}

fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    haystack.windows(needle.len()).any(|w| w == needle)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(p: &Path) -> String {
        p.to_string_lossy().into_owned()
    }

    #[test]
    fn install_both_clis_and_preserve_user_changes() {
        let tmp = tempfile::tempdir().unwrap();
        let home = s(tmp.path());
        install(&home, "", "").unwrap();
        for cli in [".codex", ".claude"] {
            let path = tmp.path().join(cli).join("skills/blueprint/SKILL.md");
            assert_eq!(
                std::fs::read(&path).unwrap(),
                CONTENT.as_bytes(),
                "missing bundled skill {path:?}"
            );
        }
        install(&home, "", "").unwrap();
        let path = tmp.path().join(".codex/skills/blueprint/SKILL.md");
        std::fs::write(&path, "user-owned skill").unwrap();
        let other = tmp.path().join(".claude/skills/blueprint/SKILL.md");
        let old = b"<!-- Managed by bp setup. -->\nuser edits to managed copy";
        std::fs::write(&other, old).unwrap();
        install(&home, "", "").unwrap();
        assert_eq!(
            std::fs::read_to_string(&path).unwrap(),
            "user-owned skill",
            "overwrote user skill"
        );
        let backups: Vec<_> = std::fs::read_dir(other.parent().unwrap())
            .unwrap()
            .map(|e| e.unwrap().path())
            .filter(|p| {
                p.file_name()
                    .unwrap()
                    .to_str()
                    .unwrap()
                    .starts_with("SKILL.md.before-bp-")
            })
            .collect();
        assert_eq!(backups.len(), 1, "backups: {backups:?}");
        assert_eq!(
            std::fs::read(&backups[0]).unwrap(),
            old,
            "backup lost changes"
        );
        assert_eq!(std::fs::read(&other).unwrap(), CONTENT.as_bytes());
    }

    #[test]
    fn install_respects_native_homes_and_symlinked_user_skill() {
        let tmp = tempfile::tempdir().unwrap();
        let home = s(tmp.path());
        let native = s(&tmp.path().join("custom-native"));
        install(&home, &native, &native).unwrap();
        assert!(
            !tmp.path().join(".codex").exists(),
            "ignored native home override"
        );
        let target = tmp.path().join("owned.md");
        std::fs::write(&target, "keep").unwrap();
        let other = tmp.path().join("custom-claude/skills/blueprint");
        std::fs::create_dir_all(&other).unwrap();
        std::os::unix::fs::symlink(&target, other.join("SKILL.md")).unwrap();
        let results = install(&home, &native, &s(&tmp.path().join("custom-claude"))).unwrap();
        assert_eq!(
            std::fs::read_to_string(&target).unwrap(),
            "keep",
            "modified symlink target"
        );
        let meta = std::fs::symlink_metadata(other.join("SKILL.md")).unwrap();
        assert!(meta.file_type().is_symlink(), "replaced user symlink");
        assert!(
            results
                .iter()
                .any(|r| r.starts_with("preserved user skill: ")),
            "{results:?}"
        );
    }
}
