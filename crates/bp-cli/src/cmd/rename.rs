//! Port of cmd/bp/rename.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn rename(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("rename"))
    }
}
