//! Port of the pure launch builders of internal/tmux/tmux.go and
//! codex_process.go: `OpenOptions` and `Validate`, `OnboardingPrompt`,
//! `mungeProjectPath`, `launchArgs`, `codexRemoteResumeArgs`,
//! `launchBinary`, `bypassShellAliases`, `ClaudeConfigPrefix`, `shellQuote`,
//! `CodexSocket`, and the command construction of `(*Client).Open`.

use std::path::Path;
use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::error::Error;
use crate::screen::harness::hermes::HERMES_BIN;

/// `OpenOptions` records how to reopen an agent. The Go `Progress` callback
/// is not part of the pure options and lives with the client.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct OpenOptions {
    #[serde(skip_serializing_if = "is_false")]
    pub resume: bool,
    #[serde(rename = "resumeId", skip_serializing_if = "String::is_empty")]
    pub resume_id: String,
    #[serde(skip_serializing_if = "is_false")]
    pub codex: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub remote: String,
    #[serde(skip_serializing_if = "is_false")]
    pub hermes: bool,
    #[serde(rename = "opencode", skip_serializing_if = "is_false")]
    pub open_code: bool,
    #[serde(rename = "noSandbox", skip_serializing_if = "is_false")]
    pub no_sandbox: bool,
    /// Native CLI flags the agent was launched with, passed verbatim after
    /// the harness binary.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    #[serde(rename = "claudeAccount", skip_serializing_if = "String::is_empty")]
    pub claude_account: String,
    #[serde(rename = "claudeConfigDir", skip_serializing_if = "String::is_empty")]
    pub claude_config_dir: String,
    #[serde(skip)]
    pub no_prompt: bool,
    #[serde(skip)]
    pub legacy: bool,
    /// Internal, shell-quoted wrapper for local managed sessions.
    #[serde(skip)]
    pub launcher: String,
}

fn is_false(value: &bool) -> bool {
    !*value
}

static CODEX_THREAD_ID: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$").expect("codexThreadID")
});

impl OpenOptions {
    /// `OpenOptions.Validate`.
    pub fn validate(&self) -> Result<(), Error> {
        if [self.codex, self.hermes, self.open_code]
            .iter()
            .filter(|v| **v)
            .count()
            > 1
        {
            return Err(Error::other("choose one harness"));
        }
        if self.no_sandbox && !self.codex {
            return Err(Error::other("--no-sandbox requires Codex"));
        }
        if !self.remote.is_empty() {
            if !self.codex || codex_socket("/home", &self.remote).is_empty() {
                return Err(Error::other(
                    "--remote requires Codex and a unix:// endpoint",
                ));
            }
            if !self.no_sandbox {
                return Err(Error::other(
                    "remote execution leaves the TUI sandbox; use an explicitly approved --no-sandbox launch",
                ));
            }
        }
        if !self.resume_id.is_empty() && !CODEX_THREAD_ID.is_match(&self.resume_id) {
            return Err(Error::other("invalid session/thread id"));
        }
        Ok(())
    }
}

/// `CodexSocket`: the unix socket path of a Codex app-server endpoint, or "".
pub fn codex_socket(home: &str, endpoint: &str) -> String {
    if endpoint == "unix://" {
        return Path::new(home)
            .join("app-server-control")
            .join("app-server-control.sock")
            .to_string_lossy()
            .into_owned();
    }
    if endpoint.starts_with("unix:///") {
        return endpoint["unix://".len()..].to_string();
    }
    String::new()
}

/// `portableOnboarding`.
const PORTABLE_ONBOARDING: &str = "You are the '%s' bp agent. Follow the user's instructions and the agent rules in your own working directory. Read bp help, bp config path and bp book to learn this machine's configuration and coordinator; do not assume server-specific paths or privileges. Use bp status, bp msg and bp qstat for communication; let the queue preserve busy agents and user input. Do not manually type into other panes. Briefly report readiness in the user's language.";

/// `OnboardingPrompt`: the first prompt bp open gives a new agent.
pub fn onboarding_prompt(session: &str) -> String {
    PORTABLE_ONBOARDING.replacen("%s", session, 1)
}

/// `mungeProjectPath`: Claude Code's cwd -> project-dir encoding. Every BYTE
/// that is not an ASCII letter or digit becomes '-'.
pub fn munge_project_path(dir: &str) -> String {
    dir.bytes()
        .map(|c| {
            if c.is_ascii_alphanumeric() {
                c as char
            } else {
                '-'
            }
        })
        .collect()
}

/// `shellQuote`: POSIX single-quote quoting.
pub fn shell_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// `launchArgs` shell-quotes recorded native flags, leaving out the ones the
/// base command already carries.
pub fn launch_args<S: AsRef<str>>(command: &str, args: &[S]) -> String {
    let padded = format!(" {command} ");
    let mut out = Vec::new();
    for arg in args {
        let arg = arg.as_ref();
        if arg.starts_with('-') && padded.contains(&format!(" {arg} ")) {
            continue;
        }
        let bypass = arg == "--yolo" || arg == "--dangerously-bypass-approvals-and-sandbox";
        if bypass && command.contains("--dangerously-bypass-approvals-and-sandbox") {
            continue;
        }
        out.push(shell_quote(arg));
    }
    out.join(" ")
}

/// `codexRemoteResumeArgs` drops recorded options whose permissions are
/// owned by the remote app-server.
pub fn codex_remote_resume_args<S: AsRef<str>>(args: &[S]) -> Vec<String> {
    let mut filtered = Vec::with_capacity(args.len());
    let mut i = 0;
    while i < args.len() {
        let arg = args[i].as_ref();
        let (name, value, inline) = match arg.split_once('=') {
            Some((name, value)) => (name, value, true),
            None => (arg, "", false),
        };
        match name {
            "--yolo" | "--dangerously-bypass-approvals-and-sandbox" | "--full-auto" => {
                i += 1;
                continue;
            }
            "-s" | "--sandbox" | "-a" | "--ask-for-approval" | "--add-dir" => {
                if !inline && i + 1 < args.len() {
                    i += 1;
                }
                i += 1;
                continue;
            }
            "-c" | "--config" => {
                if !inline && i + 1 < args.len() {
                    if codex_remote_resume_permission_config(args[i + 1].as_ref()) {
                        i += 2;
                        continue;
                    }
                } else if inline && codex_remote_resume_permission_config(value) {
                    i += 1;
                    continue;
                }
            }
            _ => {}
        }
        filtered.push(arg.to_string());
        i += 1;
    }
    filtered
}

/// `codexRemoteResumePermissionConfig`.
fn codex_remote_resume_permission_config(value: &str) -> bool {
    let Some((key, _)) = value.split_once('=') else {
        return false;
    };
    let key = key.trim();
    matches!(
        key,
        "sandbox_mode"
            | "approval_policy"
            | "default_permissions"
            | "permission_profile"
            | "permissions"
    ) || key.starts_with("sandbox_workspace_write.")
        || key.starts_with("permission_profile.")
        || key.starts_with("permissions.")
}

/// `launchBinary` names the harness executable in an Open command.
pub fn launch_binary(opts: &OpenOptions) -> &'static str {
    if opts.open_code {
        "opencode"
    } else if opts.hermes {
        HERMES_BIN
    } else if opts.codex {
        "codex"
    } else {
        "claude"
    }
}

/// `bypassShellAliases` prefixes the harness executable with the shell's
/// `command` builtin (whole-word lookup).
pub fn bypass_shell_aliases(command: &str, bin: &str) -> String {
    let padded = format!(" {command} ");
    match padded.find(&format!(" {bin} ")) {
        Some(at) => format!("{}command {}", &command[..at], &command[at..]),
        None => command.to_string(),
    }
}

/// `ClaudeConfigPrefix`: the environment assignment that starts an
/// account-bound Claude agent in its account's profile home, or "".
pub fn claude_config_prefix(opts: &OpenOptions) -> String {
    if opts.claude_config_dir.is_empty() || opts.codex || opts.hermes || opts.open_code {
        return String::new();
    }
    format!(
        "CLAUDE_CONFIG_DIR={} ",
        shell_quote(&opts.claude_config_dir)
    )
}

/// The command `(*Client).Open` types into the pane, and whether it carries
/// the native onboarding prompt.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LaunchCommand {
    pub command: String,
    pub native_onboarding: bool,
}

/// The pure command construction of `(*Client).Open` (tmux.go). `opts` must
/// already carry the resolved Claude resume id; validation and the Codex
/// "verified thread id" check run here exactly as in Open.
pub fn launch_command(session: &str, opts: &OpenOptions) -> Result<LaunchCommand, Error> {
    opts.validate()?;
    if opts.codex && opts.resume && opts.resume_id.is_empty() {
        return Err(Error::other("Codex resume requires a verified thread id"));
    }
    let mut command = format!(
        "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX={session} claude --dangerously-skip-permissions --name {}",
        shell_quote(session)
    );
    if opts.resume && !opts.codex && !opts.hermes && !opts.open_code {
        command.push_str(" --resume ");
        command.push_str(&shell_quote(&opts.resume_id));
    }
    if opts.codex {
        command = "codex".to_string();
        if opts.no_sandbox {
            command = "CODEX_BWRAPPED=1 codex".to_string();
            if opts.remote.is_empty() || !opts.resume {
                command.push_str(" --dangerously-bypass-approvals-and-sandbox");
            }
        }
        if !opts.remote.is_empty() {
            command.push_str(" --remote ");
            command.push_str(&shell_quote(&opts.remote));
        }
        if opts.resume {
            command.push_str(" resume ");
            command.push_str(&shell_quote(&opts.resume_id));
        }
    }
    if opts.hermes {
        command = HERMES_BIN.to_string();
    }
    if opts.open_code {
        command = "opencode".to_string();
    }
    let bin = launch_binary(opts);
    let recorded = if opts.codex && !opts.remote.is_empty() && opts.resume {
        codex_remote_resume_args(&opts.args)
    } else {
        opts.args.clone()
    };
    let extra = launch_args(&command, &recorded);
    if !extra.is_empty() {
        let padded = format!(" {command} ");
        if let Some(at) = padded.find(&format!(" {bin} ")) {
            let end = at + bin.len();
            command = format!("{} {extra}{}", &command[..end], &command[end..]);
        }
    }
    command = claude_config_prefix(opts) + &command;
    let native_onboarding = opts.codex && !opts.resume && !opts.no_prompt;
    if native_onboarding {
        command.push(' ');
        command.push_str(&shell_quote(&onboarding_prompt(session)));
    }
    if opts.launcher.is_empty() {
        command = bypass_shell_aliases(&command, bin);
    } else {
        command = format!("exec {} {}", opts.launcher, shell_quote(&command));
    }
    Ok(LaunchCommand {
        command,
        native_onboarding,
    })
}

#[cfg(test)]
mod tests;
