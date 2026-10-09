//! Port of cmd/bp/bar*.go, color.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn bar(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("bar"))
    }

    pub(crate) fn color(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("color"))
    }

    pub(crate) fn bar_name_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("bar_name_command"))
    }
}
