//! Port of cmd/bp/main.go (status, tree, codex*).

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn status(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("status"))
    }

    pub(crate) fn tree(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("tree"))
    }
}
