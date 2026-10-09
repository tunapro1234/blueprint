//! Port of internal/lowprio: runs background collectors below interactive work.
//!
//! Periodic jobs (usage pulses, dashboard generation, watchers) are CPU heavy
//! but never latency sensitive, so they run under `nice -n NICENESS`. The
//! niceness is inherited by every child they spawn. Hosts without a nice
//! binary run the command unchanged.

use std::path::{Path, PathBuf};
use std::process::Command;

/// Scheduling priority given to background collectors.
pub const NICENESS: &str = "10";

/// Go `exec.LookPath`: the first executable regular file named `name` on
/// `$PATH` (or `name` itself when it contains a slash).
pub fn look_path(name: &str) -> Option<PathBuf> {
    use std::os::unix::fs::PermissionsExt;
    let executable = |path: &Path| {
        std::fs::metadata(path).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
    };
    if name.contains('/') {
        let path = PathBuf::from(name);
        return executable(&path).then_some(path);
    }
    let path_var = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&path_var) {
        let dir = if dir.as_os_str().is_empty() {
            PathBuf::from(".")
        } else {
            dir
        };
        let candidate = dir.join(name);
        if executable(&candidate) {
            return Some(candidate);
        }
    }
    None
}

/// `argv` prefixed with nice when `look_path("nice")` finds it.
pub fn args_with<F>(look_path: F, args: &[String]) -> Vec<String>
where
    F: Fn(&str) -> Option<PathBuf>,
{
    if args.is_empty() {
        return Vec::new();
    }
    let Some(nice) = look_path("nice") else {
        return args.to_vec();
    };
    let mut out = vec![
        nice.to_string_lossy().into_owned(),
        "-n".to_string(),
        NICENESS.to_string(),
    ];
    out.extend_from_slice(args);
    out
}

/// Go `lowprio.Args`: `argv` prefixed with nice when it is available.
pub fn args(args: &[String]) -> Vec<String> {
    args_with(look_path, args)
}

/// Go `lowprio.CommandContext`: a [`Command`] for a low-priority command.
pub fn command<S: AsRef<str>>(name: &str, rest: &[S]) -> Command {
    let mut argv = vec![name.to_string()];
    argv.extend(rest.iter().map(|s| s.as_ref().to_string()));
    let argv = args(&argv);
    let mut cmd = Command::new(&argv[0]);
    cmd.args(&argv[1..]);
    cmd
}

#[cfg(test)]
mod tests {
    use super::*;

    fn owned(items: &[&str]) -> Vec<String> {
        items.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn args_prefixes_nice() {
        let found = |_: &str| Some(PathBuf::from("/usr/bin/nice"));
        let got = args_with(found, &owned(&["/usr/bin/python3", "gen.py"])).join(" ");
        assert_eq!(got, "/usr/bin/nice -n 10 /usr/bin/python3 gen.py");
        assert!(
            args_with(found, &[]).is_empty(),
            "empty argv grew a nice prefix"
        );

        let missing = |_: &str| None;
        assert_eq!(args_with(missing, &owned(&["a", "b"])).join(" "), "a b");
    }

    #[test]
    fn command_runs_niced() {
        if look_path("nice").is_none() {
            eprintln!("skip: no nice binary");
            return;
        }
        // `nice` with no arguments prints the current niceness.
        let out = command::<&str>("nice", &[]).output().unwrap();
        assert!(out.status.success());
        let got = String::from_utf8_lossy(&out.stdout).trim().to_string();
        assert!(
            got != "0" && !got.is_empty(),
            "child niceness = {got:?}, want raised"
        );
    }
}
