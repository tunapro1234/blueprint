//! Port of internal/compositor/compositor_test.go. Go puts the fake
//! hyprctl/swaymsg on PATH; here the adapter's `bin` names the fake script
//! directly (mutating PATH would race with the other tests in the process),
//! and the log path is written into the script instead of `$COMMAND_LOG`.

use super::*;
use std::collections::HashMap;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

struct TempDir(PathBuf);

impl TempDir {
    fn new(name: &str) -> Self {
        let dir =
            std::env::temp_dir().join(format!("bp-tmux-compositor-{name}-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        TempDir(dir)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn write_fake_command(dir: &Path, name: &str, script: &str) -> String {
    let path = dir.join(name);
    std::fs::write(&path, script).unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    path.to_string_lossy().into_owned()
}

fn detect_with(values: &HashMap<&str, &str>) -> Result<Kind, CompositorError> {
    detect_kind(|key| values.get(key).copied().unwrap_or_default().to_string())
}

#[test]
fn detect_environment_requires_supported_compositor() {
    let mut values = HashMap::new();
    let err = detect_with(&values).unwrap_err();
    assert!(
        err.to_string().contains("no supported compositor detected"),
        "{err}"
    );
    let getenv_empty = |_: &str| String::new();
    assert!(detect_environment(getenv_empty).is_err());
    values.insert("SWAYSOCK", "/tmp/sway-ipc.sock");
    assert_eq!(detect_with(&values).unwrap(), Kind::Sway);
    values.insert("HYPRLAND_INSTANCE_SIGNATURE", "instance");
    assert_eq!(detect_with(&values).unwrap(), Kind::Hyprland);
}

#[test]
fn hyprland_adapter_uses_hyprctl() {
    let dir = TempDir::new("hypr");
    let log = dir.0.join("commands.log");
    let bin = write_fake_command(
        &dir.0,
        "hyprctl",
        &format!(
            r#"#!/bin/sh
printf '%s\n' "$*" >> '{}'
if [ "$1" = "-j" ]; then
  cat <<'JSON'
[{{"address":"0x1234","pid":42,"class":"kitty","focused":false,"focusHistoryID":0,"workspace":{{"id":7,"name":"dev"}}}}]
JSON
fi
"#,
            log.display()
        ),
    );
    let adapter = Hyprland { bin };
    let windows = adapter.list_windows().unwrap();
    assert_eq!(
        windows,
        vec![Window {
            id: "0x1234".into(),
            pid: 42,
            class: "kitty".into(),
            workspace: "dev".into(),
            focused: true,
        }]
    );
    adapter.focus_window("0x1234").unwrap();
    adapter
        .set_border_colors("0x1234", "#123456", "#010203")
        .unwrap();
    assert!(
        adapter.focus_window("not-an-address").is_err(),
        "invalid Hyprland address was accepted"
    );
    let data = std::fs::read_to_string(&log).unwrap();
    for want in [
        "-j clients",
        "dispatch focuswindow address:0x1234",
        "setprop address:0x1234 activebordercolor rgb(123456)",
        "setprop address:0x1234 inactivebordercolor rgb(010203)",
    ] {
        assert!(
            data.contains(want),
            "hyprctl did not receive {want:?}; log={data}"
        );
    }
}

#[test]
fn sway_adapter_lists_and_focuses_windows() {
    let dir = TempDir::new("sway");
    let log = dir.0.join("commands.log");
    let bin = write_fake_command(
        &dir.0,
        "swaymsg",
        &format!(
            r#"#!/bin/sh
printf '%s\n' "$*" >> '{}'
if [ "$2" = "get_tree" ]; then
  cat <<'JSON'
{{"id":1,"type":"root","nodes":[{{"id":2,"type":"workspace","name":"2:web","nodes":[{{"id":9,"type":"con","pid":55,"app_id":"foot","focused":true}}]}}]}}
JSON
fi
"#,
            log.display()
        ),
    );
    let adapter = Sway { bin };
    let windows = adapter.list_windows().unwrap();
    assert_eq!(
        windows,
        vec![Window {
            id: "9".into(),
            pid: 55,
            class: "foot".into(),
            workspace: "2:web".into(),
            focused: true,
        }]
    );
    adapter.focus_window("9").unwrap();
    assert!(
        adapter.focus_window("-1").is_err(),
        "negative sway window id was accepted"
    );
    assert_eq!(
        adapter.set_border_colors("9", "#ffffff", "#777777"),
        Err(CompositorError::BorderColorsUnsupported)
    );
    let data = std::fs::read_to_string(&log).unwrap();
    for want in ["-t get_tree -r", "[con_id=9] focus"] {
        assert!(
            data.contains(want),
            "swaymsg did not receive {want:?}; log={data}"
        );
    }
}

#[test]
fn failing_command_reports_go_style_error() {
    let dir = TempDir::new("fail");
    let bin = write_fake_command(&dir.0, "hyprctl", "#!/bin/sh\necho boom >&2\nexit 3\n");
    let err = Hyprland { bin }.list_windows().unwrap_err();
    assert_eq!(err.to_string(), "hyprctl -j: exit status 3: boom");
}
