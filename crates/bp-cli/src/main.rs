//! The `bp` binary. Port of cmd/bp/main.go `main`: pre-dispatch for commands
//! that must work without (or with a broken) configuration, then `App::run`
//! and Go's exit-code contract.

mod app;
mod cmd;
mod error;

use std::io::Write;

use error::CliError;

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let mut app = app::App::new();
    let args = if args.is_empty() {
        vec!["status".to_string()]
    } else {
        args
    };
    match app.run(&args) {
        Ok(()) => {}
        Err(CliError::Reported) => std::process::exit(1),
        Err(CliError::Exit { code, message }) => {
            if !message.is_empty() {
                let _ = writeln!(std::io::stderr(), "ERROR: {message}");
            }
            std::process::exit(code);
        }
        Err(CliError::Message(message)) => {
            let _ = writeln!(std::io::stderr(), "ERROR: {message}");
            std::process::exit(1);
        }
    }
}
