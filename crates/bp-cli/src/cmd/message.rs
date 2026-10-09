//! Port of cmd/bp/main.go (message, announce, deliver, force).

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn message(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("message"))
    }

    pub(crate) fn announce(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("announce"))
    }
}
