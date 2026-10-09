//! Port of cmd/bp/history.go, schema.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn history_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("history_command"))
    }

    pub(crate) fn schema_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("schema_command"))
    }

    pub(crate) fn continue_project(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("continue_project"))
    }
}
