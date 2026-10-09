//! Port of the error contract in cmd/bp/main.go: errReported, commandExitError
//! and the default `ERROR: <msg>` exit 1.

use std::fmt;

#[derive(Debug)]
pub enum CliError {
    /// The command already printed its explanation; exit 1 without another line.
    Reported,
    /// A specific exit code; `message` (if non-empty) is printed as `ERROR: <message>`.
    Exit { code: i32, message: String },
    /// Any other failure: printed as `ERROR: <message>`, exit 1.
    Message(String),
}

impl fmt::Display for CliError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            CliError::Reported => f.write_str("reported above"),
            CliError::Exit { message, .. } => f.write_str(message),
            CliError::Message(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for CliError {}

impl CliError {
    pub fn msg(message: impl Into<String>) -> Self {
        CliError::Message(message.into())
    }

    pub fn exit(code: i32, message: impl Into<String>) -> Self {
        CliError::Exit {
            code,
            message: message.into(),
        }
    }
}

/// Builds a `CliError::Message` with `format!` syntax, like Go's `fmt.Errorf`.
#[macro_export]
macro_rules! errorf {
    ($($arg:tt)*) => {
        $crate::error::CliError::Message(format!($($arg)*))
    };
}

/// Marks a command whose Rust port has not landed yet.
pub fn not_ported(command: &str) -> CliError {
    CliError::Message(format!("{command}: not yet implemented in the Rust port"))
}

impl From<std::io::Error> for CliError {
    fn from(err: std::io::Error) -> Self {
        CliError::Message(err.to_string())
    }
}
