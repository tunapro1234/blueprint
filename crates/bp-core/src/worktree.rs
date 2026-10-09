//! Port of internal/worktree: Blueprint's managed git worktrees
//! (`<repo>/.worktrees/<topic>` on branch `<topic>/dev`).

use std::process::Command;

use crate::gopath;
use crate::text::go_is_space;

/// Worktree failures (messages match Go's `fmt.Errorf` texts).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct Error(pub String);

pub type Result<T> = std::result::Result<T, Error>;

fn err<T>(message: impl Into<String>) -> Result<T> {
    Err(Error(message.into()))
}

/// The fixed path and branch convention for a managed worktree.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Derived {
    pub repo: String,
    pub path: String,
    pub branch: String,
}

/// One entry from `git worktree list`.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Info {
    pub repo: String,
    pub path: String,
    pub branch: String,
    pub main: bool,
}

/// Performs worktree operations through git.
#[derive(Debug, Clone)]
pub struct Manager {
    pub git: String,
}

impl Default for Manager {
    fn default() -> Self {
        Manager { git: "git".into() }
    }
}

fn go_quote(s: &str) -> String {
    crate::gojson::time::go_quote(s)
}

/// Go `worktree.Derive`: applies the `.worktrees/<topic>` and `<topic>/dev`
/// convention.
pub fn derive(repo_dir: &str, topic: &str) -> Result<Derived> {
    validate_topic(topic)?;
    let repo = gopath::abs(repo_dir).map_err(|e| Error(format!("resolve repository path: {e}")))?;
    let repo = gopath::clean(&repo);
    Ok(Derived {
        path: gopath::join(&[repo.as_str(), ".worktrees", topic]),
        branch: format!("{topic}/dev"),
        repo,
    })
}

fn validate_topic(topic: &str) -> Result<()> {
    if topic.is_empty()
        || topic == "."
        || topic == ".."
        || gopath::base(topic) != topic
        || topic.contains(['/', '\\'])
    {
        return err(format!(
            "invalid worktree topic {}: expected one path component",
            go_quote(topic)
        ));
    }
    let invalid = || err(format!("invalid worktree topic {}", go_quote(topic)));
    if topic.starts_with('-')
        || topic.starts_with('.')
        || topic.ends_with('.')
        || topic.ends_with(".lock")
        || topic.contains("..")
        || topic.contains("@{")
    {
        return invalid();
    }
    if topic
        .chars()
        .any(|c| go_is_space(c) || c.is_control() || "~^:?*[".contains(c))
    {
        return invalid();
    }
    Ok(())
}

impl Manager {
    pub fn new() -> Self {
        Self::default()
    }

    fn run(&self, args: &[&str]) -> Result<Vec<u8>> {
        let output = Command::new(&self.git).args(args).output();
        let fail = |detail: String| Error(format!("git {}: {detail}", args.join(" ")));
        match output {
            Ok(out) if out.status.success() => {
                let mut combined = out.stdout;
                combined.extend_from_slice(&out.stderr);
                Ok(combined)
            }
            Ok(out) => {
                let mut combined = out.stdout;
                combined.extend_from_slice(&out.stderr);
                let detail = String::from_utf8_lossy(&combined).trim().to_string();
                let detail = if detail.is_empty() {
                    match out.status.code() {
                        Some(code) => format!("exit status {code}"),
                        None => out.status.to_string(),
                    }
                } else {
                    detail
                };
                Err(fail(detail))
            }
            Err(e) => Err(fail(e.to_string())),
        }
    }

    /// The main worktree for the repository containing `dir`.
    pub fn resolve_repo(&self, dir: &str) -> Result<String> {
        let abs = gopath::abs(dir).map_err(|e| Error(format!("resolve repository path: {e}")))?;
        if !std::fs::metadata(&abs).is_ok_and(|m| m.is_dir()) {
            return err(format!("repository directory does not exist: {dir}"));
        }
        let bare = self
            .run(&["-C", &abs, "rev-parse", "--is-bare-repository"])
            .map_err(|e| Error(format!("{dir} is not a git repository: {e}")))?;
        if String::from_utf8_lossy(&bare).trim() == "true" {
            return err(format!("bare repositories are not supported: {dir}"));
        }
        let entries = self.list_from(&abs)?;
        match entries.first() {
            Some(first) => Ok(canonical(&first.path)),
            None => err(format!("git returned no worktrees for {dir}")),
        }
    }

    /// The managed worktree, created when missing.
    pub fn ensure(&self, repo_dir: &str, topic: &str) -> Result<Info> {
        let repo = self.resolve_repo(repo_dir)?;
        let derived = derive(&repo, topic)?;
        for mut entry in self.list_from(&repo)? {
            if same_path(&entry.path, &derived.path) {
                if entry.branch != derived.branch {
                    return err(format!(
                        "worktree {} uses branch {}, expected {}",
                        derived.path, entry.branch, derived.branch
                    ));
                }
                entry.repo = repo;
                return Ok(entry);
            }
        }
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o755)
            .create(gopath::dir(&derived.path))
            .map_err(|e| Error(format!("create worktree directory: {e}")))?;
        self.run(&[
            "-C",
            &repo,
            "worktree",
            "add",
            "-B",
            &derived.branch,
            &derived.path,
        ])?;
        Ok(Info {
            repo,
            path: derived.path,
            branch: derived.branch,
            main: false,
        })
    }

    /// Worktrees managed below `<repo>/.worktrees`.
    pub fn list(&self, repo_dir: &str) -> Result<Vec<Info>> {
        let repo = self.resolve_repo(repo_dir)?;
        let root = gopath::join2(&repo, ".worktrees");
        Ok(self
            .list_from(&repo)?
            .into_iter()
            .filter(|e| !e.main && contains_path(&root, &e.path))
            .map(|mut e| {
                e.repo = repo.clone();
                e
            })
            .collect())
    }

    /// Deletes a managed worktree. Dirty worktrees require `force`.
    pub fn remove(&self, repo_dir: &str, topic: &str, force: bool) -> Result<()> {
        let repo = self.resolve_repo(repo_dir)?;
        let derived = derive(&repo, topic)?;
        let found = self
            .list_from(&repo)?
            .iter()
            .any(|e| same_path(&e.path, &derived.path) && !e.main);
        if !found {
            return err(format!("worktree does not exist: {}", derived.path));
        }
        if !force {
            let status = self.run(&[
                "-C",
                &derived.path,
                "status",
                "--porcelain",
                "--untracked-files=all",
            ])?;
            if !String::from_utf8_lossy(&status).trim().is_empty() {
                return err(format!(
                    "worktree is dirty: {} (use --force to remove it)",
                    derived.path
                ));
            }
        }
        let mut args = vec!["-C", repo.as_str(), "worktree", "remove"];
        if force {
            args.push("--force");
        }
        args.push(&derived.path);
        self.run(&args).map(|_| ())
    }

    /// The git worktree containing `dir`; `main` is true for the primary
    /// checkout.
    pub fn inspect(&self, dir: &str) -> Result<Info> {
        let top = self.run(&["-C", dir, "rev-parse", "--show-toplevel"])?;
        let top_path = canonical(String::from_utf8_lossy(&top).trim());
        let entries = self.list_from(&top_path)?;
        let Some(first) = entries.first() else {
            return err(format!("git returned no worktrees for {dir}"));
        };
        let repo = canonical(&first.path);
        for mut entry in entries {
            if same_path(&entry.path, &top_path) {
                entry.repo = repo;
                return Ok(entry);
            }
        }
        err(format!("could not identify worktree for {dir}"))
    }

    fn list_from(&self, dir: &str) -> Result<Vec<Info>> {
        let out = self.run(&["-C", dir, "worktree", "list", "--porcelain", "-z"])?;
        Ok(parse_list(&out))
    }
}

/// Parses `git worktree list --porcelain -z` output.
pub fn parse_list(data: &[u8]) -> Vec<Info> {
    let mut entries: Vec<Info> = Vec::new();
    let mut current = Info::default();
    fn flush(entries: &mut Vec<Info>, current: &mut Info) {
        if current.path.is_empty() {
            return;
        }
        let mut entry = std::mem::take(current);
        entry.path = canonical(&entry.path);
        entry.main = entries.is_empty();
        entries.push(entry);
    }
    for field in data.split(|b| *b == 0) {
        if field.is_empty() {
            flush(&mut entries, &mut current);
            continue;
        }
        let line = String::from_utf8_lossy(field);
        if let Some(path) = line.strip_prefix("worktree ") {
            if !current.path.is_empty() {
                flush(&mut entries, &mut current);
            }
            current.path = path.to_string();
        } else if let Some(branch) = line.strip_prefix("branch refs/heads/") {
            current.branch = branch.to_string();
        } else if line == "detached" {
            current.branch = "(detached)".into();
        }
    }
    flush(&mut entries, &mut current);
    entries
}

/// Whether `path` is `root` itself or lies below it.
pub fn contains_path(root: &str, path: &str) -> bool {
    let (root, path) = (canonical(root), canonical(path));
    match gopath::rel(&root, &path) {
        Ok(rel) => rel != ".." && !rel.starts_with("../"),
        Err(_) => false,
    }
}

fn same_path(left: &str, right: &str) -> bool {
    canonical(left) == canonical(right)
}

fn canonical(path: &str) -> String {
    let mut path = gopath::abs(path).unwrap_or_else(|_| path.to_string());
    if let Ok(resolved) = std::fs::canonicalize(&path) {
        path = resolved.to_string_lossy().into_owned();
    }
    gopath::clean(&path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_str(dir: &tempfile::TempDir) -> String {
        dir.path().to_string_lossy().into_owned()
    }

    #[test]
    fn derive_worktree_path_and_branch() {
        let tmp = tempfile::tempdir().unwrap();
        let repo = gopath::join2(&tmp_str(&tmp), "example-repo");
        let got = derive(&repo, "shop").unwrap();
        assert_eq!(
            got.path,
            gopath::join(&[repo.as_str(), ".worktrees", "shop"])
        );
        assert_eq!(got.branch, "shop/dev");
        assert_eq!(got.repo, repo);
    }

    #[test]
    fn derive_rejects_unsafe_topics() {
        let tmp = tempfile::tempdir().unwrap();
        for topic in [
            "",
            ".",
            "..",
            "../shop",
            "shop/next",
            "shop\\next",
            "-shop",
            "shop dev",
            "shop..next",
        ] {
            assert!(
                derive(&tmp_str(&tmp), topic).is_err(),
                "Derive accepted unsafe topic {topic:?}"
            );
        }
        assert_eq!(
            derive("/r", "a b").unwrap_err().0,
            "invalid worktree topic \"a b\""
        );
        assert_eq!(
            derive("/r", "a/b").unwrap_err().0,
            "invalid worktree topic \"a/b\": expected one path component"
        );
    }

    #[test]
    fn contains_path_cases() {
        let tmp = tempfile::tempdir().unwrap();
        let root = gopath::join(&[tmp_str(&tmp).as_str(), "repo", ".worktrees", "shop"]);
        assert!(contains_path(
            &root,
            &gopath::join(&[root.as_str(), "cmd", "bp"])
        ));
        assert!(!contains_path(&root, &format!("{root}-old")));
    }

    #[test]
    fn parse_list_porcelain() {
        let data = b"worktree /nonexistent/main\0HEAD abc\0branch refs/heads/main\0\0worktree /nonexistent/main/.worktrees/x\0HEAD def\0detached\0\0";
        let got = parse_list(data);
        assert_eq!(got.len(), 2);
        assert!(got[0].main && got[0].branch == "main" && got[0].path == "/nonexistent/main");
        assert!(!got[1].main && got[1].branch == "(detached)");
    }

    fn run_git(dir: &str, args: &[&str]) {
        let out = Command::new("git")
            .arg("-C")
            .arg(dir)
            .args(args)
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "git {}: {}",
            args.join(" "),
            String::from_utf8_lossy(&out.stderr)
        );
    }

    #[test]
    fn manager_lifecycle_and_dirty_removal() {
        if crate::lowprio::look_path("git").is_none() {
            eprintln!("skip: git is not installed");
            return;
        }
        let tmp = tempfile::tempdir().unwrap();
        // Canonical temp root so path comparisons match git's resolved paths.
        let base = canonical(&tmp_str(&tmp));
        let repo = gopath::join2(&base, "repo");
        std::fs::create_dir(&repo).unwrap();
        run_git(&repo, &["init", "-q"]);
        run_git(&repo, &["config", "user.email", "test@example.com"]);
        run_git(&repo, &["config", "user.name", "Blueprint Test"]);
        std::fs::write(gopath::join2(&repo, "seed.txt"), "seed\n").unwrap();
        run_git(&repo, &["add", "seed.txt"]);
        run_git(&repo, &["commit", "-qm", "seed"]);

        let manager = Manager::new();
        let entry = manager.ensure(&repo, "shop").unwrap();
        let want_path = gopath::join(&[repo.as_str(), ".worktrees", "shop"]);
        assert_eq!(entry.path, want_path);
        assert_eq!(entry.branch, "shop/dev");
        let again = manager.ensure(&repo, "shop").unwrap();
        assert_eq!(again.path, want_path);
        let entries = manager.list(&repo).unwrap();
        assert_eq!(entries.len(), 1, "{entries:?}");
        assert_eq!(entries[0].path, want_path);

        let nested = gopath::join2(&want_path, "nested");
        std::fs::create_dir(&nested).unwrap();
        let inspected = manager.inspect(&nested).unwrap();
        assert!(
            !inspected.main && inspected.repo == repo && inspected.branch == "shop/dev",
            "{inspected:?}"
        );
        assert_eq!(manager.resolve_repo(&nested).unwrap(), repo);

        std::fs::write(gopath::join2(&want_path, "dirty.txt"), "dirty\n").unwrap();
        let e = manager.remove(&repo, "shop", false).unwrap_err();
        assert!(e.0.contains("worktree is dirty"), "{e}");
        manager.remove(&repo, "shop", true).unwrap();
        assert!(!std::path::Path::new(&want_path).exists());
    }
}
