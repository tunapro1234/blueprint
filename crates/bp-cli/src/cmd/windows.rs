//! Port of cmd/bp/windows.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn windows(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("windows"))
    }

    pub(crate) fn focus(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("focus"))
    }
}
