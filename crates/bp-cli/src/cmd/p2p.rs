//! Port of cmd/bp/p2p.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn p2p_command(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("p2p_command"))
    }
}
