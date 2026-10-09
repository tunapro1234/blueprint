//! Port of cmd/bp/workflow.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn workflow(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("workflow"))
    }

    pub(crate) fn workflow_wait(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("workflow_wait"))
    }

    pub(crate) fn workflow_run(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("workflow_run"))
    }
}
