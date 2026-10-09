//! Port of cmd/bp/update.go, fleet_update.go, codex_policy.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn update(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("update"))
    }
}
