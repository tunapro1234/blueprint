//! Port of cmd/bp/main.go (whoami, identity helpers).

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn whoami(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("whoami"))
    }
}
