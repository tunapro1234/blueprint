//! Port of the `overrides` decoding in internal/config/config.go.
//!
//! Go decodes the config file into `overrides`, a struct of pointers, so
//! "absent" (and `null`) differs from "set to the zero value". The readers
//! below fill [`Overrides`] from a [`Val`] tree with yaml.v3 or
//! encoding/json rules (see [`super::tree`]).

use std::collections::BTreeMap;

use super::tree::{Ctx, Format, R, Val, as_bool, as_int, as_map, as_string, as_string_list, struct_key};
use super::{CodexConfig, FedConfig, NtfyConfig, RemoteConfig, WindowsConfig, p2p};

#[derive(Debug, Default)]
pub(crate) struct Overrides {
    pub update_check: Option<bool>,
    pub local_mouse: Option<bool>,
    pub local_observation: Option<bool>,
    pub msgq_root: Option<String>,
    pub agentbooks: Option<Vec<String>>,
    pub token_agentbooks: Option<Vec<String>>,
    pub state_dir: Option<String>,
    pub wa_outbox: Option<String>,
    pub wa_store: Option<String>,
    pub usage_bin: Option<String>,
    pub usage_history: Option<String>,
    pub clipboard_dir: Option<String>,
    pub wa_bridge: Option<bool>,
    pub ntfy: Option<NtfyConfig>,
    pub fed: Option<FedConfig>,
    pub p2p: Option<p2p::Config>,
    pub codex: Option<CodexConfig>,
    pub remotes: Option<BTreeMap<String, RemoteConfig>>,
    pub cli_updates: Option<BTreeMap<String, Vec<String>>>,
    pub bar: Option<BarOverrides>,
    pub lifecycle: Option<LifecycleOverrides>,
    pub windows: Option<WindowsConfig>,
    pub claude_accounts: Option<ClaudeAccountsOverrides>,
}

#[derive(Debug, Default)]
pub(crate) struct ClaudeAccountsOverrides {
    pub auto_switch: Option<bool>,
    pub threshold: Option<i64>,
    pub cooldown_minutes: Option<i64>,
    pub poll_minutes: Option<i64>,
    pub limits: Option<BTreeMap<String, i64>>,
    pub keep_alive: Option<bool>,
    pub keep_alive_model: Option<String>,
}

#[derive(Debug, Default)]
pub(crate) struct LifecycleOverrides {
    pub ephemeral_default: Option<bool>,
    pub archive_on_close: Option<bool>,
}

#[derive(Debug, Default)]
pub(crate) struct BarOverrides {
    pub default_color: Option<String>,
    pub context: Option<String>,
    pub widgets: Option<Vec<String>>,
}

/// Parses a YAML config like `yaml.NewDecoder` + `KnownFields(true)` +
/// a second `Decode` that must hit EOF.
pub(crate) fn overrides_yaml(data: &[u8]) -> R<Overrides> {
    let text = std::str::from_utf8(data).map_err(|_| "yaml: invalid leading UTF-8 octet".to_string())?;
    let value = parse_yaml(text)?;
    let ctx = Ctx {
        format: Format::Yaml,
        type_name: "config.overrides",
        path: String::new(),
    };
    read_overrides(&value, &ctx)
}

/// Parses one YAML document into a [`Val`]; errors for syntax errors,
/// duplicate keys and multiple documents.
pub(crate) fn parse_yaml(text: &str) -> R<Val> {
    match serde_norway::from_str::<serde_norway::Value>(text) {
        Ok(value) => Ok(Val::from_yaml(value)),
        Err(err) => {
            let message = err.to_string();
            if message.contains("more than one document") {
                Err("expected a single YAML document".to_string())
            } else {
                Err(format!("yaml: {message}"))
            }
        }
    }
}

/// Parses a JSON config like `json.Unmarshal(data, &overrides)`.
pub(crate) fn overrides_json(data: &[u8]) -> R<Overrides> {
    let node = crate::gojson::parse_node(data).map_err(|e| e.to_string())?;
    let value = Val::from_node(node);
    let ctx = Ctx {
        format: Format::Json,
        type_name: "config.overrides",
        path: String::new(),
    };
    read_overrides(&value, &ctx)
}

/// Walks the fields of a struct value, calling `set` for each recognised key
/// (in source order) with the field's context.
fn each_field(
    v: &Val,
    ctx: &Ctx,
    type_name: &'static str,
    names: &[&'static str],
    mut set: impl FnMut(&'static str, &Val, &Ctx) -> R<()>,
) -> R<()> {
    let sctx = Ctx {
        format: ctx.format,
        type_name,
        path: ctx.path.clone(),
    };
    let entries = as_map(v, &sctx, type_name)?;
    for (key, value) in entries {
        if let Some(field) = struct_key(key, names, &sctx)? {
            set(field, value, &sctx.field(type_name, field))?;
        }
    }
    Ok(())
}

/// A pointer field: `null` clears it, anything else decodes into it.
fn ptr<T>(slot: &mut Option<T>, v: &Val, ctx: &Ctx, read: impl FnOnce(&Val, &Ctx) -> R<T>) -> R<()> {
    *slot = match v {
        Val::Null => None,
        _ => Some(read(v, ctx)?),
    };
    Ok(())
}

/// A pointer-to-struct or pointer-to-map field: a repeated JSON key merges
/// into the existing value like Go.
fn ptr_merge<T: Default>(
    slot: &mut Option<T>,
    v: &Val,
    ctx: &Ctx,
    read: impl FnOnce(&mut T, &Val, &Ctx) -> R<()>,
) -> R<()> {
    if matches!(v, Val::Null) {
        *slot = None;
        return Ok(());
    }
    read(slot.get_or_insert_with(T::default), v, ctx)
}

/// A plain (non-pointer) field: `null` keeps the current value.
fn plain<T>(slot: &mut T, v: &Val, ctx: &Ctx, read: impl FnOnce(&Val, &Ctx) -> R<T>) -> R<()> {
    if !matches!(v, Val::Null) {
        *slot = read(v, ctx)?;
    }
    Ok(())
}

fn read_overrides(v: &Val, ctx: &Ctx) -> R<Overrides> {
    let mut out = Overrides::default();
    if matches!(v, Val::Null) {
        return Ok(out);
    }
    const NAMES: &[&str] = &[
        "updateCheck",
        "localMouse",
        "localObservation",
        "msgqRoot",
        "agentbooks",
        "tokenAgentbooks",
        "stateDir",
        "waOutbox",
        "waStore",
        "usageBin",
        "usageHistory",
        "clipboardDir",
        "waBridge",
        "ntfy",
        "fed",
        "p2p",
        "codex",
        "remotes",
        "cliUpdates",
        "bar",
        "lifecycle",
        "windows",
        "claudeAccounts",
    ];
    each_field(v, ctx, "config.overrides", NAMES, |field, v, c| match field {
        "updateCheck" => ptr(&mut out.update_check, v, c, as_bool),
        "localMouse" => ptr(&mut out.local_mouse, v, c, as_bool),
        "localObservation" => ptr(&mut out.local_observation, v, c, as_bool),
        "msgqRoot" => ptr(&mut out.msgq_root, v, c, as_string),
        "agentbooks" => ptr(&mut out.agentbooks, v, c, as_string_list),
        "tokenAgentbooks" => ptr(&mut out.token_agentbooks, v, c, as_string_list),
        "stateDir" => ptr(&mut out.state_dir, v, c, as_string),
        "waOutbox" => ptr(&mut out.wa_outbox, v, c, as_string),
        "waStore" => ptr(&mut out.wa_store, v, c, as_string),
        "usageBin" => ptr(&mut out.usage_bin, v, c, as_string),
        "usageHistory" => ptr(&mut out.usage_history, v, c, as_string),
        "clipboardDir" => ptr(&mut out.clipboard_dir, v, c, as_string),
        "waBridge" => ptr(&mut out.wa_bridge, v, c, as_bool),
        "ntfy" => ptr_merge(&mut out.ntfy, v, c, read_ntfy),
        "fed" => ptr_merge(&mut out.fed, v, c, read_fed),
        "p2p" => ptr_merge(&mut out.p2p, v, c, read_p2p),
        "codex" => ptr_merge(&mut out.codex, v, c, read_codex),
        "remotes" => ptr_merge(&mut out.remotes, v, c, read_remotes),
        "cliUpdates" => ptr_merge(&mut out.cli_updates, v, c, read_list_map),
        "bar" => ptr_merge(&mut out.bar, v, c, read_bar),
        "lifecycle" => ptr_merge(&mut out.lifecycle, v, c, read_lifecycle),
        "windows" => ptr_merge(&mut out.windows, v, c, read_windows),
        "claudeAccounts" => ptr_merge(&mut out.claude_accounts, v, c, read_claude_accounts),
        _ => Ok(()),
    })?;
    Ok(out)
}

fn read_ntfy(out: &mut NtfyConfig, v: &Val, ctx: &Ctx) -> R<()> {
    each_field(v, ctx, "ntfy.Config", &["url", "topic", "token"], |field, v, c| match field {
        "url" => plain(&mut out.url, v, c, as_string),
        "topic" => plain(&mut out.topic, v, c, as_string),
        _ => plain(&mut out.token, v, c, as_string),
    })
}

fn read_fed(out: &mut FedConfig, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &["mode", "listen", "hub", "peerName", "token", "expose"];
    each_field(v, ctx, "config.FedConfig", NAMES, |field, v, c| match field {
        "mode" => plain(&mut out.mode, v, c, as_string),
        "listen" => plain(&mut out.listen, v, c, as_string),
        "hub" => plain(&mut out.hub, v, c, as_string),
        "peerName" => plain(&mut out.peer_name, v, c, as_string),
        "token" => plain(&mut out.token, v, c, as_string),
        _ => plain(&mut out.expose, v, c, as_string_list),
    })
}

fn read_codex(out: &mut CodexConfig, v: &Val, ctx: &Ctx) -> R<()> {
    each_field(v, ctx, "config.CodexConfig", &["sockets", "disabled"], |field, v, c| match field {
        "sockets" => plain(&mut out.sockets, v, c, as_string_list),
        _ => plain(&mut out.disabled, v, c, as_bool),
    })
}

fn read_windows(out: &mut WindowsConfig, v: &Val, ctx: &Ctx) -> R<()> {
    each_field(v, ctx, "config.WindowsConfig", &["resetColor"], |_, v, c| {
        plain(&mut out.reset_color, v, c, as_string)
    })
}

fn read_bar(out: &mut BarOverrides, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &["defaultColor", "context", "widgets"];
    each_field(v, ctx, "config.barOverrides", NAMES, |field, v, c| match field {
        "defaultColor" => ptr(&mut out.default_color, v, c, as_string),
        "context" => ptr(&mut out.context, v, c, as_string),
        _ => ptr(&mut out.widgets, v, c, as_string_list),
    })
}

fn read_lifecycle(out: &mut LifecycleOverrides, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &["ephemeralDefault", "archiveOnClose"];
    each_field(v, ctx, "config.lifecycleOverrides", NAMES, |field, v, c| match field {
        "ephemeralDefault" => ptr(&mut out.ephemeral_default, v, c, as_bool),
        _ => ptr(&mut out.archive_on_close, v, c, as_bool),
    })
}

fn read_claude_accounts(out: &mut ClaudeAccountsOverrides, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &[
        "autoSwitch",
        "threshold",
        "cooldownMinutes",
        "pollMinutes",
        "limits",
        "keepAlive",
        "keepAliveModel",
    ];
    each_field(v, ctx, "config.claudeAccountsOverrides", NAMES, |field, v, c| match field {
        "autoSwitch" => ptr(&mut out.auto_switch, v, c, as_bool),
        "threshold" => ptr(&mut out.threshold, v, c, as_int),
        "cooldownMinutes" => ptr(&mut out.cooldown_minutes, v, c, as_int),
        "pollMinutes" => ptr(&mut out.poll_minutes, v, c, as_int),
        "limits" => ptr_merge(&mut out.limits, v, c, |map, v, c| {
            for (key, value) in as_map(v, c, "map[string]int")? {
                map.insert(key.clone(), as_int(value, c)?);
            }
            Ok(())
        }),
        "keepAlive" => ptr(&mut out.keep_alive, v, c, as_bool),
        _ => ptr(&mut out.keep_alive_model, v, c, as_string),
    })
}

fn read_list_map(out: &mut BTreeMap<String, Vec<String>>, v: &Val, ctx: &Ctx) -> R<()> {
    for (key, value) in as_map(v, ctx, "map[string][]string")? {
        out.insert(key.clone(), as_string_list(value, ctx)?);
    }
    Ok(())
}

pub(crate) fn read_remotes(out: &mut BTreeMap<String, RemoteConfig>, v: &Val, ctx: &Ctx) -> R<()> {
    for (key, value) in as_map(v, ctx, "map[string]config.RemoteConfig")? {
        let mut remote = RemoteConfig::default();
        read_remote(&mut remote, value, ctx)?;
        out.insert(key.clone(), remote);
    }
    Ok(())
}

fn read_remote(out: &mut RemoteConfig, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &["host", "port", "user", "identity", "transport", "moshPorts", "elevate"];
    each_field(v, ctx, "config.RemoteConfig", NAMES, |field, v, c| match field {
        "host" => plain(&mut out.host, v, c, as_string),
        "port" => plain(&mut out.port, v, c, as_int),
        "user" => plain(&mut out.user, v, c, as_string),
        "identity" => plain(&mut out.identity, v, c, as_string),
        "transport" => plain(&mut out.transport, v, c, as_string),
        "moshPorts" => plain(&mut out.mosh_ports, v, c, as_string),
        _ => plain(&mut out.elevate, v, c, as_string),
    })
}

fn read_p2p(out: &mut p2p::Config, v: &Val, ctx: &Ctx) -> R<()> {
    const NAMES: &[&str] = &["enabled", "listen", "advertise", "rendezvous", "relay", "mdns", "peers"];
    each_field(v, ctx, "p2p.Config", NAMES, |field, v, c| match field {
        "enabled" => plain(&mut out.enabled, v, c, as_bool),
        "listen" => plain(&mut out.listen, v, c, as_string_list),
        "advertise" => plain(&mut out.advertise, v, c, as_string_list),
        "rendezvous" => plain(&mut out.rendezvous, v, c, as_string_list),
        "relay" => plain(&mut out.relay, v, c, as_bool),
        "mdns" => plain(&mut out.mdns, v, c, as_bool),
        _ => {
            if matches!(v, Val::Null) {
                return Ok(());
            }
            for (key, value) in as_map(v, c, "map[string]p2p.Peer")? {
                let mut peer = p2p::Peer::default();
                read_peer(&mut peer, value, c)?;
                out.peers.insert(key.clone(), peer);
            }
            Ok(())
        }
    })
}

fn read_peer(out: &mut p2p::Peer, v: &Val, ctx: &Ctx) -> R<()> {
    each_field(v, ctx, "p2p.Peer", &["id", "addresses", "expose"], |field, v, c| match field {
        "id" => plain(&mut out.id, v, c, as_string),
        "addresses" => plain(&mut out.addresses, v, c, as_string_list),
        _ => plain(&mut out.expose, v, c, as_string_list),
    })
}
