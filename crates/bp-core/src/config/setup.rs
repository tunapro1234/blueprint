//! Port of internal/config/setup.go.

use std::fs::OpenOptions;
use std::io::{self, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};

use crate::gopath;

/// Go `config.ExampleYAML` (the embedded `example.yaml`).
pub const EXAMPLE_YAML: &str = include_str!("../../../../internal/config/example.yaml");

/// Go `InitYAML`: creates a readable local config without replacing any
/// existing format, returning the active config path.
pub fn init_yaml(home: &str) -> io::Result<String> {
    if home.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid argument"));
    }
    for name in ["config.yaml", "config.yml", "config.json"] {
        let path = gopath::join2(home, name);
        match std::fs::symlink_metadata(&path) {
            Ok(_) => return Ok(path),
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(err),
        }
    }
    std::fs::DirBuilder::new().recursive(true).mode(0o700).create(home)?;
    let path = gopath::join2(home, "config.yaml");
    let mut file = match OpenOptions::new().write(true).create_new(true).mode(0o600).open(&path) {
        Ok(file) => file,
        Err(err) if err.kind() == io::ErrorKind::AlreadyExists => return Ok(path),
        Err(err) => return Err(err),
    };
    file.write_all(EXAMPLE_YAML.as_bytes())?;
    Ok(path)
}
