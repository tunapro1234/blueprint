//! Port of cmd/bp/img.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn image(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("image"))
    }
}
