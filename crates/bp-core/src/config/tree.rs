//! Format-aware decoding of config documents into typed values.
//!
//! Go decodes the same `overrides` struct with `yaml.v3` (KnownFields, strict
//! duplicates) or `encoding/json` (case-insensitive keys, last duplicate
//! wins, nested struct pointers merge). Both inputs are first turned into a
//! source-ordered [`Val`] tree; the readers below apply each format's rules.

use crate::gojson::{self, Node};

/// A decoded scalar/collection with its source order kept.
#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Val {
    Null,
    Bool(bool),
    Int(i128),
    Float(f64),
    Str(String),
    Seq(Vec<Val>),
    Map(Vec<(String, Val)>),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Format {
    Yaml,
    Json,
}

impl Val {
    pub(crate) fn from_node(node: Node) -> Val {
        match node {
            Node::Null => Val::Null,
            Node::Bool(b) => Val::Bool(b),
            Node::Number(n) => {
                if let Some(i) = n.as_i64() {
                    Val::Int(i.into())
                } else if let Some(u) = n.as_u64() {
                    Val::Int(u.into())
                } else {
                    Val::Float(n.as_f64().unwrap_or(f64::NAN))
                }
            }
            Node::String(s) => Val::Str(s),
            Node::Array(items) => Val::Seq(items.into_iter().map(Val::from_node).collect()),
            Node::Object(entries) => Val::Map(
                entries
                    .into_iter()
                    .map(|(k, v)| (k, Val::from_node(v)))
                    .collect(),
            ),
        }
    }

    pub(crate) fn from_yaml(value: serde_norway::Value) -> Val {
        use serde_norway::Value as Y;
        match value {
            Y::Null => Val::Null,
            Y::Bool(b) => Val::Bool(b),
            Y::Number(n) => {
                if let Some(i) = n.as_i64() {
                    Val::Int(i.into())
                } else if let Some(u) = n.as_u64() {
                    Val::Int(u.into())
                } else {
                    Val::Float(n.as_f64().unwrap_or(f64::NAN))
                }
            }
            Y::String(s) => Val::Str(s),
            Y::Sequence(items) => Val::Seq(items.into_iter().map(Val::from_yaml).collect()),
            Y::Mapping(map) => Val::Map(
                map.into_iter()
                    .map(|(k, v)| (yaml_key(k), Val::from_yaml(v)))
                    .collect(),
            ),
            Y::Tagged(tagged) => Val::from_yaml(tagged.value),
        }
    }

    /// Go's kind word for error messages.
    fn kind(&self, format: Format) -> String {
        match (format, self) {
            (Format::Json, Val::Null) => "null".into(),
            (Format::Json, Val::Bool(_)) => "bool".into(),
            (Format::Json, Val::Int(i)) => format!("number {i}"),
            (Format::Json, Val::Float(f)) => format!("number {}", gojson::format_float(*f).unwrap_or_default()),
            (Format::Json, Val::Str(_)) => "string".into(),
            (Format::Json, Val::Seq(_)) => "array".into(),
            (Format::Json, Val::Map(_)) => "object".into(),
            (Format::Yaml, Val::Null) => "!!null".into(),
            (Format::Yaml, Val::Bool(b)) => format!("!!bool `{b}`"),
            (Format::Yaml, Val::Int(i)) => format!("!!int `{i}`"),
            (Format::Yaml, Val::Float(f)) => format!("!!float `{f}`"),
            (Format::Yaml, Val::Str(s)) => format!("!!str `{}`", yaml_short(s)),
            (Format::Yaml, Val::Seq(_)) => "!!seq".into(),
            (Format::Yaml, Val::Map(_)) => "!!map".into(),
        }
    }
}

fn yaml_short(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    if chars.len() > 10 {
        format!("{}...", chars[..7].iter().collect::<String>())
    } else {
        s.to_string()
    }
}

fn yaml_key(key: serde_norway::Value) -> String {
    use serde_norway::Value as Y;
    match key {
        Y::Null => String::new(),
        Y::Bool(b) => b.to_string(),
        Y::Number(n) => n.to_string(),
        Y::String(s) => s,
        Y::Tagged(t) => yaml_key(t.value),
        other => format!("{other:?}"),
    }
}

/// Reading context: format plus the Go type/field path used in messages.
#[derive(Clone)]
pub(crate) struct Ctx {
    pub format: Format,
    /// Go struct type name of the innermost struct (e.g. `config.overrides`).
    pub type_name: &'static str,
    /// JSON field path from the root, dot separated (Go's error shape).
    pub path: String,
}

impl Ctx {
    pub(crate) fn field(&self, type_name: &'static str, key: &str) -> Ctx {
        let path = if self.path.is_empty() {
            key.to_string()
        } else {
            format!("{}.{key}", self.path)
        };
        Ctx {
            format: self.format,
            type_name,
            path,
        }
    }

    pub(crate) fn type_error(&self, value: &Val, go_type: &str) -> String {
        match self.format {
            Format::Json => {
                if self.path.is_empty() {
                    format!("json: cannot unmarshal {} into Go value of type {go_type}", value.kind(Format::Json))
                } else {
                    let short = self.type_name.rsplit('.').next().unwrap_or(self.type_name);
                    format!(
                        "json: cannot unmarshal {} into Go struct field {short}.{} of type {go_type}",
                        value.kind(Format::Json),
                        self.path
                    )
                }
            }
            Format::Yaml => format!(
                "yaml: unmarshal errors:\n  cannot unmarshal {} into {go_type}",
                value.kind(Format::Yaml)
            ),
        }
    }
}

pub(crate) type R<T> = Result<T, String>;

pub(crate) fn as_bool(v: &Val, ctx: &Ctx) -> R<bool> {
    match (v, ctx.format) {
        (Val::Bool(b), _) => Ok(*b),
        (Val::Null, _) => Ok(false),
        (Val::Str(s), Format::Yaml) => match s.as_str() {
            "y" | "Y" | "yes" | "Yes" | "YES" | "on" | "On" | "ON" => Ok(true),
            "n" | "N" | "no" | "No" | "NO" | "off" | "Off" | "OFF" => Ok(false),
            _ => Err(ctx.type_error(v, "bool")),
        },
        _ => Err(ctx.type_error(v, "bool")),
    }
}

pub(crate) fn as_int(v: &Val, ctx: &Ctx) -> R<i64> {
    match (v, ctx.format) {
        (Val::Int(i), _) => i64::try_from(*i).map_err(|_| ctx.type_error(v, "int")),
        (Val::Null, _) => Ok(0),
        (Val::Float(f), Format::Yaml) if f.fract() == 0.0 && f.abs() < 9.2e18 => Ok(*f as i64),
        _ => Err(ctx.type_error(v, "int")),
    }
}

pub(crate) fn as_string(v: &Val, ctx: &Ctx) -> R<String> {
    match (v, ctx.format) {
        (Val::Str(s), _) => Ok(s.clone()),
        (Val::Null, _) => Ok(String::new()),
        (Val::Bool(b), Format::Yaml) => Ok(b.to_string()),
        (Val::Int(i), Format::Yaml) => Ok(i.to_string()),
        (Val::Float(f), Format::Yaml) => Ok(f.to_string()),
        _ => Err(ctx.type_error(v, "string")),
    }
}

pub(crate) fn as_string_list(v: &Val, ctx: &Ctx) -> R<Vec<String>> {
    match v {
        Val::Seq(items) => items.iter().map(|item| as_string(item, ctx)).collect(),
        Val::Null => Ok(Vec::new()),
        _ => Err(ctx.type_error(v, "[]string")),
    }
}

/// Map entries in source order (JSON duplicates included).
pub(crate) fn as_map<'a>(v: &'a Val, ctx: &Ctx, go_type: &str) -> R<&'a [(String, Val)]> {
    match v {
        Val::Map(entries) => Ok(entries),
        Val::Null => Ok(&[]),
        _ => Err(ctx.type_error(v, go_type)),
    }
}

/// Resolves a struct key to its field name: YAML requires an exact match
/// (and rejects unknown keys); JSON folds case and ignores unknown keys.
pub(crate) fn struct_key(
    key: &str,
    fields: &[&'static str],
    ctx: &Ctx,
) -> R<Option<&'static str>> {
    match ctx.format {
        Format::Yaml => fields
            .iter()
            .copied()
            .find(|f| *f == key)
            .map(Some)
            .ok_or_else(|| {
                format!(
                    "yaml: unmarshal errors:\n  field {key} not found in type {}",
                    ctx.type_name
                )
            }),
        Format::Json => Ok(fields
            .iter()
            .copied()
            .find(|f| *f == key)
            .or_else(|| fields.iter().copied().find(|f| gojson::fold_eq(f, key)))),
    }
}
