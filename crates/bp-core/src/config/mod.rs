//! Port of internal/config/config.go: machine-local blueprint configuration.
//!
//! [`load`] resolves `BP_HOME` (or `/etc/blueprint/home`, or
//! `~/.blueprint`), reads at most one of `config.yaml`, `config.yml`,
//! `config.json` and applies it over the defaults. YAML is strict (unknown
//! keys, duplicate keys and extra documents are errors); an invalid
//! `config.json` falls back to the defaults with [`Config::invalid_config`]
//! set, exactly like Go.

mod color;
mod decode;
pub mod p2p;
mod remotes;
mod setup;
pub(crate) mod tree;

#[cfg(test)]
mod tests;

use std::collections::BTreeMap;
use std::io::{self, Write};

use serde::{Serialize, Serializer};

use crate::gopath;

pub use color::color_index;
pub use remotes::update_remote;
pub use setup::{EXAMPLE_YAML, init_yaml};

/// Go `config.LegacyHome`.
pub const LEGACY_HOME: &str = "/srv/blueprint";

/// A config error; the text matches Go's `error.Error()` where the port can
/// reproduce it.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct Error(pub String);

impl From<io::Error> for Error {
    fn from(err: io::Error) -> Self {
        Error(go_io_error(&err))
    }
}

/// Renders an I/O error close to Go's `*PathError` text (without the path).
pub(crate) fn go_io_error(err: &io::Error) -> String {
    match err.kind() {
        io::ErrorKind::NotFound => "no such file or directory".to_string(),
        io::ErrorKind::PermissionDenied => "permission denied".to_string(),
        _ => {
            let text = err.to_string();
            match text.find(" (os error") {
                Some(i) => {
                    let mut s = text[..i].to_string();
                    if let Some(first) = s.get(..1) {
                        let lower = first.to_lowercase();
                        s.replace_range(..1, &lower);
                    }
                    s
                }
                None => text,
            }
        }
    }
}

fn nil_if_empty<S: Serializer>(list: &[String], serializer: S) -> Result<S::Ok, S::Error> {
    if list.is_empty() {
        serializer.serialize_none()
    } else {
        list.serialize(serializer)
    }
}

/// A Go `[]string` that was built with `append(nil, ...)`: empty encodes as
/// `null`.
struct NilList<'a>(&'a [String]);

impl Serialize for NilList<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        nil_if_empty(self.0, serializer)
    }
}

fn nil_list_map<S: Serializer>(map: &BTreeMap<String, Vec<String>>, serializer: S) -> Result<S::Ok, S::Error> {
    use serde::ser::SerializeMap;
    let mut out = serializer.serialize_map(Some(map.len()))?;
    for (key, value) in map {
        out.serialize_entry(key, &NilList(value))?;
    }
    out.end()
}

/// Go `config.Config`. JSON field names, order and `omitempty` match Go;
/// empty lists encode as `null` because Go builds them with `append(nil, …)`.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Config {
    pub update_check: bool,
    pub local_mouse: bool,
    pub local_observation: bool,
    /// The config file that was read; empty when none exists.
    #[serde(skip)]
    pub path: String,
    #[serde(skip)]
    pub home: String,
    #[serde(skip)]
    pub legacy: bool,
    pub msgq_root: String,
    #[serde(serialize_with = "nil_if_empty")]
    pub agentbooks: Vec<String>,
    #[serde(serialize_with = "nil_if_empty")]
    pub token_agentbooks: Vec<String>,
    pub state_dir: String,
    pub wa_outbox: String,
    pub wa_store: String,
    pub usage_bin: String,
    pub usage_history: String,
    pub clipboard_dir: String,
    pub wa_bridge: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ntfy: Option<NtfyConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fed: Option<FedConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub p2p: Option<p2p::Config>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub codex: Option<CodexConfig>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty", serialize_with = "nil_list_map")]
    pub cli_updates: BTreeMap<String, Vec<String>>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub remotes: BTreeMap<String, RemoteConfig>,
    pub bar: BarConfig,
    pub lifecycle: LifecycleConfig,
    pub windows: WindowsConfig,
    pub claude_accounts: ClaudeAccountsConfig,
    /// Non-empty when `config.json` could not be parsed and the defaults
    /// are in use; holds the parse error.
    #[serde(skip)]
    pub invalid_config: String,
}

impl Config {
    /// Go `Config.CodexDisabled`.
    pub fn codex_disabled(&self) -> bool {
        self.codex.as_ref().is_some_and(|c| c.disabled)
    }
}

/// Go `ntfy.Config`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct NtfyConfig {
    pub url: String,
    pub topic: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub token: String,
}

/// Go `config.RemoteConfig`: an interactive bp host. It deliberately holds
/// no secrets; `identity` is only a path to an SSH identity file.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RemoteConfig {
    pub host: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub port: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub user: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub identity: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub transport: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mosh_ports: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub elevate: String,
}

fn is_zero(v: &i64) -> bool {
    *v == 0
}

/// Go `config.LifecycleConfig`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LifecycleConfig {
    pub ephemeral_default: bool,
    pub archive_on_close: bool,
}

/// Go `config.ClaudeAccountsConfig`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ClaudeAccountsConfig {
    pub auto_switch: bool,
    pub threshold: i64,
    pub cooldown_minutes: i64,
    pub poll_minutes: i64,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub limits: BTreeMap<String, i64>,
    pub keep_alive: bool,
    pub keep_alive_model: String,
}

impl ClaudeAccountsConfig {
    /// Go `LimitPercents`: `None` when no account has its own limit.
    pub fn limit_percents(&self) -> Option<BTreeMap<String, f64>> {
        if self.limits.is_empty() {
            return None;
        }
        Some(self.limits.iter().map(|(k, v)| (k.clone(), *v as f64)).collect())
    }
}

impl Default for ClaudeAccountsConfig {
    fn default() -> Self {
        default_claude_accounts()
    }
}

/// Go `DefaultClaudeAccounts`.
pub fn default_claude_accounts() -> ClaudeAccountsConfig {
    ClaudeAccountsConfig {
        auto_switch: false,
        threshold: 90,
        cooldown_minutes: 5,
        poll_minutes: 5,
        limits: BTreeMap::new(),
        keep_alive: false,
        keep_alive_model: "haiku".to_string(),
    }
}

/// Go `config.BarConfig`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BarConfig {
    pub default_color: String,
    pub context: String,
    #[serde(serialize_with = "nil_if_empty")]
    pub widgets: Vec<String>,
}

/// Go `config.WindowsConfig`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowsConfig {
    pub reset_color: String,
}

/// Go `config.CodexConfig`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct CodexConfig {
    #[serde(serialize_with = "nil_if_empty")]
    pub sockets: Vec<String>,
    /// Stops bp from starting new Codex sessions.
    pub disabled: bool,
}

/// Go `config.FedConfig`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct FedConfig {
    pub mode: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub listen: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hub: String,
    pub peer_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub token: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub expose: Vec<String>,
}

/// Injectable process environment for [`load_with`] (Go `loadWith`).
pub struct LoadEnv<'a> {
    /// `os.Getenv`; an unset variable is `""`.
    pub getenv: &'a dyn Fn(&str) -> String,
    /// `os.UserHomeDir`.
    pub user_home: &'a dyn Fn() -> Result<String, String>,
    /// `os.ReadFile`; a missing file must be `ErrorKind::NotFound`.
    pub read_file: &'a dyn Fn(&str) -> io::Result<Vec<u8>>,
}

/// Go `os.UserHomeDir` on Unix.
pub fn user_home_dir() -> Result<String, String> {
    match std::env::var_os("HOME") {
        Some(home) if !home.is_empty() => Ok(home.to_string_lossy().into_owned()),
        _ => Err("$HOME is not defined".to_string()),
    }
}

fn system_getenv(key: &str) -> String {
    std::env::var_os(key)
        .map(|v| v.to_string_lossy().into_owned())
        .unwrap_or_default()
}

fn system_read(path: &str) -> io::Result<Vec<u8>> {
    std::fs::read(path)
}

/// Go `config.Load`: the process environment, warnings to stderr.
pub fn load() -> Result<Config, Error> {
    let env = LoadEnv {
        getenv: &system_getenv,
        user_home: &user_home_dir,
        read_file: &system_read,
    };
    let mut stderr = io::stderr();
    load_with(&env, Some(&mut stderr))
}

/// Go `loadWithWarning`.
pub fn load_with(env: &LoadEnv<'_>, warning: Option<&mut dyn Write>) -> Result<Config, Error> {
    let mut home = (env.getenv)("BP_HOME");
    if home.is_empty() {
        // A server installation explicitly selects its home. Merely cloning
        // the repository under /srv/blueprint must never enable server
        // integrations.
        match (env.read_file)("/etc/blueprint/home") {
            Ok(data) => {
                home = crate::text::go_trim_space(&String::from_utf8_lossy(&data)).to_string();
                if !gopath::is_abs(&home) || home.contains(['\r', '\n', '\0']) {
                    return Err(Error(
                        "invalid /etc/blueprint/home: expected one absolute directory".to_string(),
                    ));
                }
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                let user = (env.user_home)().map_err(|e| Error(format!("find home directory: {e}")))?;
                home = gopath::join2(&user, ".blueprint");
            }
            Err(err) => {
                return Err(Error(format!("read /etc/blueprint/home: {}", go_io_error(&err))));
            }
        }
    }

    let home = gopath::clean(&home);
    let legacy = home == LEGACY_HOME;
    let mut result = defaults(&home, legacy);

    let mut found: Option<(String, Vec<u8>)> = None;
    for name in ["config.yaml", "config.yml", "config.json"] {
        let candidate = gopath::join2(&home, name);
        match (env.read_file)(&candidate) {
            Err(err) if err.kind() == io::ErrorKind::NotFound => continue,
            Err(err) => {
                return Err(Error(format!("read {candidate}: {}", go_io_error(&err))));
            }
            Ok(content) => {
                if let Some((path, _)) = &found {
                    return Err(Error(format!(
                        "multiple bp configs: {path} and {candidate}; keep one active file (no implicit merging)"
                    )));
                }
                found = Some((candidate, content));
            }
        }
    }
    let Some((path, data)) = found else {
        return Ok(result);
    };
    result.path = path.clone();
    let is_json = gopath::ext(&path) == ".json";
    let values = if !is_json {
        decode::overrides_yaml(&data).map_err(|e| Error(format!("parse {path}: {e}")))?
    } else {
        match decode::overrides_json(&data) {
            Ok(values) => values,
            Err(err) => {
                let parse_err = format!("parse {path}: {err}");
                if let Some(w) = warning {
                    let _ = writeln!(w, "config.json is invalid, falling back to defaults: {parse_err}");
                }
                result.invalid_config = parse_err;
                return Ok(result);
            }
        }
    };
    apply(&mut result, values);
    if !result.bar.default_color.is_empty() {
        color_index(&result.bar.default_color).map_err(|e| Error(format!("bar.defaultColor: {e}")))?;
    }
    color_index(&result.windows.reset_color).map_err(|e| Error(format!("windows.resetColor: {e}")))?;
    if !is_json {
        let user = (env.user_home)().map_err(|e| Error(format!("find home directory: {e}")))?;
        resolve_paths(&mut result, &user);
        if result.bar.context != "used" && result.bar.context != "remaining" {
            return Err(Error("bar.context must be used or remaining".to_string()));
        }
        for widget in &result.bar.widgets {
            match widget.as_str() {
                "ctx" | "temp" | "queue" | "model" | "quota" | "talk" | "clock" => {}
                _ => {
                    return Err(Error(format!(
                        "parse {path}: unknown bar widget {}",
                        crate::gojson::time::go_quote(widget)
                    )));
                }
            }
        }
    }
    validate_fed(result.fed.as_ref()).map_err(|e| Error(format!("parse {path}: fed: {e}")))?;
    if let Some(p2p) = &result.p2p {
        p2p.validate().map_err(|e| Error(format!("parse {path}: p2p: {e}")))?;
    }
    validate_remotes(&result.remotes).map_err(|e| Error(format!("parse {path}: remotes: {e}")))?;
    validate_claude_accounts(&result.claude_accounts)
        .map_err(|e| Error(format!("parse {path}: claudeAccounts: {e}")))?;
    Ok(result)
}

/// YAML paths are relative to BP_HOME, never to the current directory. Only
/// `~/` is expanded; values are never executed as shell text.
fn resolve_paths(c: &mut Config, user: &str) {
    let home = c.home.clone();
    let resolve = |p: &str| -> String {
        if p.is_empty() {
            String::new()
        } else if let Some(rest) = p.strip_prefix("~/") {
            gopath::join2(user, rest)
        } else if gopath::is_abs(p) {
            gopath::clean(p)
        } else {
            gopath::join2(&home, p)
        }
    };
    for p in [
        &mut c.msgq_root,
        &mut c.state_dir,
        &mut c.wa_outbox,
        &mut c.wa_store,
        &mut c.usage_bin,
        &mut c.usage_history,
        &mut c.clipboard_dir,
    ] {
        *p = resolve(p);
    }
    for list in [&mut c.agentbooks, &mut c.token_agentbooks] {
        for p in list.iter_mut() {
            *p = resolve(p);
        }
    }
    if let Some(codex) = &mut c.codex {
        for p in codex.sockets.iter_mut() {
            *p = resolve(p);
        }
    }
    for remote in c.remotes.values_mut() {
        remote.identity = resolve(&remote.identity);
    }
}

/// Go `defaults(home, legacy)`.
pub fn defaults(home: &str, legacy: bool) -> Config {
    let strings = |items: &[&str]| items.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    let bar = BarConfig {
        default_color: String::new(),
        context: "used".to_string(),
        widgets: strings(&["ctx", "temp", "queue", "model", "quota"]),
    };
    let lifecycle = LifecycleConfig {
        ephemeral_default: true,
        archive_on_close: true,
    };
    let windows = WindowsConfig {
        reset_color: "white".to_string(),
    };
    let base = Config {
        update_check: true,
        local_mouse: true,
        local_observation: true,
        path: String::new(),
        home: home.to_string(),
        legacy: false,
        msgq_root: gopath::join2(home, "msgq"),
        agentbooks: vec![gopath::join2(home, "agentbook.json")],
        token_agentbooks: vec![gopath::join2(home, "agentbook.json")],
        state_dir: gopath::join2(home, "state"),
        wa_outbox: String::new(),
        wa_store: String::new(),
        usage_bin: String::new(),
        usage_history: String::new(),
        clipboard_dir: String::new(),
        wa_bridge: false,
        ntfy: None,
        fed: None,
        p2p: None,
        codex: None,
        cli_updates: default_cli_updates(),
        remotes: BTreeMap::new(),
        bar,
        lifecycle,
        windows,
        claude_accounts: default_claude_accounts(),
        invalid_config: String::new(),
    };
    if !legacy {
        return base;
    }
    Config {
        legacy: true,
        msgq_root: "/srv/server-main/msgq".to_string(),
        // One book for the whole fleet. Several books still work when the
        // config lists them; the default no longer assumes any.
        agentbooks: strings(&["/srv/server-main/agentbook.json"]),
        token_agentbooks: strings(&["/srv/server-main/agentbook.json"]),
        state_dir: "/srv/blueprint/state".to_string(),
        wa_outbox: "/srv/whatsapp/outbox".to_string(),
        wa_store: "/srv/whatsapp/messages.jsonl".to_string(),
        usage_bin: "/srv/server-main/bin".to_string(),
        usage_history: "/srv/server-main/usage/history.jsonl".to_string(),
        clipboard_dir: "/srv/server-main/clipboard".to_string(),
        wa_bridge: true,
        ..base
    }
}

fn default_cli_updates() -> BTreeMap<String, Vec<String>> {
    let entry = |k: &str, v: &[&str]| (k.to_string(), v.iter().map(|s| s.to_string()).collect());
    BTreeMap::from([
        entry("claude", &["claude", "update"]),
        entry("codex", &["npm", "install", "-g", "@openai/codex@latest"]),
        entry("opencode", &["opencode", "upgrade"]),
        entry("hermes", &["hermes", "update"]),
    ])
}

fn apply(result: &mut Config, values: decode::Overrides) {
    if let Some(lifecycle) = &values.lifecycle {
        if let Some(v) = lifecycle.ephemeral_default {
            result.lifecycle.ephemeral_default = v;
        }
        if let Some(v) = lifecycle.archive_on_close {
            result.lifecycle.archive_on_close = v;
        }
    }
    if let Some(v) = values.bar.as_ref().and_then(|b| b.default_color.clone()) {
        result.bar.default_color = v;
    }
    if let Some(v) = values.update_check {
        result.update_check = v;
    }
    if let Some(v) = values.local_mouse {
        result.local_mouse = v;
    }
    if let Some(v) = values.local_observation {
        result.local_observation = v;
    }
    if let Some(v) = values.msgq_root {
        result.msgq_root = v;
    }
    if let Some(books) = &values.agentbooks {
        result.agentbooks = books.clone();
        if !result.legacy && values.token_agentbooks.is_none() {
            result.token_agentbooks = books.clone();
        }
    }
    if let Some(v) = values.token_agentbooks {
        result.token_agentbooks = v;
    }
    for (slot, value) in [
        (&mut result.state_dir, values.state_dir),
        (&mut result.wa_outbox, values.wa_outbox),
        (&mut result.wa_store, values.wa_store),
        (&mut result.usage_bin, values.usage_bin),
        (&mut result.usage_history, values.usage_history),
        (&mut result.clipboard_dir, values.clipboard_dir),
    ] {
        if let Some(v) = value {
            *slot = v;
        }
    }
    if let Some(v) = values.wa_bridge {
        result.wa_bridge = v;
    }
    if values.ntfy.is_some() {
        result.ntfy = values.ntfy;
    }
    if values.fed.is_some() {
        result.fed = values.fed;
    }
    if values.p2p.is_some() {
        result.p2p = values.p2p;
    }
    if values.codex.is_some() {
        result.codex = values.codex;
    }
    if let Some(remotes) = values.remotes {
        result.remotes = remotes
            .into_iter()
            .map(|(name, mut remote)| {
                if remote.transport.is_empty() {
                    remote.transport = "ssh".to_string();
                }
                (name, remote)
            })
            .collect();
    }
    if let Some(updates) = values.cli_updates {
        result.cli_updates.extend(updates);
    }
    if let Some(bar) = values.bar {
        if let Some(v) = bar.context {
            result.bar.context = v;
        }
        if let Some(v) = bar.widgets {
            result.bar.widgets = v;
        }
    }
    if let Some(accounts) = values.claude_accounts {
        let target = &mut result.claude_accounts;
        if let Some(v) = accounts.auto_switch {
            target.auto_switch = v;
        }
        if let Some(v) = accounts.threshold {
            target.threshold = v;
        }
        if let Some(v) = accounts.cooldown_minutes {
            target.cooldown_minutes = v;
        }
        if let Some(v) = accounts.poll_minutes {
            target.poll_minutes = v;
        }
        if let Some(v) = accounts.limits {
            target.limits = v;
        }
        if let Some(v) = accounts.keep_alive {
            target.keep_alive = v;
        }
        if let Some(v) = accounts.keep_alive_model {
            target.keep_alive_model = v;
        }
    }
    if let Some(windows) = values.windows {
        result.windows = windows;
        if result.windows.reset_color.is_empty() {
            result.windows.reset_color = "white".to_string();
        }
    }
}

fn validate_claude_accounts(value: &ClaudeAccountsConfig) -> Result<(), String> {
    use crate::text::go_trim_space;
    if !(1..=100).contains(&value.threshold) {
        return Err("threshold must be between 1 and 100".to_string());
    }
    if value.cooldown_minutes < 1 {
        return Err("cooldownMinutes must be at least 1".to_string());
    }
    if value.poll_minutes < 1 {
        return Err("pollMinutes must be at least 1".to_string());
    }
    for (key, limit) in &value.limits {
        if go_trim_space(key).is_empty() {
            return Err("limits keys must name a slot number, alias or email".to_string());
        }
        if !(1..=100).contains(limit) {
            return Err(format!(
                "limits[{}] must be between 1 and 100",
                crate::gojson::time::go_quote(key)
            ));
        }
    }
    let model = go_trim_space(&value.keep_alive_model);
    if model.is_empty() || model.starts_with('-') {
        return Err("keepAliveModel must name a Claude model".to_string());
    }
    Ok(())
}

fn validate_fed(value: Option<&FedConfig>) -> Result<(), String> {
    let Some(value) = value else {
        return Ok(());
    };
    if !valid_federation_name(&value.peer_name) {
        return Err(
            "peerName must contain only A-Z, a-z, 0-9, '.', '_' or '-' and be at most 64 characters"
                .to_string(),
        );
    }
    match value.mode.as_str() {
        "hub" => {
            if value.listen.is_empty() {
                return Err("listen is required in hub mode".to_string());
            }
            let (host, _) =
                split_host_port(&value.listen).map_err(|e| format!("invalid listen address: {e}"))?;
            if host != "localhost" && !p2p::parse_ip(&host).is_some_and(|ip| ip.is_loopback() || is_mapped_loopback(&ip)) {
                return Err("listen must use a loopback address".to_string());
            }
        }
        "client" => {
            if value.hub.is_empty() {
                return Err("hub is required in client mode".to_string());
            }
            let parsed = parse_url(&value.hub);
            let Some((scheme, hostname, host_present)) = parsed else {
                return Err("hub must be an http or https URL".to_string());
            };
            if (scheme != "http" && scheme != "https") || !host_present {
                return Err("hub must be an http or https URL".to_string());
            }
            if scheme == "http" {
                let host = hostname.to_lowercase();
                if host != "localhost"
                    && !p2p::parse_ip(&host).is_some_and(|ip| ip.is_loopback() || is_mapped_loopback(&ip))
                {
                    return Err("refusing to send bearer token over plaintext HTTP".to_string());
                }
            }
            if value.token.len() != 64 || !value.token.bytes().all(|b| b.is_ascii_hexdigit()) {
                return Err("token must be 64 hexadecimal characters".to_string());
            }
            for name in &value.expose {
                if !valid_federation_name(name) {
                    return Err(format!(
                        "expose contains invalid agent name {}",
                        crate::gojson::time::go_quote(name)
                    ));
                }
            }
        }
        _ => return Err("mode must be hub or client".to_string()),
    }
    Ok(())
}

/// Go `net.IP.IsLoopback` also accepts IPv4-mapped `::ffff:127.x.y.z`.
fn is_mapped_loopback(ip: &std::net::IpAddr) -> bool {
    match ip {
        std::net::IpAddr::V6(v6) => v6.to_ipv4_mapped().is_some_and(|v4| v4.is_loopback()),
        std::net::IpAddr::V4(_) => false,
    }
}

/// Go `net.SplitHostPort`.
pub(crate) fn split_host_port(hostport: &str) -> Result<(String, String), String> {
    let addr_err = |why: &str| format!("address {hostport}: {why}");
    let missing_port = "missing port in address";
    let too_many = "too many colons in address";
    let Some(i) = hostport.rfind(':') else {
        return Err(addr_err(missing_port));
    };
    let (host, j, k);
    if hostport.starts_with('[') {
        let Some(end) = hostport.find(']') else {
            return Err(addr_err("missing ']' in address"));
        };
        if end + 1 == hostport.len() {
            return Err(addr_err(missing_port));
        } else if end + 1 != i {
            if hostport.as_bytes()[end + 1] == b':' {
                return Err(addr_err(too_many));
            }
            return Err(addr_err(missing_port));
        }
        host = hostport[1..end].to_string();
        j = 1;
        k = end + 1;
    } else {
        host = hostport[..i].to_string();
        if host.contains(':') {
            return Err(addr_err(too_many));
        }
        j = 0;
        k = 0;
    }
    if hostport[j..].contains('[') {
        return Err(addr_err("unexpected '[' in address"));
    }
    if hostport[k..].contains(']') {
        return Err(addr_err("unexpected ']' in address"));
    }
    Ok((host, hostport[i + 1..].to_string()))
}

/// The parts of Go `url.Parse` the hub check needs: `(scheme lowercased,
/// Hostname(), Host != "")`, or `None` where Go returns an error.
fn parse_url(raw: &str) -> Option<(String, String, bool)> {
    if raw.bytes().any(|b| b < 0x20 || b == 0x7f) {
        return None;
    }
    let raw = raw.split('#').next().unwrap_or("");
    // getScheme
    let mut scheme = "";
    let mut rest = raw;
    for (i, c) in raw.char_indices() {
        if c.is_ascii_alphabetic() {
            continue;
        }
        if c.is_ascii_digit() || c == '+' || c == '-' || c == '.' {
            if i == 0 {
                break;
            }
            continue;
        }
        if c == ':' {
            if i == 0 {
                return None; // "missing protocol scheme"
            }
            scheme = &raw[..i];
            rest = &raw[i + 1..];
        }
        break;
    }
    let scheme = scheme.to_ascii_lowercase();
    let rest = rest.split('?').next().unwrap_or("");
    let Some(after) = rest.strip_prefix("//") else {
        return Some((scheme, String::new(), false));
    };
    let authority = match after.find('/') {
        Some(i) => &after[..i],
        None => after,
    };
    let host = match authority.rfind('@') {
        Some(i) => &authority[i + 1..],
        None => authority,
    };
    // Validate the port like Go's parseHost/validOptionalPort.
    let (hostname, port) = if let Some(stripped) = host.strip_prefix('[') {
        let end = stripped.find(']')?;
        let after_bracket = &stripped[end + 1..];
        if !after_bracket.is_empty() && !after_bracket.starts_with(':') {
            return None;
        }
        (stripped[..end].to_string(), after_bracket)
    } else {
        match host.rfind(':') {
            Some(i) => (host[..i].to_string(), &host[i..]),
            None => (host.to_string(), ""),
        }
    };
    if let Some(digits) = port.strip_prefix(':')
        && !digits.bytes().all(|b| b.is_ascii_digit())
    {
        return None;
    }
    Some((scheme, hostname, !host.is_empty()))
}

/// Go `validFederationName`.
pub fn valid_federation_name(value: &str) -> bool {
    if value.is_empty() || value == "." || value == ".." || value.len() > 64 {
        return false;
    }
    value
        .bytes()
        .all(|c| c.is_ascii_alphanumeric() || c == b'.' || c == b'_' || c == b'-')
}

/// Go `validateRemotes` (entries checked in name order).
pub(crate) fn validate_remotes(remotes: &BTreeMap<String, RemoteConfig>) -> Result<(), String> {
    for (name, remote) in remotes {
        if !valid_federation_name(name) {
            return Err(format!("invalid server name {}", crate::gojson::time::go_quote(name)));
        }
        if remote.host.is_empty()
            || remote.host.starts_with('-')
            || remote.host.contains([' ', '\t', '\r', '\n', '\0'])
        {
            return Err(format!("{name}.host must be one host name or address"));
        }
        if !(0..=65535).contains(&remote.port) {
            return Err(format!("{name}.port must be between 1 and 65535"));
        }
        if !remote.user.is_empty() && !valid_federation_name(&remote.user) {
            return Err(format!("{name}.user contains invalid characters"));
        }
        if remote.identity.contains(['\r', '\n', '\0']) {
            return Err(format!("{name}.identity contains invalid characters"));
        }
        let transport = if remote.transport.is_empty() {
            "ssh"
        } else {
            remote.transport.as_str()
        };
        if transport != "ssh" && transport != "mosh" {
            return Err(format!("{name}.transport must be mosh or ssh"));
        }
        if !remote.mosh_ports.is_empty() && !valid_port_range(&remote.mosh_ports) {
            return Err(format!("{name}.moshPorts must be a port or MIN:MAX range"));
        }
        if remote.elevate.contains(['\r', '\n', '\0']) {
            return Err(format!("{name}.elevate must be one command line"));
        }
        for field in crate::text::go_fields(&remote.elevate) {
            if !valid_command_word(field) {
                return Err(format!("{name}.elevate contains unsupported shell characters"));
            }
        }
    }
    Ok(())
}

/// Go `strconv.Atoi` for the decimal forms it accepts.
fn atoi(s: &str) -> Option<i64> {
    let digits = s.strip_prefix(['+', '-']).unwrap_or(s);
    if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    s.parse::<i64>().ok()
}

fn valid_port_range(value: &str) -> bool {
    let parts: Vec<&str> = value.split(':').collect();
    if parts.len() > 2 {
        return false;
    }
    let mut ports = Vec::new();
    for part in &parts {
        match atoi(part) {
            Some(port) if (1..=65535).contains(&port) => ports.push(port),
            _ => return false,
        }
    }
    ports.len() != 2 || ports[0] <= ports[1]
}

fn valid_command_word(value: &str) -> bool {
    !value.is_empty()
        && value
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "._/+:-=@".contains(c))
}
