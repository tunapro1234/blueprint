//! A serde `Serializer` that produces Go `encoding/json` compact output.
//!
//! Structs keep declaration order (Go struct fields); maps are sorted by key
//! (Go `map[string]T`) unless [`super::Options::sort_map_keys`] is off.

use serde::ser::{self, Serialize};

use super::Error;
use super::format::{append_compact, base64_std, format_float, format_float32, write_string};

const RAW_TOKEN: &str = "$serde_json::private::RawValue";
const NUMBER_TOKEN: &str = "$serde_json::private::Number";

#[derive(Clone, Copy)]
pub(crate) struct Opts {
    pub escape_html: bool,
    pub sort_map_keys: bool,
}

pub(crate) struct Serializer<'a> {
    pub out: &'a mut Vec<u8>,
    pub opts: Opts,
}

impl<'a> Serializer<'a> {
    fn nested(&mut self) -> Serializer<'_> {
        Serializer {
            out: self.out,
            opts: self.opts,
        }
    }
}

impl ser::Error for Error {
    fn custom<T: std::fmt::Display>(msg: T) -> Self {
        Error::Message(msg.to_string())
    }
}

impl<'a, 'b> ser::Serializer for &'b mut Serializer<'a> {
    type Ok = ();
    type Error = Error;
    type SerializeSeq = Seq<'a, 'b>;
    type SerializeTuple = Seq<'a, 'b>;
    type SerializeTupleStruct = Seq<'a, 'b>;
    type SerializeTupleVariant = Seq<'a, 'b>;
    type SerializeMap = Map<'a, 'b>;
    type SerializeStruct = Struct<'a, 'b>;
    type SerializeStructVariant = Struct<'a, 'b>;

    fn serialize_bool(self, v: bool) -> Result<(), Error> {
        self.out
            .extend_from_slice(if v { b"true" } else { b"false" });
        Ok(())
    }
    fn serialize_i8(self, v: i8) -> Result<(), Error> {
        self.serialize_i64(v.into())
    }
    fn serialize_i16(self, v: i16) -> Result<(), Error> {
        self.serialize_i64(v.into())
    }
    fn serialize_i32(self, v: i32) -> Result<(), Error> {
        self.serialize_i64(v.into())
    }
    fn serialize_i64(self, v: i64) -> Result<(), Error> {
        self.out.extend_from_slice(v.to_string().as_bytes());
        Ok(())
    }
    fn serialize_i128(self, v: i128) -> Result<(), Error> {
        self.out.extend_from_slice(v.to_string().as_bytes());
        Ok(())
    }
    fn serialize_u8(self, v: u8) -> Result<(), Error> {
        self.serialize_u64(v.into())
    }
    fn serialize_u16(self, v: u16) -> Result<(), Error> {
        self.serialize_u64(v.into())
    }
    fn serialize_u32(self, v: u32) -> Result<(), Error> {
        self.serialize_u64(v.into())
    }
    fn serialize_u64(self, v: u64) -> Result<(), Error> {
        self.out.extend_from_slice(v.to_string().as_bytes());
        Ok(())
    }
    fn serialize_u128(self, v: u128) -> Result<(), Error> {
        self.out.extend_from_slice(v.to_string().as_bytes());
        Ok(())
    }
    fn serialize_f32(self, v: f32) -> Result<(), Error> {
        self.out.extend_from_slice(format_float32(v)?.as_bytes());
        Ok(())
    }
    fn serialize_f64(self, v: f64) -> Result<(), Error> {
        self.out.extend_from_slice(format_float(v)?.as_bytes());
        Ok(())
    }
    fn serialize_char(self, v: char) -> Result<(), Error> {
        let mut buf = [0u8; 4];
        self.serialize_str(v.encode_utf8(&mut buf))
    }
    fn serialize_str(self, v: &str) -> Result<(), Error> {
        write_string(self.out, v, self.opts.escape_html);
        Ok(())
    }
    fn serialize_bytes(self, v: &[u8]) -> Result<(), Error> {
        // Go encodes []byte as a base64 string.
        write_string(self.out, &base64_std(v), self.opts.escape_html);
        Ok(())
    }
    fn serialize_none(self) -> Result<(), Error> {
        self.serialize_unit()
    }
    fn serialize_some<T: ?Sized + Serialize>(self, value: &T) -> Result<(), Error> {
        value.serialize(self)
    }
    fn serialize_unit(self) -> Result<(), Error> {
        self.out.extend_from_slice(b"null");
        Ok(())
    }
    fn serialize_unit_struct(self, _: &'static str) -> Result<(), Error> {
        self.serialize_unit()
    }
    fn serialize_unit_variant(self, _: &'static str, _: u32, variant: &'static str) -> Result<(), Error> {
        self.serialize_str(variant)
    }
    fn serialize_newtype_struct<T: ?Sized + Serialize>(self, _: &'static str, value: &T) -> Result<(), Error> {
        value.serialize(self)
    }
    fn serialize_newtype_variant<T: ?Sized + Serialize>(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        value: &T,
    ) -> Result<(), Error> {
        self.out.push(b'{');
        write_string(self.out, variant, self.opts.escape_html);
        self.out.push(b':');
        value.serialize(&mut *self)?;
        self.out.push(b'}');
        Ok(())
    }
    fn serialize_seq(self, _: Option<usize>) -> Result<Self::SerializeSeq, Error> {
        self.out.push(b'[');
        Ok(Seq {
            ser: self,
            first: true,
            variant: false,
        })
    }
    fn serialize_tuple(self, len: usize) -> Result<Self::SerializeTuple, Error> {
        self.serialize_seq(Some(len))
    }
    fn serialize_tuple_struct(self, _: &'static str, len: usize) -> Result<Self::SerializeTupleStruct, Error> {
        self.serialize_seq(Some(len))
    }
    fn serialize_tuple_variant(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleVariant, Error> {
        self.out.push(b'{');
        write_string(self.out, variant, self.opts.escape_html);
        self.out.extend_from_slice(b":[");
        Ok(Seq {
            ser: self,
            first: true,
            variant: true,
        })
    }
    fn serialize_map(self, _: Option<usize>) -> Result<Self::SerializeMap, Error> {
        Ok(Map {
            ser: self,
            entries: Vec::new(),
            key: None,
        })
    }
    fn serialize_struct(self, name: &'static str, _: usize) -> Result<Self::SerializeStruct, Error> {
        let kind = match name {
            RAW_TOKEN => StructKind::Raw,
            NUMBER_TOKEN => StructKind::Number,
            _ => {
                self.out.push(b'{');
                StructKind::Plain
            }
        };
        Ok(Struct {
            ser: self,
            first: true,
            kind,
            variant: false,
        })
    }
    fn serialize_struct_variant(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        _: usize,
    ) -> Result<Self::SerializeStructVariant, Error> {
        self.out.push(b'{');
        write_string(self.out, variant, self.opts.escape_html);
        self.out.extend_from_slice(b":{");
        Ok(Struct {
            ser: self,
            first: true,
            kind: StructKind::Plain,
            variant: true,
        })
    }
}

pub(crate) struct Seq<'a, 'b> {
    ser: &'b mut Serializer<'a>,
    first: bool,
    variant: bool,
}

impl Seq<'_, '_> {
    fn element<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        if !self.first {
            self.ser.out.push(b',');
        }
        self.first = false;
        value.serialize(&mut self.ser.nested())
    }
    fn finish(self) -> Result<(), Error> {
        self.ser.out.push(b']');
        if self.variant {
            self.ser.out.push(b'}');
        }
        Ok(())
    }
}

impl ser::SerializeSeq for Seq<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_element<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        self.element(value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}
impl ser::SerializeTuple for Seq<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_element<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        self.element(value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}
impl ser::SerializeTupleStruct for Seq<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_field<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        self.element(value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}
impl ser::SerializeTupleVariant for Seq<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_field<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        self.element(value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}

pub(crate) struct Map<'a, 'b> {
    ser: &'b mut Serializer<'a>,
    entries: Vec<(String, Vec<u8>)>,
    key: Option<String>,
}

impl ser::SerializeMap for Map<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_key<T: ?Sized + Serialize>(&mut self, key: &T) -> Result<(), Error> {
        self.key = Some(key.serialize(KeySerializer)?);
        Ok(())
    }
    fn serialize_value<T: ?Sized + Serialize>(&mut self, value: &T) -> Result<(), Error> {
        let key = self
            .key
            .take()
            .ok_or_else(|| Error::Message("map value without key".into()))?;
        let mut buf = Vec::new();
        value.serialize(&mut Serializer {
            out: &mut buf,
            opts: self.ser.opts,
        })?;
        self.entries.push((key, buf));
        Ok(())
    }
    fn end(mut self) -> Result<(), Error> {
        if self.ser.opts.sort_map_keys {
            // Go sorts the encoded key strings bytewise; a later duplicate key
            // (only possible from hand-written Serialize impls) is kept after.
            self.entries.sort_by(|a, b| a.0.cmp(&b.0));
        }
        let out = &mut *self.ser.out;
        out.push(b'{');
        for (i, (key, value)) in self.entries.iter().enumerate() {
            if i > 0 {
                out.push(b',');
            }
            write_string(out, key, self.ser.opts.escape_html);
            out.push(b':');
            out.extend_from_slice(value);
        }
        out.push(b'}');
        Ok(())
    }
}

enum StructKind {
    Plain,
    Raw,
    Number,
}

pub(crate) struct Struct<'a, 'b> {
    ser: &'b mut Serializer<'a>,
    first: bool,
    kind: StructKind,
    variant: bool,
}

impl Struct<'_, '_> {
    fn field<T: ?Sized + Serialize>(&mut self, key: &'static str, value: &T) -> Result<(), Error> {
        match self.kind {
            StructKind::Plain => {
                if !self.first {
                    self.ser.out.push(b',');
                }
                self.first = false;
                write_string(self.ser.out, key, self.ser.opts.escape_html);
                self.ser.out.push(b':');
                value.serialize(&mut self.ser.nested())
            }
            StructKind::Raw | StructKind::Number => {
                let text = value.serialize(KeySerializer)?;
                if matches!(self.kind, StructKind::Raw) {
                    // Go compacts (and HTML-escapes) json.RawMessage output.
                    super::format::validate(text.as_bytes())?;
                    append_compact(self.ser.out, text.as_bytes(), self.ser.opts.escape_html);
                } else {
                    self.ser.out.extend_from_slice(text.as_bytes());
                }
                Ok(())
            }
        }
    }
    fn finish(self) -> Result<(), Error> {
        if matches!(self.kind, StructKind::Plain) {
            self.ser.out.push(b'}');
            if self.variant {
                self.ser.out.push(b'}');
            }
        }
        Ok(())
    }
}

impl ser::SerializeStruct for Struct<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_field<T: ?Sized + Serialize>(&mut self, key: &'static str, value: &T) -> Result<(), Error> {
        self.field(key, value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}
impl ser::SerializeStructVariant for Struct<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_field<T: ?Sized + Serialize>(&mut self, key: &'static str, value: &T) -> Result<(), Error> {
        self.field(key, value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}

/// Serializes a map key (or a raw token payload) to its string form. Go
/// accepts string and integer keys; integers are quoted.
struct KeySerializer;

fn key_error() -> Error {
    Error::Message("json: unsupported map key type".into())
}

impl ser::Serializer for KeySerializer {
    type Ok = String;
    type Error = Error;
    type SerializeSeq = ser::Impossible<String, Error>;
    type SerializeTuple = ser::Impossible<String, Error>;
    type SerializeTupleStruct = ser::Impossible<String, Error>;
    type SerializeTupleVariant = ser::Impossible<String, Error>;
    type SerializeMap = ser::Impossible<String, Error>;
    type SerializeStruct = ser::Impossible<String, Error>;
    type SerializeStructVariant = ser::Impossible<String, Error>;

    fn serialize_bool(self, _: bool) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_i8(self, v: i8) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_i16(self, v: i16) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_i32(self, v: i32) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_i64(self, v: i64) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_u8(self, v: u8) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_u16(self, v: u16) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_u32(self, v: u32) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_u64(self, v: u64) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_f32(self, _: f32) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_f64(self, _: f64) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_char(self, v: char) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_str(self, v: &str) -> Result<String, Error> {
        Ok(v.to_string())
    }
    fn serialize_bytes(self, _: &[u8]) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_none(self) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_some<T: ?Sized + Serialize>(self, _: &T) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_unit(self) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_unit_struct(self, _: &'static str) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_unit_variant(self, _: &'static str, _: u32, variant: &'static str) -> Result<String, Error> {
        Ok(variant.to_string())
    }
    fn serialize_newtype_struct<T: ?Sized + Serialize>(self, _: &'static str, value: &T) -> Result<String, Error> {
        value.serialize(self)
    }
    fn serialize_newtype_variant<T: ?Sized + Serialize>(
        self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: &T,
    ) -> Result<String, Error> {
        Err(key_error())
    }
    fn serialize_seq(self, _: Option<usize>) -> Result<Self::SerializeSeq, Error> {
        Err(key_error())
    }
    fn serialize_tuple(self, _: usize) -> Result<Self::SerializeTuple, Error> {
        Err(key_error())
    }
    fn serialize_tuple_struct(self, _: &'static str, _: usize) -> Result<Self::SerializeTupleStruct, Error> {
        Err(key_error())
    }
    fn serialize_tuple_variant(
        self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleVariant, Error> {
        Err(key_error())
    }
    fn serialize_map(self, _: Option<usize>) -> Result<Self::SerializeMap, Error> {
        Err(key_error())
    }
    fn serialize_struct(self, _: &'static str, _: usize) -> Result<Self::SerializeStruct, Error> {
        Err(key_error())
    }
    fn serialize_struct_variant(
        self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeStructVariant, Error> {
        Err(key_error())
    }
}
