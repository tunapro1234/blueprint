//! Port of cmd/bp/account*.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn account(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("account"))
    }
}
