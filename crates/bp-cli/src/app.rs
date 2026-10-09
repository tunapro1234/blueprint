//! Port of the `app` struct and `run` dispatch in cmd/bp/main.go.

use std::io::Write;

use crate::error::CliError;
use crate::errorf;

pub const USAGE: &str = include_str!("usage.txt");

/// Shared state for one bp invocation. Go's optional function fields (test
/// seams) become trait objects or closures added by each command module.
pub struct App {
    pub out: Box<dyn Write>,
    pub err: Box<dyn Write>,
}

impl App {
    pub fn new() -> Self {
        App {
            out: Box::new(std::io::stdout()),
            err: Box::new(std::io::stderr()),
        }
    }

    pub fn run(&mut self, args: &[String]) -> Result<(), CliError> {
        let default = ["status".to_string()];
        let args = if args.is_empty() { &default[..] } else { args };
        if help_requested(args) {
            writeln!(self.out, "{USAGE}")?;
            return Ok(());
        }
        let rest = &args[1..];
        match args[0].as_str() {
            "run" => self.local_run(rest),
            "_session" => self.local_session(rest),
            "_open-session" => self.managed_session(rest),
            "_local-worker" => self.local_worker(rest),
            "_workflow-run" => self.workflow_run(rest),
            "whoami" => self.whoami(rest),
            "update" => self.update(rest),
            "onboard" => self.onboard(rest),
            "book" => self.show_book(rest),
            "setup" => self.local_setup(rest),
            "status" => self.status(rest),
            "windows" => self.windows(rest),
            "focus" => self.focus(rest),
            "tree" => self.tree(rest),
            "open" => self.open(rest),
            "attach" => self.attach(rest),
            "schema" => self.schema_command(rest),
            "continue" => self.continue_project(rest),
            "history" => self.history_command(rest),
            "worktree" => self.worktree(rest),
            "close" => self.close(rest),
            "archive" => self.archive(rest),
            "restore" => self.restore(rest),
            "keep" => self.set_lifetime_persistent(rest),
            "release" => self.set_lifetime_ephemeral(rest),
            "rename" => self.rename(rest),
            "reparent" => self.reparent(rest),
            "msg" => self.message(rest),
            "announce" => self.announce(rest),
            "compact" => self.compact(rest),
            "workflow" => self.workflow(rest),
            "wait" => self.workflow_wait(rest),
            "remote" => self.remote(rest),
            "shell" => self.shell(rest),
            "q" => self.queue_command(rest),
            "qstat" => self.queue_status(rest),
            "qcancel" => self.queue_cancel(rest),
            "peek" => self.peek(rest),
            "wa" => self.whatsapp(rest),
            "usage" => self.usage_command(rest),
            "tokens" => self.tokens(rest),
            "monitor" => self.monitor(rest),
            "policy" => self.policy(rest),
            "account" => self.account(rest),
            "service" => self.service(rest),
            "con" => self.connect(rest),
            "img" => self.image(rest),
            "bar" => self.bar(rest),
            "color" => self.color(rest),
            "name" => self.bar_name_command(rest),
            "dash" => self.dashboard(rest),
            "p2p" => self.p2p_command(rest),
            "fed" => self.federation(rest),
            "daemon" => self.daemon(rest),
            "config" => self.config_command(rest),
            "help" | "-h" | "--help" => {
                writeln!(self.out, "{USAGE}")?;
                Ok(())
            }
            other => {
                writeln!(self.err, "{USAGE}")?;
                Err(errorf!("unknown command: {other}"))
            }
        }
    }

    fn config_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(crate::error::not_ported("config"))
    }
}

/// Reports whether -h/--help appears among a subcommand's own flags. Free-form
/// message commands only honour help before the message starts, so
/// "bp msg agent --help" still delivers the literal word.
pub fn help_requested(args: &[String]) -> bool {
    let mut rest = &args[1..];
    match args[0].as_str() {
        "run" | "_session" | "_local-worker" => return false,
        "msg" | "announce" => {
            if let Some(index) = rest.iter().position(|arg| !arg.starts_with('-')) {
                rest = &rest[..index];
            }
        }
        "wa" => {
            if matches!(
                rest.first().map(String::as_str),
                Some("send" | "read" | "chats")
            ) {
                rest = &rest[1..];
            }
            for arg in rest {
                if arg == "--" || !arg.starts_with('-') {
                    return false;
                }
                if arg == "-h" || arg == "--help" {
                    return true;
                }
            }
            return false;
        }
        _ => {}
    }
    rest.iter().any(|arg| arg == "-h" || arg == "--help")
}

/// Guards commands whose first positional argument is an agent name.
pub fn reject_flag(command: &str, arg: &str) -> Result<(), CliError> {
    if arg.starts_with('-') {
        return Err(errorf!("unknown {command} option: {arg}"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(list: &[&str]) -> Vec<String> {
        list.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn help_rules_follow_go() {
        assert!(!help_requested(&args(&["run", "--help"])));
        assert!(help_requested(&args(&["msg", "--help", "x"])));
        assert!(!help_requested(&args(&["msg", "x", "--help"])));
        assert!(help_requested(&args(&["wa", "send", "-h"])));
        assert!(!help_requested(&args(&["wa", "send", "text", "-h"])));
        assert!(help_requested(&args(&["status", "--help"])));
    }

    #[test]
    fn usage_matches_go_constant() {
        assert!(USAGE.starts_with("blueprint (bp) — agent infrastructure CLI\n"));
        assert!(USAGE.ends_with("bp daemon"));
    }
}
