//! Port of cmd/bp/main.go (q, qstat, qcancel, peek).

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn queue_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("queue_command"))
    }

    pub(crate) fn queue_status(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("queue_status"))
    }

    pub(crate) fn queue_cancel(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("queue_cancel"))
    }

    pub(crate) fn peek(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("peek"))
    }
}
