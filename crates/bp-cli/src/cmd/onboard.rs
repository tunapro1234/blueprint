//! Port of cmd/bp/onboard.go, setup.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn show_book(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("show_book"))
    }

    pub(crate) fn onboard(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("onboard"))
    }

    pub(crate) fn local_setup(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("local_setup"))
    }
}
