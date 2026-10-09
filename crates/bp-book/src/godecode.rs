//! Go `encoding/json` decoding semantics for the structs bp reads.
//!
//! Not a port of one Go file: this reproduces what `json.Unmarshal` does to
//! the Go structs in `internal/cache`, `internal/codexauth` and
//! `internal/codexrpc` (and later `internal/book`), which serde cannot:
//!
//! - the whole input is syntax-checked first; invalid JSON decodes nothing;
//! - object keys match struct fields case-insensitively (Go's folding), and
//!   every matching key is applied in document order, so the last one wins
//!   and duplicate objects merge into the same struct;
//! - unknown keys are ignored;
//! - a type mismatch (`"id": 5` into a string) leaves that field alone, keeps
//!   decoding, and makes the call fail at the end, so callers that ignore the
//!   error still see the other fields;
//! - `null` leaves strings, numbers, bools and structs untouched and sets
//!   pointers (`Option`) and slices to nil;
//! - integers are parsed from the literal (`1.0` and `1e3` do not fit an int);
//! - `json.RawMessage` captures the exact bytes, `null` included;
//! - a `time.Time` field that is not a strict RFC 3339 string aborts the
//!   decode (the `UnmarshalJSON` error is returned at once);
//! - invalid UTF-8 and lone surrogates in strings become U+FFFD, one per byte.

use crate::gotime::{self, Time};

/// Go's scanner nesting limit.
const MAX_DEPTH: usize = 10_000;

/// One parsed JSON value and its byte span in the source.
#[derive(Debug, Clone)]
pub struct Node {
    pub kind: Kind,
    pub start: usize,
    pub end: usize,
}

#[derive(Debug, Clone)]
pub enum Kind {
    Null,
    Bool(bool),
    /// The literal is `src[start..end]`.
    Number,
    String(String),
    Array(Vec<Node>),
    /// Every entry in document order, duplicates included.
    Object(Vec<(String, Node)>),
}

/// Parses one JSON document the way Go's `checkValid` accepts it.
pub fn parse(src: &[u8]) -> Option<Node> {
    let mut parser = Parser {
        src,
        at: 0,
        depth: 0,
    };
    parser.space();
    let node = parser.value()?;
    parser.space();
    (parser.at == src.len()).then_some(node)
}

/// An open container while parsing.
enum Frame {
    Array {
        start: usize,
        items: Vec<Node>,
    },
    Object {
        start: usize,
        entries: Vec<(String, Node)>,
        key: String,
    },
}

/// Drops nested containers without recursion (see [`Parser::value`]).
impl Drop for Kind {
    fn drop(&mut self) {
        let mut stack: Vec<Node> = match self {
            Kind::Array(items) if !items.is_empty() => std::mem::take(items),
            Kind::Object(entries) if !entries.is_empty() => std::mem::take(entries)
                .into_iter()
                .map(|(_, n)| n)
                .collect(),
            _ => return,
        };
        while let Some(mut node) = stack.pop() {
            match &mut node.kind {
                Kind::Array(items) => stack.append(items),
                Kind::Object(entries) => stack.extend(entries.drain(..).map(|(_, n)| n)),
                _ => {}
            }
        }
    }
}

struct Parser<'a> {
    src: &'a [u8],
    at: usize,
    depth: usize,
}

impl Parser<'_> {
    fn space(&mut self) {
        while let Some(b' ' | b'\t' | b'\n' | b'\r') = self.src.get(self.at) {
            self.at += 1;
        }
    }

    fn literal(&mut self, word: &[u8]) -> bool {
        if self.src[self.at..].starts_with(word) {
            self.at += word.len();
            true
        } else {
            false
        }
    }

    /// Parses one value. Containers are tracked on an explicit stack, not
    /// by recursion: Go accepts 10000 levels, which would overflow a thread
    /// stack here.
    fn value(&mut self) -> Option<Node> {
        let mut stack: Vec<Frame> = Vec::new();
        loop {
            // Parse one value, or open a container.
            let start = self.at;
            let mut node = match *self.src.get(self.at)? {
                b'n' => Node {
                    kind: self.literal(b"null").then_some(Kind::Null)?,
                    start,
                    end: self.at,
                },
                b't' => Node {
                    kind: self.literal(b"true").then_some(Kind::Bool(true))?,
                    start,
                    end: self.at,
                },
                b'f' => Node {
                    kind: self.literal(b"false").then_some(Kind::Bool(false))?,
                    start,
                    end: self.at,
                },
                b'"' => {
                    let text = self.string()?;
                    Node {
                        kind: Kind::String(text),
                        start,
                        end: self.at,
                    }
                }
                b'-' | b'0'..=b'9' => {
                    self.number()?;
                    Node {
                        kind: Kind::Number,
                        start,
                        end: self.at,
                    }
                }
                b'[' => {
                    self.enter()?;
                    self.at += 1;
                    self.space();
                    if self.src.get(self.at) == Some(&b']') {
                        self.at += 1;
                        self.depth -= 1;
                        Node {
                            kind: Kind::Array(Vec::new()),
                            start,
                            end: self.at,
                        }
                    } else {
                        stack.push(Frame::Array {
                            start,
                            items: Vec::new(),
                        });
                        self.space();
                        continue;
                    }
                }
                b'{' => {
                    self.enter()?;
                    self.at += 1;
                    self.space();
                    if self.src.get(self.at) == Some(&b'}') {
                        self.at += 1;
                        self.depth -= 1;
                        Node {
                            kind: Kind::Object(Vec::new()),
                            start,
                            end: self.at,
                        }
                    } else {
                        let key = self.key()?;
                        stack.push(Frame::Object {
                            start,
                            entries: Vec::new(),
                            key,
                        });
                        continue;
                    }
                }
                _ => return None,
            };
            // Attach the finished value to its container, closing every
            // container that ends right after it.
            loop {
                let Some(frame) = stack.last_mut() else {
                    return Some(node);
                };
                self.space();
                let close = match frame {
                    Frame::Array { items, .. } => {
                        items.push(node);
                        match self.src.get(self.at)? {
                            b',' => false,
                            b']' => true,
                            _ => return None,
                        }
                    }
                    Frame::Object { entries, key, .. } => {
                        entries.push((std::mem::take(key), node));
                        match self.src.get(self.at)? {
                            b',' => false,
                            b'}' => true,
                            _ => return None,
                        }
                    }
                };
                self.at += 1;
                if !close {
                    self.space();
                    if let Some(Frame::Object { key, .. }) = stack.last_mut() {
                        *key = self.key()?;
                    }
                    break;
                }
                self.depth -= 1;
                node = match stack.pop()? {
                    Frame::Array { start, items } => Node {
                        kind: Kind::Array(items),
                        start,
                        end: self.at,
                    },
                    Frame::Object { start, entries, .. } => Node {
                        kind: Kind::Object(entries),
                        start,
                        end: self.at,
                    },
                };
            }
            self.space();
        }
    }

    /// Scans an object key and its colon, leaving `at` on the value.
    fn key(&mut self) -> Option<String> {
        if self.src.get(self.at) != Some(&b'"') {
            return None;
        }
        let key = self.string()?;
        self.space();
        if self.src.get(self.at) != Some(&b':') {
            return None;
        }
        self.at += 1;
        self.space();
        Some(key)
    }

    fn enter(&mut self) -> Option<()> {
        self.depth += 1;
        (self.depth <= MAX_DEPTH).then_some(())
    }

    fn number(&mut self) -> Option<()> {
        let src = self.src;
        let digit = |i: usize| src.get(i).is_some_and(u8::is_ascii_digit);
        if src[self.at] == b'-' {
            self.at += 1;
        }
        match src.get(self.at)? {
            b'0' => self.at += 1,
            b'1'..=b'9' => {
                while digit(self.at) {
                    self.at += 1;
                }
            }
            _ => return None,
        }
        if src.get(self.at) == Some(&b'.') {
            self.at += 1;
            if !digit(self.at) {
                return None;
            }
            while digit(self.at) {
                self.at += 1;
            }
        }
        if let Some(b'e' | b'E') = src.get(self.at) {
            self.at += 1;
            if let Some(b'+' | b'-') = src.get(self.at) {
                self.at += 1;
            }
            if !digit(self.at) {
                return None;
            }
            while digit(self.at) {
                self.at += 1;
            }
        }
        Some(())
    }

    /// Scans and unquotes a string starting at the opening quote.
    fn string(&mut self) -> Option<String> {
        let src = self.src;
        self.at += 1;
        let mut out: Vec<u8> = Vec::new();
        loop {
            let c = *src.get(self.at)?;
            match c {
                b'"' => {
                    self.at += 1;
                    break;
                }
                b'\\' => {
                    let e = *src.get(self.at + 1)?;
                    self.at += 2;
                    match e {
                        b'"' | b'\\' | b'/' => out.push(e),
                        b'b' => out.push(0x08),
                        b'f' => out.push(0x0c),
                        b'n' => out.push(b'\n'),
                        b'r' => out.push(b'\r'),
                        b't' => out.push(b'\t'),
                        b'u' => {
                            let first = self.hex4()?;
                            let mut ch = None;
                            if (0xD800..0xDC00).contains(&first)
                                && src.get(self.at) == Some(&b'\\')
                                && src.get(self.at + 1) == Some(&b'u')
                            {
                                let save = self.at;
                                self.at += 2;
                                let second = self.hex4()?;
                                if (0xDC00..0xE000).contains(&second) {
                                    ch = char::from_u32(
                                        0x10000 + ((first - 0xD800) << 10) + (second - 0xDC00),
                                    );
                                } else {
                                    // Not a pair: the first becomes U+FFFD and
                                    // the second escape is decoded on its own.
                                    self.at = save;
                                }
                            } else {
                                ch = char::from_u32(first);
                            }
                            let ch = ch.unwrap_or('\u{FFFD}');
                            let mut buf = [0u8; 4];
                            out.extend_from_slice(ch.encode_utf8(&mut buf).as_bytes());
                        }
                        _ => return None,
                    }
                }
                0..=0x1f => return None,
                _ => {
                    out.push(c);
                    self.at += 1;
                }
            }
        }
        Some(utf8_go(&out))
    }

    fn hex4(&mut self) -> Option<u32> {
        let part = self.src.get(self.at..self.at + 4)?;
        let mut value = 0u32;
        for &c in part {
            value = value * 16 + (c as char).to_digit(16)?;
        }
        self.at += 4;
        Some(value)
    }
}

/// Go's string conversion of decoded bytes: each byte of an invalid UTF-8
/// sequence becomes one U+FFFD.
pub fn utf8_go(bytes: &[u8]) -> String {
    match std::str::from_utf8(bytes) {
        Ok(s) => s.to_owned(),
        Err(_) => {
            let mut out = String::with_capacity(bytes.len() + 8);
            let mut rest = bytes;
            while !rest.is_empty() {
                match std::str::from_utf8(rest) {
                    Ok(s) => {
                        out.push_str(s);
                        break;
                    }
                    Err(err) => {
                        let valid = err.valid_up_to();
                        out.push_str(std::str::from_utf8(&rest[..valid]).unwrap_or_default());
                        out.push('\u{FFFD}');
                        rest = &rest[valid + 1..];
                    }
                }
            }
            out
        }
    }
}

/// Whether a JSON key selects the struct field named `field` (ASCII), using
/// Go's case folding (`unicode.ToUpper(unicode.ToLower(r))` per rune), which
/// also folds U+017F to `S` and U+212A (Kelvin) to `K`.
pub fn key_matches(key: &str, field: &str) -> bool {
    if key == field {
        return true;
    }
    let mut want = field.bytes();
    for c in key.chars() {
        let folded = match c {
            'a'..='z' => c.to_ascii_uppercase(),
            '\u{017F}' => 'S',
            '\u{212A}' => 'K',
            _ => c,
        };
        match want.next() {
            Some(w) if folded.is_ascii() && w.to_ascii_uppercase() == folded as u8 => {}
            _ => return false,
        }
    }
    want.next().is_none()
}

/// `strings.EqualFold(value, ascii)` for an ASCII `ascii`.
pub fn equal_fold_ascii(value: &str, ascii: &str) -> bool {
    key_matches(value, ascii)
        || (value.chars().count() == ascii.len()
            && value.chars().zip(ascii.bytes()).all(|(c, w)| {
                let c = match c {
                    '\u{017F}' => 's',
                    '\u{212A}' => 'k',
                    _ => c,
                };
                c.is_ascii() && (c as u8).eq_ignore_ascii_case(&w)
            }))
}

/// Decoding context: the source bytes and whether a type error was saved.
pub struct Decoder<'a> {
    pub src: &'a [u8],
    pub type_error: Option<String>,
}

impl Decoder<'_> {
    pub fn mismatch(&mut self, node: &Node, want: &str) {
        if self.type_error.is_none() {
            let got = match node.kind {
                Kind::Null => "null",
                Kind::Bool(_) => "bool",
                Kind::Number => "number",
                Kind::String(_) => "string",
                Kind::Array(_) => "array",
                Kind::Object(_) => "object",
            };
            self.type_error = Some(format!(
                "json: cannot unmarshal {got} into Go value of type {want}"
            ));
        }
    }

    pub fn raw(&self, node: &Node) -> &[u8] {
        &self.src[node.start..node.end]
    }
}

/// An error a Go `UnmarshalJSON` method returned: decoding stops at once.
#[derive(Debug, Clone)]
pub struct Abort(pub String);

/// A Go type `json.Unmarshal` can decode into.
pub trait GoDecode {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort>;
}

/// `json.Unmarshal(src, target)`. `Err` carries Go's error class; the target
/// may be partially filled either way, exactly as in Go.
pub fn unmarshal<T: GoDecode + ?Sized>(src: &[u8], target: &mut T) -> Result<(), String> {
    let Some(root) = parse(src) else {
        return Err(
            if src
                .iter()
                .all(|c| matches!(c, b' ' | b'\t' | b'\n' | b'\r'))
            {
                "unexpected end of JSON input".to_owned()
            } else {
                "invalid character in JSON input".to_owned()
            },
        );
    };
    let mut d = Decoder {
        src,
        type_error: None,
    };
    target.go_decode(&root, &mut d).map_err(|abort| abort.0)?;
    match d.type_error {
        Some(err) => Err(err),
        None => Ok(()),
    }
}

/// `json.Unmarshal(src, target) == nil`.
pub fn unmarshal_ok<T: GoDecode + ?Sized>(src: &[u8], target: &mut T) -> bool {
    unmarshal(src, target).is_ok()
}

impl GoDecode for String {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        match &node.kind {
            Kind::Null => {}
            Kind::String(s) => s.clone_into(self),
            _ => d.mismatch(node, "string"),
        }
        Ok(())
    }
}

impl GoDecode for bool {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        match node.kind {
            Kind::Null => {}
            Kind::Bool(b) => *self = b,
            _ => d.mismatch(node, "bool"),
        }
        Ok(())
    }
}

macro_rules! go_int {
    ($($ty:ty),*) => {$(
        impl GoDecode for $ty {
            fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
                match node.kind {
                    Kind::Null => {}
                    Kind::Number => {
                        let literal = std::str::from_utf8(d.raw(node)).unwrap_or_default();
                        match literal.parse::<$ty>() {
                            Ok(v) => *self = v,
                            Err(_) => d.mismatch(node, stringify!($ty)),
                        }
                    }
                    _ => d.mismatch(node, stringify!($ty)),
                }
                Ok(())
            }
        }
    )*};
}
go_int!(i64, u64, i32);

impl GoDecode for f64 {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        match node.kind {
            Kind::Null => {}
            Kind::Number => {
                let literal = std::str::from_utf8(d.raw(node)).unwrap_or_default();
                match literal.parse::<f64>() {
                    Ok(v) if v.is_finite() => *self = v,
                    _ => d.mismatch(node, "float64"),
                }
            }
            _ => d.mismatch(node, "float64"),
        }
        Ok(())
    }
}

/// A Go pointer field: `null` sets nil; a value allocates (or reuses) the target.
impl<T: GoDecode + Default> GoDecode for Option<T> {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        if let Kind::Null = node.kind {
            *self = None;
            return Ok(());
        }
        self.get_or_insert_with(T::default).go_decode(node, d)
    }
}

/// A Go slice: `null` sets nil; an array reuses existing elements.
impl<T: GoDecode + Default> GoDecode for Vec<T> {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        match &node.kind {
            Kind::Null => self.clear(),
            Kind::Array(items) => {
                for (i, item) in items.iter().enumerate() {
                    if i == self.len() {
                        self.push(T::default());
                    }
                    self[i].go_decode(item, d)?;
                }
                self.truncate(items.len());
            }
            _ => d.mismatch(node, "slice"),
        }
        Ok(())
    }
}

/// `json.RawMessage`: the exact bytes of the value; empty when the key was absent.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RawJson(pub Vec<u8>);

impl RawJson {
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }
}

impl GoDecode for RawJson {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        self.0 = d.raw(node).to_vec();
        Ok(())
    }
}

/// `time.Time`: `UnmarshalJSON` on the raw bytes, errors abort.
impl GoDecode for Time {
    fn go_decode(&mut self, node: &Node, d: &mut Decoder<'_>) -> Result<(), Abort> {
        let raw = d.raw(node);
        if raw == b"null" {
            return Ok(());
        }
        if raw.len() < 2 || raw[0] != b'"' || raw[raw.len() - 1] != b'"' {
            return Err(Abort(
                "Time.UnmarshalJSON: input is not a JSON string".to_owned(),
            ));
        }
        match gotime::parse_rfc3339_strict(&raw[1..raw.len() - 1]) {
            Some(t) => {
                *self = t;
                Ok(())
            }
            None => Err(Abort("parsing time as RFC 3339 failed".to_owned())),
        }
    }
}

/// Implements [`GoDecode`] for a struct: `go_struct!(Type { field: "jsonName", … })`.
/// Struct types must implement `Default` (Go zero value).
#[macro_export]
macro_rules! go_struct {
    ($ty:ty { $($field:ident : $name:literal),* $(,)? }) => {
        impl $crate::godecode::GoDecode for $ty {
            fn go_decode(
                &mut self,
                node: &$crate::godecode::Node,
                d: &mut $crate::godecode::Decoder<'_>,
            ) -> Result<(), $crate::godecode::Abort> {
                match &node.kind {
                    $crate::godecode::Kind::Null => Ok(()),
                    $crate::godecode::Kind::Object(entries) => {
                        #[allow(unused_variables)]
                        for (key, value) in entries {
                            $(
                                if $crate::godecode::key_matches(key, $name) {
                                    $crate::godecode::GoDecode::go_decode(&mut self.$field, value, d)?;
                                    continue;
                                }
                            )*
                        }
                        Ok(())
                    }
                    _ => {
                        d.mismatch(node, stringify!($ty));
                        Ok(())
                    }
                }
            }
        }
    };
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Default, Debug)]
    struct Inner {
        a: String,
        b: i64,
    }
    go_struct!(Inner { a: "a", b: "b" });

    #[derive(Debug)]
    struct Outer {
        kind: String,
        inner: Option<Inner>,
        raw: RawJson,
        list: Vec<String>,
        at: Time,
        flag: bool,
    }
    impl Outer {
        fn default_inner() -> Self {
            Outer {
                kind: String::new(),
                inner: None,
                raw: RawJson::default(),
                list: Vec::new(),
                at: gotime::zero(),
                flag: false,
            }
        }
    }
    go_struct!(Outer {
        kind: "type",
        inner: "inner",
        raw: "raw",
        list: "list",
        at: "at",
        flag: "flag"
    });

    fn decode(src: &str) -> (Outer, Result<(), String>) {
        let mut out = Outer::default_inner();
        let result = unmarshal(src.as_bytes(), &mut out);
        (out, result)
    }

    #[test]
    fn case_insensitive_last_wins_and_merges() {
        let (out, result) = decode(r#"{"TYPE":"a","type":"b","inner":{"a":"x"},"Inner":{"b":2}}"#);
        assert!(result.is_ok());
        assert_eq!(out.kind, "b");
        let inner = out.inner.unwrap();
        assert_eq!((inner.a.as_str(), inner.b), ("x", 2));
        let (out, _) = decode("{\"f\u{212A}ag\":true,\"li\u{017F}t\":[\"x\"]}");
        assert!(!out.flag);
        assert_eq!(out.list, ["x"]);
    }

    #[test]
    fn type_errors_keep_other_fields() {
        let (out, result) = decode(r#"{"type":5,"inner":{"a":"x","b":"y"},"flag":true}"#);
        assert!(result.is_err());
        assert_eq!(out.kind, "");
        assert_eq!(out.inner.unwrap().a, "x");
        assert!(out.flag);
        let (_, result) = decode(r#"{"inner":{"b":1.0}}"#);
        assert!(result.is_err());
        let (out, result) = decode(r#"{"inner":{"b":-0}}"#);
        assert!(result.is_ok());
        assert_eq!(out.inner.unwrap().b, 0);
    }

    #[test]
    fn null_raw_and_time() {
        let (out, result) = decode(r#"{"type":null,"inner":null,"raw":null,"list":null}"#);
        assert!(result.is_ok());
        assert!(out.inner.is_none());
        assert_eq!(out.raw.0, b"null");
        let (out, _) = decode(r#"{"raw": { "x" : [1, 2] } }"#);
        assert_eq!(out.raw.0, br#"{ "x" : [1, 2] }"#);
        let (out, result) = decode(r#"{"at":"2026-09-24T12:00:00Z","type":"x"}"#);
        assert!(result.is_ok());
        assert_eq!(gotime::format_rfc3339_nano(&out.at), "2026-09-24T12:00:00Z");
        let (out, result) = decode(r#"{"type":"x","at":"yesterday","flag":true}"#);
        assert!(result.is_err());
        assert_eq!(out.kind, "x");
        assert!(!out.flag, "decoding stops at a time.Time error");
        assert!(decode(r#"{"at":5}"#).1.is_err());
        assert!(decode(r#"{"at":"2026-09-24T12:00:00\u005a"}"#).1.is_err());
    }

    #[test]
    fn syntax() {
        for bad in [
            "",
            "{",
            "{} x",
            "[1,]",
            "01",
            "1.",
            "\"\t\"",
            "{\"a\" 1}",
            "nul",
            "\"\\x\"",
        ] {
            assert!(parse(bad.as_bytes()).is_none(), "{bad:?}");
        }
        for good in [" {} ", "-0", "1e5", "[1, \"a\", null, true]", "\"\\ud800\""] {
            assert!(parse(good.as_bytes()).is_some(), "{good:?}");
        }
        let node = parse(b"\"\\ud800x\\ud83d\\ude00\"").unwrap();
        match &node.kind {
            Kind::String(s) => assert_eq!(s, "\u{FFFD}x\u{1F600}"),
            _ => panic!(),
        }
        let node = parse(b"\"\xe2\x82\"").unwrap();
        match &node.kind {
            Kind::String(s) => assert_eq!(s, "\u{FFFD}\u{FFFD}"),
            _ => panic!(),
        }
        assert!(parse(&vec![b'['; MAX_DEPTH + 1]).is_none());
        // Go's limit itself parses, decodes and drops without recursion.
        let mut deep = vec![b'['; MAX_DEPTH];
        deep.extend(vec![b']'; MAX_DEPTH]);
        assert!(parse(&deep).is_some());
        let mut deep = b"{\"a\":".repeat(MAX_DEPTH);
        deep.extend(b"1".iter().chain(vec![b'}'; MAX_DEPTH].iter()));
        let mut raw = RawJson::default();
        assert!(unmarshal_ok(&deep, &mut raw));
        assert_eq!(raw.as_bytes(), &deep[..]);
    }

    #[test]
    fn equal_fold() {
        assert!(equal_fold_ascii("Session_Meta", "session_meta"));
        assert!(equal_fold_ascii("\u{017F}ession_meta", "session_meta"));
        assert!(!equal_fold_ascii("session_meta2", "session_meta"));
        assert!(key_matches("TyPe", "type"));
        assert!(!key_matches("types", "type"));
    }
}
