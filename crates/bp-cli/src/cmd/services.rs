//! Port of cmd/bp/main.go (usage, monitor, policy, service, fed, daemon, dash, con), remotes.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn usage_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("usage_command"))
    }

    pub(crate) fn monitor(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("monitor"))
    }

    pub(crate) fn policy(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("policy"))
    }

    pub(crate) fn service(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("service"))
    }

    pub(crate) fn federation(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("federation"))
    }

    pub(crate) fn daemon(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("daemon"))
    }

    pub(crate) fn dashboard(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("dashboard"))
    }

    pub(crate) fn connect(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("connect"))
    }

    pub(crate) fn remote(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("remote"))
    }

    pub(crate) fn shell(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("shell"))
    }
}
