//! Port of internal/config/{config,yaml,remotes,p2p}_test.go plus the Go
//! goldens in testdata/golden/core/{config,update_remote}.json.

use std::collections::BTreeMap;
use std::io;
use std::os::unix::fs::PermissionsExt;

use serde_json::Value;

use super::*;

const TEST_USER: &str = "/home/test-user";

fn golden(name: &str) -> Value {
    let path = format!("{}/../../testdata/golden/core/{name}", env!("CARGO_MANIFEST_DIR"));
    serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap()
}

fn b64(s: &str) -> Vec<u8> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = Vec::new();
    let mut acc = 0u32;
    let mut bits = 0;
    for b in s.bytes().filter(|b| *b != b'=') {
        acc = (acc << 6) | ALPHABET.iter().position(|a| *a == b).unwrap() as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    out
}

fn not_exist() -> io::Error {
    io::Error::from(io::ErrorKind::NotFound)
}

/// `Load()` with BP_HOME set to `home` and the user home injected.
fn load_home(home: &str) -> Result<Config, Error> {
    let getenv = |k: &str| if k == "BP_HOME" { home.to_string() } else { String::new() };
    let user = || Ok(TEST_USER.to_string());
    let read = |p: &str| std::fs::read(p);
    load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        None,
    )
}

/// Go `yamlConfig`: a fresh home holding one config file.
fn yaml_config(name: &str, data: &str) -> (tempfile::TempDir, Result<Config, Error>) {
    let home = tempfile::tempdir().unwrap();
    std::fs::write(home.path().join(name), data).unwrap();
    std::fs::set_permissions(home.path().join(name), std::fs::Permissions::from_mode(0o600)).unwrap();
    let result = load_home(home.path().to_str().unwrap());
    (home, result)
}

fn strings(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| s.to_string()).collect()
}

#[test]
fn legacy_mode_selection() {
    let getenv = |_: &str| String::new();
    let user = || -> Result<String, String> { panic!("must not be called") };
    let read = |p: &str| {
        if p == "/etc/blueprint/home" {
            Ok(format!("{LEGACY_HOME}\n").into_bytes())
        } else {
            Err(not_exist())
        }
    };
    let config = load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        None,
    )
    .unwrap();
    assert!(config.legacy && config.home == LEGACY_HOME, "{config:?}");
    assert_eq!(config.msgq_root, "/srv/server-main/msgq");
    assert_eq!(config.state_dir, "/srv/blueprint/state");
    assert!(config.wa_bridge);
    let want = strings(&["/srv/server-main/agentbook.json"]);
    assert_eq!(config.agentbooks, want);
    assert_eq!(config.token_agentbooks, want);
}

#[test]
fn config_overrides_legacy_defaults() {
    let data = r#"{
		"msgqRoot":"/q", "agentbooks":["/a.json"], "stateDir":"/state",
		"waOutbox":"", "waStore":"/wa.jsonl", "usageBin":"/bin",
		"usageHistory":"/history.jsonl", "clipboardDir":"/clips",
		"waBridge":false,
		"bar":{"widgets":["clock","queue","unknown"]},
		"ntfy":{"url":"https://ntfy.example","topic":"alerts","token":"secret"},
		"codex":{"sockets":["/run/codex.sock"]},
		"fed":{"mode":"client","hub":"https://hub.example","peerName":"portable",
		       "token":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}"#;
    let getenv = |k: &str| if k == "BP_HOME" { LEGACY_HOME.to_string() } else { String::new() };
    let user = || Ok(TEST_USER.to_string());
    let read = |p: &str| {
        if !p.ends_with("config.json") {
            return Err(not_exist());
        }
        assert_eq!(p, format!("{LEGACY_HOME}/config.json"));
        Ok(data.as_bytes().to_vec())
    };
    let c = load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        None,
    )
    .unwrap();
    assert_eq!(
        (
            c.msgq_root.as_str(),
            c.state_dir.as_str(),
            c.wa_outbox.as_str(),
            c.wa_store.as_str(),
            c.usage_bin.as_str()
        ),
        ("/q", "/state", "", "/wa.jsonl", "/bin")
    );
    assert_eq!(c.usage_history, "/history.jsonl");
    assert_eq!(c.clipboard_dir, "/clips");
    assert!(!c.wa_bridge);
    assert_eq!(c.agentbooks, strings(&["/a.json"]));
    let fed = c.fed.as_ref().unwrap();
    assert_eq!((fed.mode.as_str(), fed.peer_name.as_str()), ("client", "portable"));
    let ntfy = c.ntfy.as_ref().unwrap();
    assert_eq!(
        (ntfy.url.as_str(), ntfy.topic.as_str(), ntfy.token.as_str()),
        ("https://ntfy.example", "alerts", "secret")
    );
    assert_eq!(c.codex.as_ref().unwrap().sockets, strings(&["/run/codex.sock"]));
    assert_eq!(c.bar.widgets, strings(&["clock", "queue", "unknown"]));
    assert_eq!(c.token_agentbooks, strings(&["/srv/server-main/agentbook.json"]));
}

#[test]
fn non_legacy_defaults() {
    let getenv = |_: &str| String::new();
    let user = || Ok("/Users/example".to_string());
    let read = |_: &str| Err(not_exist());
    let c = load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        None,
    )
    .unwrap();
    assert!(!c.legacy);
    assert_eq!(c.msgq_root, "/Users/example/.blueprint/msgq");
    assert_eq!(c.state_dir, "/Users/example/.blueprint/state");
    assert_eq!(c.agentbooks, strings(&["/Users/example/.blueprint/agentbook.json"]));
    assert_eq!(c.token_agentbooks, c.agentbooks);
    assert!(c.wa_outbox.is_empty() && c.wa_store.is_empty() && c.usage_bin.is_empty());
    assert!(c.usage_history.is_empty() && c.clipboard_dir.is_empty() && !c.wa_bridge);
    assert!(c.fed.is_none() && c.ntfy.is_none() && c.codex.is_none());
    assert_eq!(c.bar.widgets, strings(&["ctx", "temp", "queue", "model", "quota"]));
    assert!(!c.bar.widgets.iter().any(|w| w == "talk" || w == "clock"));
}

#[test]
fn invalid_config_falls_back_to_defaults_with_warning() {
    let getenv = |k: &str| {
        if k == "BP_HOME" {
            "/tmp/bp-invalid-config-test".to_string()
        } else {
            String::new()
        }
    };
    let user = || Ok(TEST_USER.to_string());
    let read = |p: &str| {
        if p.ends_with("config.json") {
            Ok(br#"{"fed":"#.to_vec())
        } else {
            Err(not_exist())
        }
    };
    let mut warning = Vec::new();
    let c = load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        Some(&mut warning),
    )
    .unwrap();
    assert_eq!(c.home, "/tmp/bp-invalid-config-test");
    assert_eq!(c.state_dir, "/tmp/bp-invalid-config-test/state");
    assert!(!c.invalid_config.is_empty());
    let warning = String::from_utf8(warning).unwrap();
    assert!(
        warning.contains("config.json is invalid, falling back to defaults:"),
        "{warning}"
    );
}

#[test]
fn federation_config_requires_loopback_hub_listener() {
    let getenv = |k: &str| if k == "BP_HOME" { "/tmp/bp-test".to_string() } else { String::new() };
    let user = || Ok(TEST_USER.to_string());
    let read = |p: &str| {
        if p.ends_with("config.json") {
            Ok(br#"{"fed":{"mode":"hub","listen":"0.0.0.0:7877","peerName":"tuna"}}"#.to_vec())
        } else {
            Err(not_exist())
        }
    };
    let err = load_with(
        &LoadEnv {
            getenv: &getenv,
            user_home: &user,
            read_file: &read,
        },
        None,
    )
    .unwrap_err();
    assert!(err.to_string().contains("loopback"), "{err}");
}

#[test]
fn machine_selector_is_explicit_and_bp_home_wins() {
    for (name, override_home, selector, bad) in [
        ("local override", "/home/user/personal-bp", LEGACY_HOME, false),
        ("relative selector", "", "relative/path", true),
        ("empty selector", "", "", true),
        ("multiline selector", "", "/first\n/second", true),
    ] {
        let getenv = |k: &str| if k == "BP_HOME" { override_home.to_string() } else { String::new() };
        let user = || Ok("/home/user".to_string());
        let read = |p: &str| {
            if p == "/etc/blueprint/home" {
                Ok(selector.as_bytes().to_vec())
            } else {
                Err(not_exist())
            }
        };
        let result = load_with(
            &LoadEnv {
                getenv: &getenv,
                user_home: &user,
                read_file: &read,
            },
            None,
        );
        if bad {
            assert!(result.is_err(), "{name}: invalid selector accepted: {result:?}");
            continue;
        }
        let c = result.unwrap();
        assert!(c.home == override_home && !c.legacy && !c.wa_bridge, "{name}: {c:?}");
    }
}

#[test]
fn local_observation_yaml_defaults_and_overrides() {
    let (_h, c) = yaml_config("config.yaml", "{}");
    let c = c.unwrap();
    assert!(c.local_observation && c.local_mouse && c.bar.context == "used");
    let (_h, c) = yaml_config(
        "config.yaml",
        "localObservation: false\nlocalMouse: false\nstateDir: custom-metrics\nbar:\n  context: used\n",
    );
    let c = c.unwrap();
    assert!(!c.local_observation && !c.local_mouse && c.bar.context == "used");
    assert_eq!(c.state_dir, gopath::join2(&c.home, "custom-metrics"));
    assert!(yaml_config("config.yaml", "bar:\n  context: imaginary\n").1.is_err());
    assert_eq!(defaults("/srv/blueprint", true).bar.context, "used");
}

#[test]
fn lifecycle_yaml_defaults_and_opt_out() {
    let c = yaml_config("config.yaml", "{}").1.unwrap();
    assert!(c.lifecycle.ephemeral_default && c.lifecycle.archive_on_close);
    let c = yaml_config(
        "config.yaml",
        "lifecycle:\n  ephemeralDefault: false\n  archiveOnClose: false\n",
    )
    .1
    .unwrap();
    assert!(!c.lifecycle.ephemeral_default && !c.lifecycle.archive_on_close);
    assert!(yaml_config("config.yaml", "lifecycle:\n  ephemeralDefalt: false\n").1.is_err());
}

#[test]
fn windows_reset_color_defaults_and_validates_overrides() {
    assert_eq!(yaml_config("config.yaml", "{}").1.unwrap().windows.reset_color, "white");
    let c = yaml_config("config.yaml", "windows:\n  resetColor: 245\n").1.unwrap();
    assert_eq!(c.windows.reset_color, "245");
    assert!(yaml_config("config.yaml", "windows:\n  resetColor: not-a-color\n").1.is_err());
}

#[test]
fn cli_update_commands_default_and_override() {
    let c = yaml_config("config.yaml", "{}").1.unwrap();
    assert_eq!(c.cli_updates["codex"], strings(&["npm", "install", "-g", "@openai/codex@latest"]));
    assert_eq!(c.cli_updates["claude"], strings(&["claude", "update"]));
    let c = yaml_config("config.yaml", "cliUpdates:\n  codex: [/opt/npm, install, codex]\n").1.unwrap();
    assert_eq!(c.cli_updates["codex"], strings(&["/opt/npm", "install", "codex"]));
    assert_eq!(c.cli_updates["claude"], strings(&["claude", "update"]));
    let c = yaml_config("config.yaml", "cliUpdates:\n  codex: []\n").1.unwrap();
    assert!(c.cli_updates["codex"].is_empty() && !c.cli_updates["claude"].is_empty());
}

#[test]
fn yaml_config_paths_and_disabled_values() {
    let (_h, c) = yaml_config(
        "config.yaml",
        r#"# portable paths
agentbooks: [agents/book.json]
stateDir: state-custom
msgqRoot: queues
usageHistory: ~/usage.jsonl
waBridge: false
bar:
  widgets: []
codex:
  sockets: [sockets/codex.sock]
  disabled: true
ntfy:
  url: https://example.com
  topic: bp
  token: "test-only"
"#,
    );
    let c = c.unwrap();
    assert_eq!(c.state_dir, gopath::join2(&c.home, "state-custom"));
    assert_eq!(c.msgq_root, gopath::join2(&c.home, "queues"));
    assert_eq!(c.usage_history, gopath::join2(TEST_USER, "usage.jsonl"));
    assert!(!c.wa_bridge && c.bar.widgets.is_empty());
    assert_eq!(c.agentbooks, vec![gopath::join2(&c.home, "agents/book.json")]);
    assert_eq!(c.agentbooks, c.token_agentbooks);
    assert_eq!(c.codex.as_ref().unwrap().sockets[0], gopath::join2(&c.home, "sockets/codex.sock"));
    let ntfy = c.ntfy.as_ref().unwrap();
    assert_eq!((ntfy.topic.as_str(), ntfy.token.as_str()), ("bp", "test-only"));
    assert!(c.codex_disabled());
}

#[test]
fn invalid_yaml_never_falls_back_to_defaults() {
    for data in [
        "bar: [",
        "stateDri: state",
        "bar: {widgtes: [ctx]}",
        "waBridge: definitely-not-a-bool",
        "stateDir: first\nstateDir: second\n",
        "bar: {widgets: [typo]}",
        "bar: {}\n---\nbar: {}\n",
        "fed: {mode: hub, listen: '0.0.0.0:7877', peerName: laptop}",
    ] {
        assert!(yaml_config("config.yaml", data).1.is_err(), "invalid config accepted: {data:?}");
    }
}

#[test]
fn yml_and_json_compatibility_and_conflict() {
    for name in ["config.yml", "config.json"] {
        let (home, c) = yaml_config(name, r#"{"bar":{"widgets":["model","quota"]}}"#);
        let c = c.unwrap();
        assert_eq!(c.bar.widgets, strings(&["model", "quota"]), "{name}");
        assert_eq!(gopath::base(&c.path), name);
        std::fs::write(home.path().join("config.yaml"), "bar: {}\n").unwrap();
        let err = load_home(home.path().to_str().unwrap()).unwrap_err();
        assert!(err.to_string().contains("multiple bp configs"), "{err}");
    }
}

#[test]
fn init_yaml_creates_valid_private_config_and_preserves_edits() {
    let dir = tempfile::tempdir().unwrap();
    let home = dir.path().join("bp");
    let home = home.to_str().unwrap();
    let path = init_yaml(home).unwrap();
    load_home(home).unwrap();
    assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
    let edited = "bar:\n  widgets: [model]\n";
    std::fs::write(&path, edited).unwrap();
    init_yaml(home).unwrap();
    assert_eq!(std::fs::read_to_string(&path).unwrap(), edited, "setup overwrote user settings");
    for name in ["config.json", "config.yml"] {
        let dir = tempfile::tempdir().unwrap();
        let existing = dir.path().join(name);
        std::fs::write(&existing, "{}").unwrap();
        assert_eq!(init_yaml(dir.path().to_str().unwrap()).unwrap(), existing.to_str().unwrap());
        assert!(!dir.path().join("config.yaml").exists(), "existing format shadowed");
    }
    assert!(init_yaml("").is_err());
}

#[test]
fn server_and_laptop_bar_defaults_match() {
    let (server, laptop) = (defaults("/srv/blueprint", true), defaults("/tmp/laptop", false));
    assert_eq!(server.bar.context, laptop.bar.context);
    assert_eq!(server.bar.widgets, laptop.bar.widgets);
    let c = yaml_config("config.yaml", "bar:\n  context: remaining\n").1.unwrap();
    assert_eq!(c.bar.context, "remaining");
}

#[test]
fn default_color_config() {
    for value in ["purple", "33", "''"] {
        let body = format!("bar:\n  defaultColor: {value}\n");
        assert!(yaml_config("config.yaml", &body).1.is_ok(), "{value}");
    }
    for value in ["256", "blurple", "-1"] {
        let body = format!("bar:\n  defaultColor: {value}\n");
        assert!(yaml_config("config.yaml", &body).1.is_err(), "accepted {value}");
    }
}

#[test]
fn claude_accounts_defaults_overrides_and_validation() {
    let c = yaml_config("config.yaml", "{}").1.unwrap();
    assert_eq!(c.claude_accounts, default_claude_accounts());
    assert!(!c.claude_accounts.auto_switch && c.claude_accounts.threshold == 90);
    assert!(!c.claude_accounts.keep_alive && c.claude_accounts.keep_alive_model == "haiku");
    assert_eq!(defaults("/srv/blueprint", true).claude_accounts, default_claude_accounts());
    let c = yaml_config("config.yaml", "claudeAccounts:\n  autoSwitch: true\n  threshold: 80\n").1.unwrap();
    let a = &c.claude_accounts;
    assert!(a.auto_switch && a.threshold == 80 && a.cooldown_minutes == 5 && a.poll_minutes == 5);
    for bad in [
        "claudeAccounts:\n  threshold: 0\n",
        "claudeAccounts:\n  threshold: 101\n",
        "claudeAccounts:\n  cooldownMinutes: 0\n",
        "claudeAccounts:\n  pollMinutes: -1\n",
        "claudeAccounts:\n  autoswitch: true\n",
        "claudeAccounts:\n  limits:\n    huseyin: 0\n",
        "claudeAccounts:\n  limits:\n    \"3\": 101\n",
        "claudeAccounts:\n  limits:\n    \" \": 40\n",
        "claudeAccounts:\n  keepAliveModel: \"\"\n",
        "claudeAccounts:\n  keepAliveModel: --bare\n",
    ] {
        assert!(yaml_config("config.yaml", bad).1.is_err(), "accepted {bad:?}");
    }
    let c = yaml_config(
        "config.yaml",
        "claudeAccounts:\n  keepAlive: true\n  keepAliveModel: sonnet\n  limits:\n    huseyin: 40\n    \"4\": 35\n",
    )
    .1
    .unwrap();
    let a = &c.claude_accounts;
    assert!(a.keep_alive && a.keep_alive_model == "sonnet" && !a.auto_switch);
    assert_eq!(
        a.limits,
        BTreeMap::from([("huseyin".to_string(), 40), ("4".to_string(), 35)])
    );
    assert_eq!(
        a.limit_percents().unwrap(),
        BTreeMap::from([("huseyin".to_string(), 40.0), ("4".to_string(), 35.0)])
    );
    let c = yaml_config(
        "config.json",
        r#"{"claudeAccounts":{"autoSwitch":true,"pollMinutes":3,"limits":{"cerci":40}}}"#,
    )
    .1
    .unwrap();
    let a = &c.claude_accounts;
    assert!(a.auto_switch && a.poll_minutes == 3 && a.threshold == 90 && a.limits["cerci"] == 40);
}

#[test]
fn remote_config_loads_and_expands_identity() {
    let c = yaml_config(
        "config.yaml",
        "remotes:\n  server:\n    host: server.example\n    port: 2222\n    user: tuna\n    identity: ~/.ssh/server\n    transport: mosh\n    moshPorts: 60000:61000\n    elevate: sudo -i\n",
    )
    .1
    .unwrap();
    let remote = &c.remotes["server"];
    assert_eq!(remote.host, "server.example");
    assert_eq!(remote.port, 2222);
    assert_eq!(remote.transport, "mosh");
    assert_eq!(remote.identity, gopath::join2(TEST_USER, ".ssh/server"));
}

#[test]
fn update_remote_yaml_is_atomic_and_preserves_other_settings() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("config.yaml");
    std::fs::write(&path, "# keep this comment\nlocalMouse: false\n").unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o640)).unwrap();
    let path_s = path.to_str().unwrap();
    let remote = RemoteConfig {
        host: "server.example".into(),
        transport: "ssh".into(),
        identity: "~/.ssh/id".into(),
        ..RemoteConfig::default()
    };
    update_remote(path_s, "server", Some(&remote)).unwrap();
    let data = std::fs::read_to_string(&path).unwrap();
    assert!(
        data.contains("# keep this comment") && data.contains("localMouse: false") && data.contains("server.example"),
        "{data:?}"
    );
    assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o640);
    update_remote(path_s, "server", None).unwrap();
    let c = yaml_config("config.yaml", &std::fs::read_to_string(&path).unwrap()).1.unwrap();
    assert!(c.remotes.is_empty() && !c.local_mouse);
    let names: Vec<_> = std::fs::read_dir(dir.path())
        .unwrap()
        .map(|e| e.unwrap().file_name().into_string().unwrap())
        .collect();
    assert!(!names.iter().any(|n| n.ends_with(".tmp")), "{names:?}");
}

#[test]
fn update_remote_json_preserves_other_settings() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("config.json");
    std::fs::write(&path, r#"{"localMouse":false,"bar":{"widgets":["model"]}}"#).unwrap();
    let remote = RemoteConfig {
        host: "server.example".into(),
        transport: "ssh".into(),
        ..RemoteConfig::default()
    };
    update_remote(path.to_str().unwrap(), "server", Some(&remote)).unwrap();
    let c = load_home(dir.path().to_str().unwrap()).unwrap();
    assert!(!c.local_mouse);
    assert_eq!(c.remotes["server"].host, "server.example");
    assert_eq!(c.bar.widgets, strings(&["model"]));
}

#[test]
fn remote_validation_rejects_unsafe_values() {
    for data in [
        "remotes: {server: {host: '-bad'}}\n",
        "remotes: {server: {host: ok, port: 70000}}\n",
        "remotes: {server: {host: ok, transport: telnet}}\n",
        "remotes: {server: {host: ok, moshPorts: '61000:60000'}}\n",
        "remotes: {server: {host: ok, elevate: 'sudo; reboot'}}\n",
    ] {
        assert!(yaml_config("config.yaml", data).1.is_err(), "unsafe remote accepted: {data}");
    }
}

#[test]
fn p2p_yaml_and_validation() {
    for (body, want_err) in [
        ("p2p:\n  enabled: true\n  listen: [/ip4/127.0.0.1/tcp/0]\n  relay: true\n", false),
        ("p2p:\n  enabled: true\n  rendezvous: [https://example.com]\n", true),
        ("p2p:\n  enabled: true\n  peers:\n    laptop:\n      id: not-a-peer-id\n", true),
        ("p2p:\n  enabled: true\n  invented: yes\n", true),
    ] {
        let getenv = |k: &str| if k == "BP_HOME" { "/test-bp".to_string() } else { String::new() };
        let user = || Ok("/home/test".to_string());
        let read = |p: &str| {
            if p.ends_with("config.yaml") {
                Ok(body.as_bytes().to_vec())
            } else {
                Err(not_exist())
            }
        };
        let result = load_with(
            &LoadEnv {
                getenv: &getenv,
                user_home: &user,
                read_file: &read,
            },
            None,
        );
        assert_eq!(result.is_err(), want_err, "{body}: {result:?}");
        if let Ok(c) = result {
            assert!(c.p2p.is_some_and(|p| p.enabled), "p2p settings ignored");
        }
    }
}

#[test]
fn split_host_port_matches_go() {
    assert_eq!(split_host_port("127.0.0.1:80").unwrap(), ("127.0.0.1".into(), "80".into()));
    assert_eq!(split_host_port("[::1]:80").unwrap(), ("::1".into(), "80".into()));
    assert_eq!(split_host_port(":80").unwrap(), ("".into(), "80".into()));
    assert_eq!(
        split_host_port("127.0.0.1").unwrap_err(),
        "address 127.0.0.1: missing port in address"
    );
    assert_eq!(split_host_port("::1:80").unwrap_err(), "address ::1:80: too many colons in address");
    assert!(split_host_port("[::1]").is_err());
    assert!(split_host_port("[::1]x:80").is_err());
}

/// Every case of testdata/golden/core/config.json (Go `config.Load`).
#[test]
fn golden_config_load() {
    let cases = golden("config.json");
    for case in cases.as_array().unwrap() {
        let name = case["name"].as_str().unwrap();
        let file = case["file"].as_str().unwrap();
        let body = b64(case["body"].as_str().unwrap());
        let home = tempfile::tempdir().unwrap();
        let home_s = home.path().to_str().unwrap().to_string();
        if !file.is_empty() {
            std::fs::write(home.path().join(file), &body).unwrap();
        }
        let getenv = |k: &str| match k {
            "BP_HOME" => home_s.clone(),
            "HOME" => "/home/golden".to_string(),
            _ => String::new(),
        };
        let user = || Ok("/home/golden".to_string());
        let read = |p: &str| std::fs::read(p);
        let result = load_with(
            &LoadEnv {
                getenv: &getenv,
                user_home: &user,
                read_file: &read,
            },
            None,
        );
        let want_err = case["error"].as_bool().unwrap();
        let text = String::from_utf8_lossy(&body);
        match result {
            Err(err) => assert!(want_err, "{name} {text:?}: unexpected error {err}"),
            Ok(c) => {
                assert!(!want_err, "{name} {text:?}: accepted, Go rejects");
                let json = crate::gojson::to_string(&c).unwrap().replace(&home_s, "$HOME");
                let mut got: Value = serde_json::from_str(&json).unwrap();
                let map = got.as_object_mut().unwrap();
                map.insert("Path".into(), Value::from(c.path.replace(&home_s, "$HOME")));
                map.insert("Home".into(), Value::from(c.home.replace(&home_s, "$HOME")));
                map.insert("Legacy".into(), Value::from(c.legacy));
                map.insert("InvalidConfig".into(), Value::from(!c.invalid_config.is_empty()));
                assert_eq!(got, case["result"], "{name} {text:?}");
            }
        }
    }
}

/// Every case of testdata/golden/core/update_remote.json (Go `UpdateRemote`).
#[test]
fn golden_update_remote() {
    let cases = golden("update_remote.json");
    for case in cases.as_array().unwrap() {
        let input = case["in"].as_str().unwrap();
        let name = case["name"].as_str().unwrap();
        let file = if case["json"].as_bool().unwrap() { "config.json" } else { "config.yaml" };
        let remote = case["remote"].as_object().map(|r| {
            let s = |k: &str| r.get(k).and_then(Value::as_str).unwrap_or("").to_string();
            RemoteConfig {
                host: s("host"),
                port: r.get("port").and_then(Value::as_i64).unwrap_or(0),
                user: s("user"),
                identity: s("identity"),
                transport: s("transport"),
                mosh_ports: s("moshPorts"),
                elevate: s("elevate"),
            }
        });
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join(file);
        std::fs::write(&path, input).unwrap();
        let result = update_remote(path.to_str().unwrap(), name, remote.as_ref());
        let want_err = case.get("error").and_then(Value::as_str).unwrap_or("");
        match &result {
            Ok(()) => assert!(want_err.is_empty(), "{input:?} {name}: want error {want_err}"),
            Err(err) => {
                if want_err.is_empty() {
                    panic!("{input:?} {name}: unexpected error {err}");
                }
                // Go's YAML/JSON parser texts differ; the config-level ones must match.
                if !want_err.starts_with("yaml:") && !want_err.starts_with("json:") {
                    assert_eq!(err.to_string(), want_err, "{input:?} {name}");
                }
            }
        }
        let out = std::fs::read_to_string(&path).unwrap();
        assert_eq!(out, case["out"].as_str().unwrap(), "{input:?} {name} {remote:?}");
    }
}

#[test]
fn update_remote_yaml_flow_styles() {
    let remote = RemoteConfig {
        host: "server.example".into(),
        identity: "~/.ssh/id".into(),
        transport: "ssh".into(),
        ..RemoteConfig::default()
    };
    let got = remotes::update_remote_yaml(b"remotes: {a: {host: a}}\n", "b", Some(&remote)).unwrap();
    assert_eq!(
        String::from_utf8(got).unwrap(),
        "remotes: {a: {host: a}, b: {host: server.example, identity: ~/.ssh/id, transport: ssh}}\n"
    );
    let got = remotes::update_remote_yaml(b"{}\n", "b", Some(&remote)).unwrap();
    assert_eq!(
        String::from_utf8(got).unwrap(),
        "{remotes: {b: {host: server.example, identity: ~/.ssh/id, transport: ssh}}}\n"
    );
    assert_eq!(
        remotes::update_remote_yaml(b"localMouse: false\n", "b", None).unwrap_err(),
        "unknown remote: b"
    );
}
