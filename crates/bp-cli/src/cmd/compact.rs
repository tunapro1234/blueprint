//! Port of cmd/bp/main.go (compact*).

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn compact(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("compact"))
    }
}
