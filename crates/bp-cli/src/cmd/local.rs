//! Port of cmd/bp/local*.go, launch_mode.go, runtime.go, native_name.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn local_run(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("local_run"))
    }

    pub(crate) fn local_session(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("local_session"))
    }

    pub(crate) fn managed_session(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("managed_session"))
    }

    pub(crate) fn local_worker(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("local_worker"))
    }
}
