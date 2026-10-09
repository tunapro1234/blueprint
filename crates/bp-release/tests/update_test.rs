//! Port of TestNpmManagedPathRequiresPackageAndNativeLayout (cmd/bp/update_test.go)
//! and the setup/--check sequencing of `bp update`.

use bp_release::release::{BoxError, platform};
use bp_release::update::{CommandRunner, ExecRunner, install, npm_managed_path};
use std::cell::RefCell;
use std::path::{Path, PathBuf};

#[test]
fn npm_managed_path_requires_package_and_native_layout() {
    let p = platform();
    for (name, pkg, file, want) in [
        (
            "npm native",
            r#"{"name":"@tunapro/blueprint"}"#,
            p.as_str(),
            true,
        ),
        ("other package", r#"{"name":"other"}"#, p.as_str(), false),
        ("invalid metadata", "{", p.as_str(), false),
        (
            "standalone binary",
            r#"{"name":"@tunapro/blueprint"}"#,
            "bp",
            false,
        ),
    ] {
        let root = tempfile::tempdir().unwrap();
        std::fs::create_dir(root.path().join("bin")).unwrap();
        let target = root.path().join("bin").join(file);
        std::fs::write(&target, "fixture").unwrap();
        std::fs::write(root.path().join("package.json"), pkg).unwrap();
        let link = root.path().join("linked-bp");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        assert_eq!(npm_managed_path(&link), want, "{name}");
    }
}

#[derive(Default)]
struct Recorder {
    calls: RefCell<Vec<(PathBuf, Vec<String>)>>,
    fail: Option<&'static str>,
}

impl CommandRunner for Recorder {
    fn run(&self, path: &Path, args: &[&str]) -> Result<(), BoxError> {
        let joined = args.join(" ");
        self.calls.borrow_mut().push((
            path.to_path_buf(),
            args.iter().map(|s| s.to_string()).collect(),
        ));
        if self.fail == Some(joined.as_str()) {
            return Err("exit status 1".into());
        }
        Ok(())
    }
}

#[test]
fn install_checks_candidate_then_runs_setup() {
    let dir = tempfile::tempdir().unwrap();
    let target = dir.path().join("bp");
    std::fs::write(&target, "old").unwrap();
    let runner = Recorder::default();
    install(&target, b"new", &runner).unwrap();
    let calls = runner.calls.borrow();
    assert_eq!(calls.len(), 2);
    assert!(
        calls[0]
            .0
            .file_name()
            .unwrap()
            .to_string_lossy()
            .starts_with(".bp-update-")
    );
    assert_eq!(calls[0].1, ["setup", "--check"]);
    assert_eq!(calls[1], (target.clone(), vec!["setup".to_string()]));
    assert_eq!(std::fs::read(&target).unwrap(), b"new");

    for (fail, want) in [
        ("setup --check", "exit status 1"),
        (
            "setup",
            "setup failed; previous binary restored: exit status 1",
        ),
    ] {
        std::fs::write(&target, "old").unwrap();
        let runner = Recorder {
            fail: Some(fail),
            ..Recorder::default()
        };
        let err = install(&target, b"new", &runner).unwrap_err();
        assert_eq!(err.to_string(), want);
        assert_eq!(std::fs::read(&target).unwrap(), b"old");
    }
}

#[test]
fn exec_runner_reports_exit_status_like_go() {
    let runner = ExecRunner;
    runner.run(Path::new("/bin/sh"), &["-c", "exit 0"]).unwrap();
    let err = runner
        .run(Path::new("/bin/sh"), &["-c", "exit 3"])
        .unwrap_err();
    assert_eq!(err.to_string(), "exit status 3");
    let err = runner
        .run(Path::new("/bin/sh"), &["-c", "kill -9 $$"])
        .unwrap_err();
    assert_eq!(err.to_string(), "signal: killed");
}

#[test]
fn install_runs_a_real_candidate() {
    use std::os::unix::fs::PermissionsExt;
    let dir = tempfile::tempdir().unwrap();
    let target = dir.path().join("bp");
    std::fs::write(&target, "#!/bin/sh\nexit 0\n").unwrap();
    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o755)).unwrap();
    let log = dir.path().join("log");
    let script = format!("#!/bin/sh\necho \"$*\" >> {}\nexit 0\n", log.display());
    let backup = install(&target, script.as_bytes(), &ExecRunner).unwrap();
    assert_eq!(
        std::fs::read_to_string(&log).unwrap(),
        "setup --check\nsetup\n"
    );
    assert_eq!(
        std::fs::read_to_string(&backup).unwrap(),
        "#!/bin/sh\nexit 0\n"
    );
}
