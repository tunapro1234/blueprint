//! Golden tests for gojson against Go's encoding/json and time packages
//! (testdata/golden/core/{gojson,gotime}.json, from tools/goldenexport/core).

use std::collections::{BTreeMap, HashMap};

use chrono::{DateTime, FixedOffset, TimeZone};
use serde::{Deserialize, Serialize, Serializer};
use serde_json::Value;
use serde_json::value::RawValue;

use super::*;

pub(crate) fn golden(name: &str) -> Value {
    let path = format!("{}/../../testdata/golden/core/{name}", env!("CARGO_MANIFEST_DIR"));
    serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap()
}

pub(crate) fn b64(s: &str) -> Vec<u8> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = Vec::new();
    let mut acc = 0u32;
    let mut bits = 0;
    for b in s.bytes().filter(|b| *b != b'=') {
        acc = (acc << 6) | ALPHABET.iter().position(|a| *a == b).unwrap() as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    out
}

fn s(v: &Value) -> &str {
    v.as_str().unwrap_or("")
}

#[test]
fn golden_floats() {
    let g = golden("gojson.json");
    let cases = g["floats"].as_array().unwrap();
    assert!(cases.len() > 300);
    for case in cases {
        let bits = u64::from_str_radix(s(&case["bits"]), 16).unwrap();
        let f = f64::from_bits(bits);
        let go = |r: Result<String, Error>| r.unwrap_or_else(|e| format!("ERROR: {e}"));
        assert_eq!(go(format_float(f)), s(&case["out"]), "{f:e}");
        assert_eq!(go(format_float32(f as f32)), s(&case["out32"]), "{f:e} as f32");
    }
    assert!(format_float(f64::NAN).is_err());
    assert_eq!(
        to_string(&f64::INFINITY).unwrap_err().to_string(),
        "json: unsupported value: +Inf"
    );
}

#[test]
fn golden_strings() {
    let g = golden("gojson.json");
    for case in g["strings"].as_array().unwrap() {
        let input = b64(s(&case["in_b64"]));
        let mut html = Vec::new();
        write_string_bytes(&mut html, &input, true);
        assert_eq!(String::from_utf8(html).unwrap(), s(&case["html"]), "{input:?}");
        let mut plain = Vec::new();
        write_string_bytes(&mut plain, &input, false);
        assert_eq!(String::from_utf8(plain).unwrap(), s(&case["nohtml"]), "{input:?}");
        if let Ok(text) = std::str::from_utf8(&input) {
            assert_eq!(to_string(text).unwrap(), s(&case["html"]));
        }
    }
}

#[test]
fn golden_values() {
    let g = golden("gojson.json");
    for case in g["values"].as_array().unwrap() {
        let input = b64(s(&case["in_b64"]));
        let shown = String::from_utf8_lossy(&input).into_owned();
        let decoded = decode_any(&input);
        let want_err = s(&case["error"]);
        if !want_err.is_empty() {
            let err = decoded.expect_err(&shown);
            if want_err == "unexpected end of JSON input" {
                assert_eq!(err.to_string(), want_err);
            }
            continue;
        }
        let value = decoded.unwrap_or_else(|e| panic!("{shown}: {e}"));
        assert_eq!(to_string(&value).unwrap(), s(&case["marshal"]), "{shown}");
        assert_eq!(
            String::from_utf8(to_vec_indent(&value, "", "  ").unwrap()).unwrap(),
            s(&case["indent"]),
            "{shown}"
        );
        assert_eq!(
            String::from_utf8(to_vec_indent(&value, ">", "\t").unwrap()).unwrap(),
            s(&case["indent_prefix"]),
            "{shown}"
        );
        let mut enc = Encoder::new(Vec::new());
        enc.set_escape_html(false);
        enc.set_indent("", "  ");
        enc.encode(&value).unwrap();
        assert_eq!(String::from_utf8(enc.into_inner()).unwrap(), s(&case["encoder_nohtml"]), "{shown}");
        // json.Indent / json.Compact on the original text agree as well.
        let compacted = compact(&input, false).unwrap();
        assert_eq!(
            indent(&compacted, "", "  ").unwrap(),
            indent(&input, "", "  ").unwrap(),
            "{shown}"
        );
    }
}

fn ser_bytes<S: Serializer>(bytes: &[u8], serializer: S) -> Result<S::Ok, S::Error> {
    serializer.serialize_bytes(bytes)
}

/// Mirror of `Sample` in tools/goldenexport/core/main.go.
#[derive(Serialize)]
struct Sample {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    note: String,
    count: i64,
    ratio: f64,
    small: f32,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    flag: bool,
    tags: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    empty: Vec<String>,
    #[serde(skip_serializing_if = "HashMap::is_empty")]
    labels: HashMap<String, String>,
    scores: Option<HashMap<String, i64>>,
    #[serde(skip_serializing_if = "Vec::is_empty", serialize_with = "ser_bytes")]
    blob: Vec<u8>,
    ptr: Option<i64>,
    #[serde(with = "time::rfc3339_nano")]
    when: DateTime<FixedOffset>,
    wait: GoDuration,
    #[serde(skip_serializing_if = "Option::is_none")]
    nested: Option<Inner>,
    #[serde(skip_serializing_if = "Option::is_none")]
    raw: Option<Box<RawValue>>,
    anything: Value,
}

#[derive(Serialize, Default)]
struct Inner {
    path: String,
    deep: Option<Vec<i64>>,
}

fn samples() -> Vec<Sample> {
    let plus3 = FixedOffset::east_opt(3 * 3600).unwrap();
    let when = plus3.with_ymd_and_hms(2026, 10, 9, 12, 30, 45).unwrap() + chrono::Duration::milliseconds(120);
    let utc = FixedOffset::east_opt(0).unwrap();
    vec![
        Sample {
            name: String::new(),
            note: String::new(),
            count: 0,
            ratio: 0.0,
            small: 0.0,
            flag: false,
            tags: None,
            empty: Vec::new(),
            labels: HashMap::new(),
            scores: None,
            blob: Vec::new(),
            ptr: None,
            when: zero_time(),
            wait: GoDuration(0),
            nested: None,
            raw: None,
            anything: Value::Null,
        },
        Sample {
            name: "a<b>&c".into(),
            note: "note".into(),
            count: -3,
            ratio: 1e21,
            small: 0.1,
            flag: true,
            tags: Some(vec!["x".into(), "y".into()]),
            empty: Vec::new(),
            labels: HashMap::from([
                ("z".into(), "1".into()),
                ("a".into(), "2".into()),
                ("M".into(), "3".into()),
            ]),
            scores: Some(HashMap::from([("b".into(), 2), ("a".into(), 1)])),
            blob: b"hello\0world".to_vec(),
            ptr: Some(1),
            when,
            wait: GoDuration(90 * time::SECOND),
            nested: Some(Inner {
                path: "/tmp/x".into(),
                deep: Some(Vec::new()),
            }),
            raw: Some(RawValue::from_string(r#" { "k" : [1, 2], "h":"<" } "#.to_string()).unwrap()),
            anything: serde_json::json!({"q": [1.5, "s", null, true]}),
        },
        Sample {
            name: "\u{2028}".into(),
            note: String::new(),
            count: 0,
            ratio: 3e-7,
            small: 1e-7,
            flag: false,
            tags: None,
            empty: Vec::new(),
            labels: HashMap::new(),
            scores: Some(HashMap::new()),
            blob: Vec::new(),
            ptr: None,
            when: when.with_timezone(&utc),
            wait: GoDuration(0),
            nested: Some(Inner::default()),
            raw: None,
            anything: Value::from("x"),
        },
    ]
}

#[test]
fn golden_structs() {
    let g = golden("gojson.json");
    let samples = samples();
    for case in g["structs"].as_array().unwrap() {
        let sample = &samples[case["id"].as_u64().unwrap() as usize];
        assert_eq!(to_string(sample).unwrap(), s(&case["marshal"]));
        assert_eq!(
            String::from_utf8(to_vec_indent(sample, "", "  ").unwrap()).unwrap(),
            s(&case["indent"])
        );
        let line = encode_line(sample, &Options::new().escape_html(false).indent("", "  ")).unwrap();
        assert_eq!(String::from_utf8(line).unwrap(), s(&case["encoder_nohtml"]));
    }
}

#[derive(Serialize, Deserialize, Default, Debug)]
#[serde(default)]
struct Lenient {
    name: String,
    count: i64,
    inner: LenientInner,
    list: Option<Vec<LenientInner>>,
    map: Option<BTreeMap<String, String>>,
    ptr: Option<String>,
}

#[derive(Serialize, Deserialize, Default, Debug)]
#[serde(default)]
struct LenientInner {
    x: String,
    y: i64,
}

const INNER_FIELDS: &[Field] = &[Field::any("x"), Field::any("y")];
const INNER: Shape = Shape::Struct(INNER_FIELDS);
const LENIENT: Shape = Shape::Struct(&[
    Field::any("name"),
    Field::any("count"),
    Field::new("inner", INNER),
    Field::new("list", Shape::Array(&INNER)),
    Field::new("map", Shape::Map(&Shape::Any)),
    Field::any("ptr"),
]);

#[test]
fn golden_lenient_decode() {
    let g = golden("gojson.json");
    for case in g["lenient"].as_array().unwrap() {
        let input = b64(s(&case["in_b64"]));
        let shown = String::from_utf8_lossy(&input).into_owned();
        let got = decode_lenient::<Lenient>(&input, LENIENT);
        if case["error"].as_bool().unwrap() {
            assert!(got.is_err(), "{shown}: accepted {got:?}");
            continue;
        }
        let got = got.unwrap_or_else(|e| panic!("{shown}: {e}"));
        assert_eq!(to_string(&got).unwrap(), s(&case["out"]), "{shown}");
    }
}

#[test]
fn golden_durations() {
    let g = golden("gotime.json");
    for case in g["format"].as_array().unwrap() {
        let nanos = case["nanos"].as_i64().unwrap();
        assert_eq!(format_duration(nanos), s(&case["out"]), "{nanos}");
        assert_eq!(GoDuration(nanos).to_string(), s(&case["out"]));
    }
    for case in g["parse"].as_array().unwrap() {
        let input = s(&case["in"]);
        let want_err = s(&case["error"]);
        match parse_duration(input) {
            Ok(n) => {
                assert!(want_err.is_empty(), "{input:?}: want error {want_err}");
                assert_eq!(n, case["nanos"].as_i64().unwrap(), "{input:?}");
            }
            Err(e) => assert_eq!(e, want_err, "{input:?}"),
        }
    }
}

#[test]
fn golden_times() {
    let g = golden("gotime.json");
    for case in g["times"].as_array().unwrap() {
        let unix = case["unix"].as_i64().unwrap();
        let nanos = case["nanos"].as_i64().unwrap() as u32;
        let offset = FixedOffset::east_opt(case["offset"].as_i64().unwrap() as i32).unwrap();
        let t = offset.timestamp_opt(unix, nanos).unwrap();
        assert_eq!(format_rfc3339_nano(&t), s(&case["out"]), "{unix} {nanos}");
        #[derive(Serialize)]
        struct W(#[serde(with = "time::rfc3339_nano")] DateTime<FixedOffset>);
        let json = to_string(&W(t)).unwrap_or_else(|e| format!("ERROR: {e}"));
        assert_eq!(json, s(&case["json"]));
        let parsed = time::parse_rfc3339(s(&case["out"])).unwrap();
        assert_eq!(parsed, t);
    }
}
