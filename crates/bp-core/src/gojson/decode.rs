//! Lenient decoding with Go `encoding/json` `Unmarshal` semantics.
//!
//! Go differs from a plain `serde_json::from_slice` in ways bp files depend on:
//!
//! * invalid UTF-8 inside strings becomes U+FFFD (one per invalid byte) and an
//!   unpaired UTF-16 surrogate escape becomes U+FFFD, instead of an error;
//! * struct fields match keys case-insensitively (an exact match wins);
//! * a later duplicate key wins, and a repeated object for a struct or map
//!   field is merged into the earlier one;
//! * `null` for a struct field leaves it unset;
//! * numbers decoded into `any` are float64.
//!
//! [`Node`] keeps a document in source order (with duplicates) so these rules
//! can be applied; [`Shape`] tells the decoder which objects are Go structs.

use std::borrow::Cow;
use std::fmt;

use serde::de::{self, DeserializeOwned, Deserializer, MapAccess, SeqAccess, Visitor};
use serde_json::{Map, Number, Value};

use super::Error;
use super::format::decode_rune;

/// A JSON document in source order, duplicates included.
#[derive(Debug, Clone, PartialEq)]
pub enum Node {
    Null,
    Bool(bool),
    Number(Number),
    String(String),
    Array(Vec<Node>),
    Object(Vec<(String, Node)>),
}

impl<'de> serde::Deserialize<'de> for Node {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        deserializer.deserialize_any(NodeVisitor)
    }
}

struct NodeVisitor;

impl<'de> Visitor<'de> for NodeVisitor {
    type Value = Node;
    fn expecting(&self, f: &mut fmt::Formatter) -> fmt::Result {
        f.write_str("any JSON value")
    }
    fn visit_bool<E>(self, v: bool) -> Result<Node, E> {
        Ok(Node::Bool(v))
    }
    fn visit_i64<E>(self, v: i64) -> Result<Node, E> {
        Ok(Node::Number(v.into()))
    }
    fn visit_u64<E>(self, v: u64) -> Result<Node, E> {
        Ok(Node::Number(v.into()))
    }
    fn visit_f64<E: de::Error>(self, v: f64) -> Result<Node, E> {
        Number::from_f64(v)
            .map(Node::Number)
            .ok_or_else(|| E::custom("number out of range"))
    }
    fn visit_str<E>(self, v: &str) -> Result<Node, E> {
        Ok(Node::String(v.to_string()))
    }
    fn visit_string<E>(self, v: String) -> Result<Node, E> {
        Ok(Node::String(v))
    }
    fn visit_unit<E>(self) -> Result<Node, E> {
        Ok(Node::Null)
    }
    fn visit_none<E>(self) -> Result<Node, E> {
        Ok(Node::Null)
    }
    fn visit_some<D: Deserializer<'de>>(self, d: D) -> Result<Node, D::Error> {
        d.deserialize_any(self)
    }
    fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Node, A::Error> {
        let mut out = Vec::new();
        while let Some(v) = seq.next_element()? {
            out.push(v);
        }
        Ok(Node::Array(out))
    }
    fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Node, A::Error> {
        let mut out = Vec::new();
        while let Some((k, v)) = map.next_entry::<String, Node>()? {
            out.push((k, v));
        }
        Ok(Node::Object(out))
    }
}

/// How a JSON value maps onto the Go type it is decoded into.
#[derive(Debug, Clone, Copy)]
pub enum Shape {
    /// Kept verbatim (integers stay exact). Use for typed Rust fields whose
    /// Go counterpart is a scalar or does not need folding.
    Any,
    /// Go `any`: every number becomes float64, objects are replaced, not merged.
    GoAny,
    /// A Go struct: keys fold case-insensitively onto `fields`, `null` field
    /// values are dropped, repeated objects merge. Unknown keys are kept as-is
    /// so a `#[serde(flatten)]` catch-all can preserve them.
    Struct(&'static [Field]),
    /// A Go slice/array of the inner shape.
    Array(&'static Shape),
    /// A Go map with exact keys; values have the inner shape.
    Map(&'static Shape),
}

/// One Go struct field: its JSON name (the tag) and value shape.
#[derive(Debug, Clone, Copy)]
pub struct Field {
    pub name: &'static str,
    pub shape: Shape,
}

impl Field {
    /// A field whose value needs no further folding.
    pub const fn any(name: &'static str) -> Self {
        Field {
            name,
            shape: Shape::Any,
        }
    }
    pub const fn new(name: &'static str, shape: Shape) -> Self {
        Field { name, shape }
    }
}

/// Replaces what Go's decoder would replace before handing the text to serde:
/// invalid UTF-8 bytes become U+FFFD (one per byte, like `utf8.DecodeRune`)
/// and unpaired surrogate escapes become `�`.
pub fn sanitize(src: &[u8]) -> Cow<'_, [u8]> {
    if std::str::from_utf8(src).is_ok() && !src.windows(2).any(|w| w == b"\\u") {
        return Cow::Borrowed(src);
    }
    let mut out = Vec::with_capacity(src.len() + 8);
    let mut in_string = false;
    let mut i = 0;
    while i < src.len() {
        let c = src[i];
        if c >= 0x80 {
            match decode_rune(&src[i..]) {
                Some((_, size)) => {
                    out.extend_from_slice(&src[i..i + size]);
                    i += size;
                }
                None => {
                    out.extend_from_slice("\u{fffd}".as_bytes());
                    i += 1;
                }
            }
            continue;
        }
        if !in_string {
            if c == b'"' {
                in_string = true;
            }
            out.push(c);
            i += 1;
            continue;
        }
        match c {
            b'"' => {
                in_string = false;
                out.push(c);
                i += 1;
            }
            b'\\' if src.get(i + 1) == Some(&b'u') => {
                let Some(first) = hex4(src, i + 2) else {
                    out.push(c);
                    i += 1;
                    continue;
                };
                if (0xd800..0xdc00).contains(&first) {
                    let second = (src.get(i + 6) == Some(&b'\\') && src.get(i + 7) == Some(&b'u'))
                        .then(|| hex4(src, i + 8))
                        .flatten();
                    if second.is_some_and(|s| (0xdc00..0xe000).contains(&s)) {
                        out.extend_from_slice(&src[i..i + 12]);
                        i += 12;
                    } else {
                        out.extend_from_slice(b"\\ufffd");
                        i += 6;
                    }
                } else if (0xdc00..0xe000).contains(&first) {
                    out.extend_from_slice(b"\\ufffd");
                    i += 6;
                } else {
                    out.extend_from_slice(&src[i..i + 6]);
                    i += 6;
                }
            }
            b'\\' => {
                out.push(c);
                if let Some(&next) = src.get(i + 1)
                    && next < 0x80
                {
                    out.push(next);
                    i += 2;
                } else {
                    i += 1;
                }
            }
            _ => {
                out.push(c);
                i += 1;
            }
        }
    }
    Cow::Owned(out)
}

fn hex4(src: &[u8], at: usize) -> Option<u32> {
    let digits = src.get(at..at + 4)?;
    let text = std::str::from_utf8(digits).ok()?;
    if !text.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    u32::from_str_radix(text, 16).ok()
}

/// Maps a serde_json error to Go's wording where Go's is predictable.
pub(crate) fn go_syntax_message(e: &serde_json::Error) -> String {
    if e.classify() == serde_json::error::Category::Eof {
        "unexpected end of JSON input".to_string()
    } else {
        e.to_string()
    }
}

/// Parses JSON (after [`sanitize`]) into a source-ordered [`Node`].
pub fn parse_node(src: &[u8]) -> Result<Node, Error> {
    let clean = sanitize(src);
    serde_json::from_slice::<Node>(&clean).map_err(|e| match e.classify() {
        serde_json::error::Category::Data => Error::Message(e.to_string()),
        _ => Error::Syntax(go_syntax_message(&e)),
    })
}

/// Decodes like Go `json.Unmarshal(data, &v)` with `v any`: float64 numbers,
/// last duplicate key wins, invalid UTF-8 replaced.
pub fn decode_any(src: &[u8]) -> Result<Value, Error> {
    Ok(node_to_value(parse_node(src)?, Shape::GoAny))
}

/// Decodes like Go `json.Unmarshal` into a struct described by `shape`, then
/// deserializes `T` from the folded value. `T` should use `#[serde(default)]`
/// for fields that may be missing (Go leaves them zero).
pub fn decode_lenient<T: DeserializeOwned>(src: &[u8], shape: Shape) -> Result<T, Error> {
    let value = node_to_value(parse_node(src)?, shape);
    serde_json::from_value(value).map_err(|e| Error::Message(e.to_string()))
}

/// Converts a [`Node`] to a `serde_json::Value` applying Go's decoding rules
/// for `shape`.
pub fn node_to_value(node: Node, shape: Shape) -> Value {
    match (node, shape) {
        (Node::Null, _) => Value::Null,
        (Node::Bool(b), _) => Value::Bool(b),
        (Node::Number(n), Shape::GoAny) => n
            .as_f64()
            .and_then(Number::from_f64)
            .map(Value::Number)
            .unwrap_or(Value::Number(n)),
        (Node::Number(n), _) => Value::Number(n),
        (Node::String(s), _) => Value::String(s),
        (Node::Array(items), shape) => {
            let inner = match shape {
                Shape::Array(inner) => *inner,
                Shape::GoAny => Shape::GoAny,
                _ => Shape::Any,
            };
            Value::Array(items.into_iter().map(|n| node_to_value(n, inner)).collect())
        }
        (Node::Object(entries), Shape::Struct(fields)) => {
            let mut map = Map::new();
            for (key, value) in entries {
                match lookup_field(fields, &key) {
                    Some(field) => {
                        if value == Node::Null {
                            // Go: null resets slices and maps to nil and is a
                            // no-op for structs and scalars (pointer fields
                            // would become nil; a repeated key is the only
                            // case where that differs).
                            if matches!(field.shape, Shape::Array(_) | Shape::Map(_)) {
                                map.remove(field.name);
                            }
                            continue;
                        }
                        let merge = matches!(field.shape, Shape::Struct(_) | Shape::Map(_));
                        let new = node_to_value(value, field.shape);
                        insert_merged(&mut map, field.name.to_string(), new, merge);
                    }
                    None => {
                        map.insert(key, node_to_value(value, Shape::Any));
                    }
                }
            }
            Value::Object(map)
        }
        (Node::Object(entries), Shape::Map(inner)) => {
            let mut map = Map::new();
            for (key, value) in entries {
                map.insert(key, node_to_value(value, *inner));
            }
            Value::Object(map)
        }
        (Node::Object(entries), shape) => {
            let inner = if matches!(shape, Shape::GoAny) {
                Shape::GoAny
            } else {
                Shape::Any
            };
            let mut map = Map::new();
            for (key, value) in entries {
                map.insert(key, node_to_value(value, inner));
            }
            Value::Object(map)
        }
    }
}

fn insert_merged(map: &mut Map<String, Value>, key: String, new: Value, merge: bool) {
    if merge
        && let (Some(Value::Object(old)), Value::Object(add)) = (map.get_mut(&key), &new)
    {
        for (k, v) in add {
            old.insert(k.clone(), v.clone());
        }
        return;
    }
    map.insert(key, new);
}

/// Go's struct field lookup: an exact name match wins, otherwise the first
/// field (declaration order) whose name folds equal.
pub fn lookup_field<'a>(fields: &'a [Field], key: &str) -> Option<&'a Field> {
    fields
        .iter()
        .find(|f| f.name == key)
        .or_else(|| fields.iter().find(|f| fold_eq(f.name, key)))
}

/// Case-insensitive lookup in a decoded object, preferring an exact match,
/// like Go's struct field matching.
pub fn get_fold<'a>(map: &'a Map<String, Value>, key: &str) -> Option<&'a Value> {
    map.get(key)
        .or_else(|| map.iter().find(|(k, _)| fold_eq(k, key)).map(|(_, v)| v))
}

/// Reports whether `a` and `b` are equal under Go's JSON key folding
/// (`bytes.EqualFold`: ASCII case-insensitive plus Unicode simple folding).
pub fn fold_eq(a: &str, b: &str) -> bool {
    fold_name(a) == fold_name(b)
}

/// Go's `foldName`: ASCII letters upper-cased, other runes mapped to the
/// smallest rune of their simple case-fold orbit.
pub fn fold_name(s: &str) -> String {
    s.chars()
        .map(|c| {
            if c.is_ascii() {
                c.to_ascii_uppercase()
            } else {
                fold_rune(c)
            }
        })
        .collect()
}

fn fold_rune(c: char) -> char {
    fn single(mut it: impl Iterator<Item = char>) -> Option<char> {
        let first = it.next()?;
        it.next().is_none().then_some(first)
    }
    let mut best = c;
    let lower = single(c.to_lowercase());
    let upper = single(c.to_uppercase());
    for candidate in [
        lower,
        upper,
        lower.and_then(|l| single(l.to_uppercase())),
        upper.and_then(|u| single(u.to_lowercase())),
    ]
    .into_iter()
    .flatten()
    {
        best = best.min(candidate);
    }
    best
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sanitize_replaces_like_go() {
        let src = b"{\"a\":\"x\xe2\x82y\xff\"}";
        assert_eq!(
            &*sanitize(src),
            "{\"a\":\"x\u{fffd}\u{fffd}y\u{fffd}\"}".as_bytes()
        );
        let src = r#"["\ud800","\udc00x","😀","\ud800A"]"#.as_bytes();
        let value = decode_any(src).unwrap();
        assert_eq!(
            value,
            serde_json::json!(["\u{fffd}", "\u{fffd}x", "\u{1f600}", "\u{fffd}A"])
        );
    }

    #[test]
    fn struct_folding() {
        const INNER: Shape = Shape::Struct(&[Field::any("x"), Field::any("y")]);
        const SHAPE: Shape = Shape::Struct(&[
            Field::any("name"),
            Field::new("inner", INNER),
            Field::any("gone"),
        ]);
        let v = node_to_value(
            parse_node(br#"{"NAME":"a","name":"b","Inner":{"x":1},"inner":{"Y":2},"gone":null,"extra":1}"#)
                .unwrap(),
            SHAPE,
        );
        assert_eq!(
            v,
            serde_json::json!({"name":"b","inner":{"x":1,"y":2},"extra":1})
        );
        assert!(fold_eq("k", "\u{212a}"));
        assert!(fold_eq("s", "\u{17f}"));
    }
}
