//! Go `time` helpers: `time.Time` JSON shape (RFC3339Nano) and
//! `time.Duration` `String()` / `time.ParseDuration`.

use std::fmt;
use std::str::FromStr;

use chrono::{DateTime, Datelike, FixedOffset, NaiveDate, Offset, TimeZone, Timelike, Utc};

/// Go's zero `time.Time` (`0001-01-01T00:00:00Z`).
pub fn zero_time() -> DateTime<FixedOffset> {
    NaiveDate::from_ymd_opt(1, 1, 1)
        .expect("valid date")
        .and_hms_opt(0, 0, 0)
        .expect("valid time")
        .and_utc()
        .fixed_offset()
}

/// Reports whether `t` is Go's zero time instant (`Time.IsZero`).
pub fn is_zero_time<Tz: TimeZone>(t: &DateTime<Tz>) -> bool {
    t.naive_utc() == zero_time().naive_utc()
}

/// Formats like Go `t.Format(time.RFC3339Nano)`, which is also the
/// `time.Time` JSON form: fractional seconds with trailing zeros trimmed
/// (omitted when zero) and `Z` for a zero UTC offset.
pub fn format_rfc3339_nano<Tz: TimeZone>(t: &DateTime<Tz>) -> String {
    format_rfc3339_impl(t, true)
}

/// Formats like Go `t.Format(time.RFC3339)` (no fractional seconds).
pub fn format_rfc3339<Tz: TimeZone>(t: &DateTime<Tz>) -> String {
    format_rfc3339_impl(t, false)
}

fn format_rfc3339_impl<Tz: TimeZone>(t: &DateTime<Tz>, nanos: bool) -> String {
    let offset = t.offset().fix().local_minus_utc();
    let local = t.naive_local();
    let mut out = String::with_capacity(35);
    let year = local.year();
    if year < 0 {
        out.push_str(&format!("-{:04}", -year));
    } else {
        out.push_str(&format!("{year:04}"));
    }
    out.push_str(&format!(
        "-{:02}-{:02}T{:02}:{:02}:{:02}",
        local.month(),
        local.day(),
        local.hour(),
        local.minute(),
        local.second()
    ));
    let ns = local.nanosecond() % 1_000_000_000;
    if nanos && ns != 0 {
        let frac = format!("{ns:09}");
        out.push('.');
        out.push_str(frac.trim_end_matches('0'));
    }
    if offset == 0 {
        out.push('Z');
    } else {
        let sign = if offset < 0 { '-' } else { '+' };
        let abs = offset.unsigned_abs();
        out.push_str(&format!("{sign}{:02}:{:02}", abs / 3600, abs / 60 % 60));
    }
    out
}

/// Parses an RFC 3339 timestamp like Go's `time.Time.UnmarshalJSON` input.
pub fn parse_rfc3339(s: &str) -> Result<DateTime<FixedOffset>, chrono::ParseError> {
    DateTime::parse_from_rfc3339(s)
}

/// Serde adapter (`#[serde(with = "bp_core::gojson::time::rfc3339_nano")]`)
/// for `DateTime<FixedOffset>` fields that Go declares as `time.Time`.
pub mod rfc3339_nano {
    use super::*;
    use serde::{Deserialize, Deserializer, Serializer};

    pub fn serialize<S: Serializer>(t: &DateTime<FixedOffset>, s: S) -> Result<S::Ok, S::Error> {
        use chrono::Datelike;
        if !(0..=9999).contains(&t.year()) {
            return Err(serde::ser::Error::custom(
                "json: error calling MarshalJSON for type time.Time: Time.MarshalJSON: year outside of range [0,9999]",
            ));
        }
        s.serialize_str(&format_rfc3339_nano(t))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<DateTime<FixedOffset>, D::Error> {
        // Go: JSON null leaves the time unchanged (zero).
        match Option::<String>::deserialize(d)? {
            None => Ok(zero_time()),
            Some(text) => parse_rfc3339(&text).map_err(|_| {
                serde::de::Error::custom(format!(
                    "parsing time {:?} as \"2006-01-02T15:04:05Z07:00\": cannot parse",
                    text
                ))
            }),
        }
    }
}

/// Current time in UTC as a `DateTime<FixedOffset>` (Go `time.Now().UTC()`).
pub fn now_utc() -> DateTime<FixedOffset> {
    Utc::now().fixed_offset()
}

/// A Go `time.Duration`: signed nanoseconds. Serializes as the integer (Go
/// has no Duration JSON method); `Display` is `Duration.String()` and
/// `FromStr` is `time.ParseDuration`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default, serde::Serialize, serde::Deserialize)]
#[serde(transparent)]
pub struct GoDuration(pub i64);

pub const NANOSECOND: i64 = 1;
pub const MICROSECOND: i64 = 1_000;
pub const MILLISECOND: i64 = 1_000_000;
pub const SECOND: i64 = 1_000_000_000;
pub const MINUTE: i64 = 60 * SECOND;
pub const HOUR: i64 = 60 * MINUTE;

impl GoDuration {
    pub fn from_std(d: std::time::Duration) -> Self {
        GoDuration(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
    }
    /// The duration as `std::time::Duration`; negative values clamp to zero.
    pub fn to_std(self) -> std::time::Duration {
        std::time::Duration::from_nanos(u64::try_from(self.0).unwrap_or(0))
    }
    pub fn seconds_f64(self) -> f64 {
        let sec = self.0 / SECOND;
        let nsec = self.0 % SECOND;
        sec as f64 + nsec as f64 / 1e9
    }
}

impl fmt::Display for GoDuration {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&format_duration(self.0))
    }
}

impl FromStr for GoDuration {
    type Err = String;
    fn from_str(s: &str) -> Result<Self, String> {
        parse_duration(s).map(GoDuration)
    }
}

/// Go `time.Duration(d).String()`.
pub fn format_duration(d: i64) -> String {
    // Port of time.Duration.format.
    let mut buf = [0u8; 32];
    let mut w = buf.len();
    let neg = d < 0;
    let mut u = d.unsigned_abs();
    if u < SECOND as u64 {
        let prec;
        w -= 1;
        buf[w] = b's';
        w -= 1;
        if u == 0 {
            return "0s".into();
        } else if u < MICROSECOND as u64 {
            prec = 0;
            buf[w] = b'n';
        } else if u < MILLISECOND as u64 {
            prec = 3;
            // U+00B5 'µ' micro sign == 0xC2 0xB5
            w -= 1;
            buf[w..w + 2].copy_from_slice("µ".as_bytes());
        } else {
            prec = 6;
            buf[w] = b'm';
        }
        let (nw, nu) = fmt_frac(&mut buf[..w], u, prec);
        w = nw;
        u = nu;
        w = fmt_int(&mut buf[..w], u);
    } else {
        w -= 1;
        buf[w] = b's';
        let (nw, nu) = fmt_frac(&mut buf[..w], u, 9);
        w = nw;
        u = nu;
        w = fmt_int(&mut buf[..w], u % 60);
        u /= 60;
        if u > 0 {
            w -= 1;
            buf[w] = b'm';
            w = fmt_int(&mut buf[..w], u % 60);
            u /= 60;
            if u > 0 {
                w -= 1;
                buf[w] = b'h';
                w = fmt_int(&mut buf[..w], u);
            }
        }
    }
    if neg {
        w -= 1;
        buf[w] = b'-';
    }
    String::from_utf8(buf[w..].to_vec()).expect("ascii and µ")
}

fn fmt_frac(buf: &mut [u8], mut v: u64, prec: usize) -> (usize, u64) {
    let mut w = buf.len();
    let mut print = false;
    for _ in 0..prec {
        let digit = v % 10;
        print = print || digit != 0;
        if print {
            w -= 1;
            buf[w] = digit as u8 + b'0';
        }
        v /= 10;
    }
    if print {
        w -= 1;
        buf[w] = b'.';
    }
    (w, v)
}

fn fmt_int(buf: &mut [u8], mut v: u64) -> usize {
    let mut w = buf.len();
    if v == 0 {
        w -= 1;
        buf[w] = b'0';
    } else {
        while v > 0 {
            w -= 1;
            buf[w] = (v % 10) as u8 + b'0';
            v /= 10;
        }
    }
    w
}

/// Go `time.ParseDuration`, including its error messages.
pub fn parse_duration(s: &str) -> Result<i64, String> {
    let orig = s;
    let invalid = || format!("time: invalid duration {}", time_quote(orig));
    let mut s = s.as_bytes();
    let mut d: u64 = 0;
    let mut neg = false;
    if let Some(&c) = s.first()
        && (c == b'-' || c == b'+')
    {
        neg = c == b'-';
        s = &s[1..];
    }
    if s == b"0" {
        return Ok(0);
    }
    if s.is_empty() {
        return Err(invalid());
    }
    while !s.is_empty() {
        let mut f: u64 = 0;
        let mut scale: f64 = 1.0;
        if !(s[0] == b'.' || s[0].is_ascii_digit()) {
            return Err(invalid());
        }
        let pl = s.len();
        let (mut v, rest, overflow) = leading_int(s);
        if overflow {
            return Err(invalid());
        }
        s = rest;
        let pre = pl != s.len();
        let mut post = false;
        if s.first() == Some(&b'.') {
            s = &s[1..];
            let pl = s.len();
            let (ff, sc, rest) = leading_fraction(s);
            f = ff;
            scale = sc;
            s = rest;
            post = pl != s.len();
        }
        if !pre && !post {
            return Err(invalid());
        }
        let mut i = 0;
        while i < s.len() && !(s[i] == b'.' || s[i].is_ascii_digit()) {
            i += 1;
        }
        if i == 0 {
            return Err(format!("time: missing unit in duration {}", time_quote(orig)));
        }
        let u = &s[..i];
        s = &s[i..];
        let unit: u64 = match u {
            b"ns" => 1,
            b"us" => 1_000,
            b"\xc2\xb5s" | b"\xce\xbcs" => 1_000,
            b"ms" => 1_000_000,
            b"s" => 1_000_000_000,
            b"m" => 60_000_000_000,
            b"h" => 3_600_000_000_000,
            _ => {
                return Err(format!(
                    "time: unknown unit {} in duration {}",
                    time_quote_bytes(u),
                    time_quote(orig)
                ));
            }
        };
        if v > (1u64 << 63) / unit {
            return Err(invalid());
        }
        v *= unit;
        if f > 0 {
            v += (f as f64 * (unit as f64 / scale)) as u64;
            if v > 1u64 << 63 {
                return Err(invalid());
            }
        }
        d += v;
        if d > 1u64 << 63 {
            return Err(invalid());
        }
    }
    if neg {
        return Ok((d as i64).wrapping_neg());
    }
    if d > (1u64 << 63) - 1 {
        return Err(invalid());
    }
    Ok(d as i64)
}

fn leading_int(s: &[u8]) -> (u64, &[u8], bool) {
    let mut x: u64 = 0;
    let mut i = 0;
    while i < s.len() && s[i].is_ascii_digit() {
        if x > (1u64 << 63) / 10 {
            return (0, s, true);
        }
        x = x * 10 + u64::from(s[i] - b'0');
        if x > 1u64 << 63 {
            return (0, s, true);
        }
        i += 1;
    }
    (x, &s[i..], false)
}

fn leading_fraction(s: &[u8]) -> (u64, f64, &[u8]) {
    let mut x: u64 = 0;
    let mut scale = 1.0;
    let mut overflow = false;
    let mut i = 0;
    while i < s.len() && s[i].is_ascii_digit() {
        if !overflow {
            if x > (1u64 << 63) / 10 {
                overflow = true;
            } else {
                let y = x * 10 + u64::from(s[i] - b'0');
                if y > 1u64 << 63 {
                    overflow = true;
                } else {
                    x = y;
                    scale *= 10.0;
                }
            }
        }
        i += 1;
    }
    (x, scale, &s[i..])
}

/// Go's private `time.quote`: like `%q` for ASCII, but every non-ASCII or
/// control byte is written as `\xNN`.
pub fn time_quote(s: &str) -> String {
    time_quote_bytes(s.as_bytes())
}

fn time_quote_bytes(s: &[u8]) -> String {
    let mut out = String::from("\"");
    for &b in s {
        if b >= 0x80 || b < b' ' {
            out.push_str(&format!("\\x{b:02x}"));
        } else {
            if b == b'"' || b == b'\\' {
                out.push('\\');
            }
            out.push(b as char);
        }
    }
    out.push('"');
    out
}

/// Go `strconv.Quote` (`%q`).
pub fn go_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => out.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations() {
        for (d, want) in [
            (0, "0s"),
            (1, "1ns"),
            (1100, "1.1µs"),
            (2_200_000, "2.2ms"),
            (3_300_000_000, "3.3s"),
            (4 * MINUTE + 5 * SECOND, "4m5s"),
            (4 * MINUTE + 5_001 * MILLISECOND, "4m5.001s"),
            (5 * HOUR + 6 * MINUTE + 7_001 * MILLISECOND, "5h6m7.001s"),
            (8 * MINUTE + 1, "8m0.000000001s"),
            (i64::MAX, "2562047h47m16.854775807s"),
            (i64::MIN, "-2562047h47m16.854775808s"),
            (-HOUR, "-1h0m0s"),
        ] {
            assert_eq!(format_duration(d), want);
        }
        for (s, want) in [
            ("0", 0),
            ("5s", 5 * SECOND),
            ("-5s", -5 * SECOND),
            ("+5s", 5 * SECOND),
            ("1.5h", HOUR + 30 * MINUTE),
            (".5s", 500 * MILLISECOND),
            ("1h2m3s4ms5us6ns", HOUR + 2 * MINUTE + 3 * SECOND + 4 * MILLISECOND + 5 * MICROSECOND + 6),
            ("3µs", 3000),
            ("3μs", 3000),
            ("9223372036854775807ns", i64::MAX),
            ("-9223372036854775808ns", i64::MIN),
            ("0.100000000000000000000h", 6 * MINUTE),
        ] {
            assert_eq!(parse_duration(s), Ok(want), "{s}");
        }
        assert_eq!(parse_duration("").unwrap_err(), "time: invalid duration \"\"");
        assert_eq!(parse_duration("3").unwrap_err(), "time: missing unit in duration \"3\"");
        assert_eq!(
            parse_duration("3x").unwrap_err(),
            "time: unknown unit \"x\" in duration \"3x\""
        );
        assert!(parse_duration("9223372036854775808ns").is_err());
        assert!(parse_duration(".s").is_err());
    }

    #[test]
    fn times() {
        assert_eq!(format_rfc3339_nano(&zero_time()), "0001-01-01T00:00:00Z");
        let t = DateTime::parse_from_rfc3339("2026-10-09T12:30:00.120+03:00").unwrap();
        assert_eq!(format_rfc3339_nano(&t), "2026-10-09T12:30:00.12+03:00");
        let t = DateTime::parse_from_rfc3339("2026-10-09T12:30:00+00:00").unwrap();
        assert_eq!(format_rfc3339_nano(&t), "2026-10-09T12:30:00Z");
        assert!(is_zero_time(&zero_time()));
    }
}
