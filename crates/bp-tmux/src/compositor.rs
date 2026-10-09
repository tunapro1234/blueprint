//! Port of internal/compositor (compositor.go, hyprland.go, sway.go): the
//! window-manager adapters bp uses to find and focus agent windows.
//!
//! Commands run synchronously through `std::process::Command`; Go's context
//! argument has no counterpart here.

use std::process::Command;
use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

/// `Window`: one top-level compositor window.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Window {
    pub id: String,
    pub pid: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub class: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub workspace: String,
    pub focused: bool,
}

/// Errors of the compositor adapters.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum CompositorError {
    /// `ErrBorderColorsUnsupported`.
    #[error("per-window border colors are not supported by this compositor")]
    BorderColorsUnsupported,
    /// Any other failure, with Go's message text.
    #[error("{0}")]
    Other(String),
}

/// `Adapter`: the operations bp needs from a compositor.
pub trait Adapter {
    fn list_windows(&self) -> Result<Vec<Window>, CompositorError>;
    fn focus_window(&self, id: &str) -> Result<(), CompositorError>;
    fn set_border_colors(
        &self,
        id: &str,
        active: &str,
        inactive: &str,
    ) -> Result<(), CompositorError>;
}

/// `Detect`: the adapter for the running session's compositor.
pub fn detect() -> Result<Box<dyn Adapter>, CompositorError> {
    detect_environment(|key| std::env::var(key).unwrap_or_default())
}

/// The compositor `DetectEnvironment` selects.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Hyprland,
    Sway,
}

/// `DetectEnvironment` with the decision exposed: Hyprland wins when both
/// markers are set.
pub fn detect_kind(getenv: impl Fn(&str) -> String) -> Result<Kind, CompositorError> {
    if !getenv("HYPRLAND_INSTANCE_SIGNATURE").is_empty() {
        return Ok(Kind::Hyprland);
    }
    if !getenv("SWAYSOCK").is_empty() {
        return Ok(Kind::Sway);
    }
    Err(CompositorError::Other(
        "no supported compositor detected (expected HYPRLAND_INSTANCE_SIGNATURE or SWAYSOCK)"
            .to_string(),
    ))
}

/// `DetectEnvironment`.
pub fn detect_environment(
    getenv: impl Fn(&str) -> String,
) -> Result<Box<dyn Adapter>, CompositorError> {
    Ok(match detect_kind(getenv)? {
        Kind::Hyprland => Box::new(Hyprland::default()),
        Kind::Sway => Box::new(Sway::default()),
    })
}

/// Runs `bin args…` and returns its combined output, or Go's
/// `"<name> <arg0>: <err>: <trimmed output>"` error.
fn run_combined(bin: &str, name: &str, args: &[&str]) -> Result<Vec<u8>, CompositorError> {
    let fail = |err: String, out: &[u8]| {
        CompositorError::Other(format!(
            "{name} {}: {err}: {}",
            args.first().copied().unwrap_or_default(),
            String::from_utf8_lossy(out).trim()
        ))
    };
    let output = Command::new(bin)
        .args(args)
        .output()
        .map_err(|err| fail(err.to_string(), &[]))?;
    let mut combined = output.stdout;
    combined.extend_from_slice(&output.stderr);
    if !output.status.success() {
        return Err(fail(go_exit_status(&output.status), &combined));
    }
    Ok(combined)
}

/// Go's `*exec.ExitError` text.
pub(crate) fn go_exit_status(status: &std::process::ExitStatus) -> String {
    if let Some(code) = status.code() {
        return format!("exit status {code}");
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(signal) = status.signal() {
            return format!("signal: {signal}");
        }
    }
    status.to_string()
}

/// Go's `%q` for the ASCII-printable ids these adapters validate.
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32));
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

static HYPR_ADDRESS: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^0x[0-9a-fA-F]+$").expect("hyprAddress"));
static BORDER_COLOR: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^#[0-9a-fA-F]{6}$").expect("borderColor"));

/// `Hyprland`: drives `hyprctl` (`bin` overrides the executable).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Hyprland {
    pub bin: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct HyprClient {
    address: Option<String>,
    pid: Option<i64>,
    class: Option<String>,
    focused: Option<bool>,
    #[serde(rename = "focusHistoryID")]
    focus_history_id: Option<i64>,
    workspace: Option<HyprWorkspace>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct HyprWorkspace {
    id: Option<i64>,
    name: Option<String>,
}

impl Hyprland {
    fn command(&self, args: &[&str]) -> Result<Vec<u8>, CompositorError> {
        let bin = if self.bin.is_empty() {
            "hyprctl"
        } else {
            &self.bin
        };
        run_combined(bin, "hyprctl", args)
    }

    fn check_id(id: &str) -> Result<(), CompositorError> {
        if HYPR_ADDRESS.is_match(id) {
            Ok(())
        } else {
            Err(CompositorError::Other(format!(
                "invalid Hyprland window id {}",
                go_quote(id)
            )))
        }
    }
}

/// Parses `hyprctl -j clients` output.
pub(crate) fn parse_hyprland_clients(out: &[u8]) -> Result<Vec<Window>, CompositorError> {
    let clients: Option<Vec<HyprClient>> = serde_json::from_slice(out)
        .map_err(|err| CompositorError::Other(format!("decode hyprctl clients: {err}")))?;
    let mut windows = Vec::new();
    for client in clients.unwrap_or_default() {
        let address = client.address.unwrap_or_default();
        let pid = client.pid.unwrap_or_default();
        if pid <= 0 || !HYPR_ADDRESS.is_match(&address) {
            continue;
        }
        let workspace = client.workspace.unwrap_or_default();
        let mut workspace_name = workspace.name.unwrap_or_default();
        if workspace_name.is_empty() {
            workspace_name = workspace.id.unwrap_or_default().to_string();
        }
        let focused = client.focused.unwrap_or_default() || client.focus_history_id == Some(0);
        windows.push(Window {
            id: address,
            pid,
            class: client.class.unwrap_or_default(),
            workspace: workspace_name,
            focused,
        });
    }
    Ok(windows)
}

impl Adapter for Hyprland {
    fn list_windows(&self) -> Result<Vec<Window>, CompositorError> {
        let out = self.command(&["-j", "clients"])?;
        parse_hyprland_clients(&out)
    }

    fn focus_window(&self, id: &str) -> Result<(), CompositorError> {
        Self::check_id(id)?;
        self.command(&["dispatch", "focuswindow", &format!("address:{id}")])?;
        Ok(())
    }

    fn set_border_colors(
        &self,
        id: &str,
        active: &str,
        inactive: &str,
    ) -> Result<(), CompositorError> {
        Self::check_id(id)?;
        if !BORDER_COLOR.is_match(active) || !BORDER_COLOR.is_match(inactive) {
            return Err(CompositorError::Other(
                "border colors must be #rrggbb".to_string(),
            ));
        }
        let target = format!("address:{id}");
        self.command(&[
            "setprop",
            &target,
            "activebordercolor",
            &format!("rgb({})", &active[1..]),
        ])?;
        self.command(&[
            "setprop",
            &target,
            "inactivebordercolor",
            &format!("rgb({})", &inactive[1..]),
        ])?;
        Ok(())
    }
}

/// `Sway`: drives `swaymsg` (`bin` overrides the executable).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Sway {
    pub bin: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct SwayNode {
    id: Option<i64>,
    name: Option<String>,
    #[serde(rename = "type")]
    kind: Option<String>,
    pid: Option<i64>,
    app_id: Option<String>,
    focused: Option<bool>,
    nodes: Option<Vec<SwayNode>>,
    floating_nodes: Option<Vec<SwayNode>>,
    window_properties: Option<SwayWindowProperties>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct SwayWindowProperties {
    class: Option<String>,
}

/// Parses `swaymsg -t get_tree -r` output.
pub(crate) fn parse_sway_tree(out: &[u8]) -> Result<Vec<Window>, CompositorError> {
    let root: Option<SwayNode> = serde_json::from_slice(out)
        .map_err(|err| CompositorError::Other(format!("decode sway tree: {err}")))?;
    let mut windows = Vec::new();
    walk_sway(&root.unwrap_or_default(), "", &mut windows);
    Ok(windows)
}

fn walk_sway(node: &SwayNode, workspace: &str, windows: &mut Vec<Window>) {
    let name = node.name.as_deref().unwrap_or_default();
    let workspace = if node.kind.as_deref() == Some("workspace") {
        name
    } else {
        workspace
    };
    let pid = node.pid.unwrap_or_default();
    let app_id = node.app_id.as_deref().unwrap_or_default();
    let window_class = node
        .window_properties
        .as_ref()
        .and_then(|p| p.class.as_deref())
        .unwrap_or_default();
    if pid > 0 && (!app_id.is_empty() || !window_class.is_empty()) {
        let class = if app_id.is_empty() {
            window_class
        } else {
            app_id
        };
        windows.push(Window {
            id: node.id.unwrap_or_default().to_string(),
            pid,
            class: class.to_string(),
            workspace: workspace.to_string(),
            focused: node.focused.unwrap_or_default(),
        });
    }
    for child in node.nodes.iter().flatten() {
        walk_sway(child, workspace, windows);
    }
    for child in node.floating_nodes.iter().flatten() {
        walk_sway(child, workspace, windows);
    }
}

impl Sway {
    fn command(&self, args: &[&str]) -> Result<Vec<u8>, CompositorError> {
        let bin = if self.bin.is_empty() {
            "swaymsg"
        } else {
            &self.bin
        };
        run_combined(bin, "swaymsg", args)
    }
}

impl Adapter for Sway {
    fn list_windows(&self) -> Result<Vec<Window>, CompositorError> {
        let out = self.command(&["-t", "get_tree", "-r"])?;
        parse_sway_tree(&out)
    }

    fn focus_window(&self, id: &str) -> Result<(), CompositorError> {
        match id.parse::<i64>() {
            Ok(window_id) if window_id > 0 => {}
            _ => {
                return Err(CompositorError::Other(format!(
                    "invalid sway window id {}",
                    go_quote(id)
                )));
            }
        }
        self.command(&[&format!("[con_id={id}]"), "focus"])?;
        Ok(())
    }

    fn set_border_colors(&self, _: &str, _: &str, _: &str) -> Result<(), CompositorError> {
        Err(CompositorError::BorderColorsUnsupported)
    }
}

#[cfg(test)]
mod tests;
