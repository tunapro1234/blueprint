//! Go `encoding/json`-compatible JSON (new module; no single Go origin).
//!
//! Encoding goes through a serde `Serializer` that reproduces Go's bytes:
//!
//! * Rust structs keep declaration order (Go structs); maps — including
//!   `serde_json::Value` objects, `BTreeMap`, `HashMap` — are sorted by key
//!   like Go maps. A struct with `#[serde(flatten)]` is reported to serde as a
//!   map and therefore sorted; use [`Options::sort_map_keys`]`(false)` with
//!   `BTreeMap` fields if the Go struct order matters for such a type.
//! * Strings escape like Go: `< > &` (unless HTML escaping is
//!   off), `   `, `\b \f \n \r \t`, other controls as `\u00XX`.
//! * Floats use Go's JSON float format (`1e+21`, `1e-7`, integers without
//!   `.0`); NaN/Inf are errors. `[]byte` (`serialize_bytes`) is base64.
//! * `serde_json::value::RawValue` is compacted and HTML-escaped like a Go
//!   `json.RawMessage`.
//! * Indented output is produced like Go: compact encoding followed by
//!   `json.Indent`.
//!
//! Decoding helpers live in [`decode`]; Go `time` helpers in [`time`].

mod decode;
mod format;
mod ser;
pub mod time;

use std::io::Write;

use serde::Serialize;

pub use decode::{
    Field, Node, Shape, decode_any, decode_lenient, fold_eq, fold_name, get_fold, lookup_field,
    node_to_value, parse_node, sanitize,
};
pub(crate) use decode::go_syntax_message;
pub use format::{compact, format_float, format_float32, indent, write_string, write_string_bytes};
pub use time::{GoDuration, format_duration, format_rfc3339_nano, parse_duration, zero_time};

/// Errors from encoding or lenient decoding.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Go: `json: unsupported value: NaN`.
    #[error("json: unsupported value: {0}")]
    UnsupportedValue(String),
    /// Invalid JSON text (Go's syntax errors).
    #[error("{0}")]
    Syntax(String),
    /// Any other serialization/deserialization failure.
    #[error("{0}")]
    Message(String),
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

/// Encoding options; the defaults match `json.Marshal`.
#[derive(Debug, Clone)]
pub struct Options {
    escape_html: bool,
    sort_map_keys: bool,
    prefix: String,
    indent: String,
}

impl Default for Options {
    fn default() -> Self {
        Options {
            escape_html: true,
            sort_map_keys: true,
            prefix: String::new(),
            indent: String::new(),
        }
    }
}

impl Options {
    pub fn new() -> Self {
        Self::default()
    }
    /// Go `Encoder.SetEscapeHTML`.
    pub fn escape_html(mut self, on: bool) -> Self {
        self.escape_html = on;
        self
    }
    /// Go `Encoder.SetIndent` / `MarshalIndent` arguments.
    pub fn indent(mut self, prefix: &str, indent: &str) -> Self {
        self.prefix = prefix.to_string();
        self.indent = indent.to_string();
        self
    }
    /// Sort map keys (Go map semantics, default). Turn off only to emit
    /// maps in their iteration order.
    pub fn sort_map_keys(mut self, on: bool) -> Self {
        self.sort_map_keys = on;
        self
    }

    /// Encodes `value` without a trailing newline (`json.Marshal` /
    /// `json.MarshalIndent`).
    pub fn to_vec<T: ?Sized + Serialize>(&self, value: &T) -> Result<Vec<u8>, Error> {
        let mut out = Vec::with_capacity(128);
        value.serialize(&mut ser::Serializer {
            out: &mut out,
            opts: ser::Opts {
                escape_html: self.escape_html,
                sort_map_keys: self.sort_map_keys,
            },
        })?;
        if self.prefix.is_empty() && self.indent.is_empty() {
            return Ok(out);
        }
        let mut indented = Vec::with_capacity(out.len() * 2);
        format::append_indent(&mut indented, &out, &self.prefix, &self.indent);
        Ok(indented)
    }
}

/// Go `json.Marshal`.
pub fn to_vec<T: ?Sized + Serialize>(value: &T) -> Result<Vec<u8>, Error> {
    Options::default().to_vec(value)
}

/// Go `json.Marshal`, as a `String`.
pub fn to_string<T: ?Sized + Serialize>(value: &T) -> Result<String, Error> {
    Ok(String::from_utf8(to_vec(value)?).expect("encoder emits UTF-8"))
}

/// Go `json.MarshalIndent(value, prefix, indent)`.
pub fn to_vec_indent<T: ?Sized + Serialize>(value: &T, prefix: &str, indent: &str) -> Result<Vec<u8>, Error> {
    Options::default().indent(prefix, indent).to_vec(value)
}

/// Go `json.NewEncoder(w)`: every [`Encoder::encode`] writes one value
/// followed by `\n`.
pub struct Encoder<W: Write> {
    writer: W,
    options: Options,
}

impl<W: Write> Encoder<W> {
    pub fn new(writer: W) -> Self {
        Encoder {
            writer,
            options: Options::default(),
        }
    }
    /// `Encoder.SetEscapeHTML`.
    pub fn set_escape_html(&mut self, on: bool) {
        self.options.escape_html = on;
    }
    /// `Encoder.SetIndent`.
    pub fn set_indent(&mut self, prefix: &str, indent: &str) {
        self.options.prefix = prefix.to_string();
        self.options.indent = indent.to_string();
    }
    /// `Encoder.Encode`: the encoded value plus a newline, in one write.
    pub fn encode<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        let mut data = self.options.to_vec(value)?;
        data.push(b'\n');
        self.writer.write_all(&data)?;
        Ok(())
    }
    pub fn into_inner(self) -> W {
        self.writer
    }
}

/// Convenience: `Encoder` output for one value as bytes (value + `\n`).
pub fn encode_line<T: ?Sized + Serialize>(value: &T, options: &Options) -> Result<Vec<u8>, Error> {
    let mut data = options.to_vec(value)?;
    data.push(b'\n');
    Ok(data)
}

#[cfg(test)]
mod tests;
