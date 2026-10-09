//! Port of cmd/bp/main.go (open, worktree, close), attach.go, reparent.go.

use crate::app::App;
use crate::error::{CliError, not_ported};

impl App {
    pub(crate) fn open(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("open"))
    }

    pub(crate) fn worktree(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("worktree"))
    }

    pub(crate) fn close(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("close"))
    }

    pub(crate) fn attach(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("attach"))
    }

    pub(crate) fn reparent(&mut self, _args: &[String]) -> Result<(), CliError> {
        Err(not_ported("reparent"))
    }
}
