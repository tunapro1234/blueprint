//! Port of cmd/bp/archive.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn archive(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("archive"))
    }

    pub(crate) fn restore(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("restore"))
    }

    pub(crate) fn set_lifetime_persistent(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("set_lifetime_persistent"))
    }

    pub(crate) fn set_lifetime_ephemeral(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("set_lifetime_ephemeral"))
    }
}
