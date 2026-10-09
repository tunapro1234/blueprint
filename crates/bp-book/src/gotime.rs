//! Go `time` semantics the ported readers depend on: the zero `time.Time`,
//! `time.Parse(time.RFC3339Nano, …)` (lenient) and the strict RFC 3339 parser
//! behind `time.Time.UnmarshalJSON`, `MarshalJSON` output, saturating `Sub`
//! and `Duration.Round`.
//!
//! Not a port of one Go file; it backs `internal/cache`, `internal/codexauth`
//! and `internal/codexrpc`.

use chrono::{DateTime, FixedOffset, NaiveDate, TimeDelta, TimeZone, Utc};

/// A Go `time.Time`: an instant plus the offset it was written with.
/// Equality (`==`) compares instants only, like Go's `Equal`.
pub type Time = DateTime<FixedOffset>;

/// A Go `time.Duration`; may be negative (`LastHumanAge` uses -1ns as "unknown").
pub type Duration = TimeDelta;

/// `time.Time{}`: 0001-01-01T00:00:00Z.
pub fn zero() -> Time {
    Utc.with_ymd_and_hms(1, 1, 1, 0, 0, 0)
        .single()
        .expect("year 1 is representable")
        .fixed_offset()
}

/// `t.IsZero()`.
pub fn is_zero(t: &Time) -> bool {
    *t == zero()
}

/// `time.Now()`. Ages only use the instant, so the offset is UTC.
pub fn now() -> Time {
    Utc::now().fixed_offset()
}

/// `-1` as a Go Duration (one nanosecond below zero), the "unknown" age.
pub fn minus_one() -> Duration {
    TimeDelta::nanoseconds(-1)
}

/// `a.Sub(b)`, saturating at Go's Duration range like Go does.
pub fn sub(a: &Time, b: &Time) -> Duration {
    let delta = a.signed_duration_since(*b);
    match delta.num_nanoseconds() {
        Some(_) => delta,
        None if delta > TimeDelta::zero() => TimeDelta::nanoseconds(i64::MAX),
        None => TimeDelta::nanoseconds(i64::MIN),
    }
}

/// `time.Unix(sec, 0).UTC()`, clamped to chrono's range.
pub fn unix(sec: i64) -> Time {
    match Utc.timestamp_opt(sec, 0).single() {
        Some(t) => t.fixed_offset(),
        None if sec > 0 => DateTime::<Utc>::MAX_UTC.fixed_offset(),
        None => DateTime::<Utc>::MIN_UTC.fixed_offset(),
    }
}

/// `d.Round(m)` for positive `m`: halves round away from zero.
pub fn round(d: Duration, m: Duration) -> Duration {
    let (Some(d), Some(m)) = (d.num_nanoseconds(), m.num_nanoseconds()) else {
        return d;
    };
    if m <= 0 {
        return TimeDelta::nanoseconds(d);
    }
    let mut r = d % m;
    if d < 0 {
        r = -r;
        if r + r < m {
            return TimeDelta::nanoseconds(d + r);
        }
        return TimeDelta::nanoseconds(d.checked_sub(m).map_or(i64::MIN, |v| v + r));
    }
    if r + r < m {
        return TimeDelta::nanoseconds(d - r);
    }
    TimeDelta::nanoseconds(d.checked_add(m).map_or(i64::MAX, |v| v - r))
}

/// `time.Parse(time.RFC3339Nano, s)` (and `time.RFC3339`, which parses the
/// same inputs).
pub fn parse_rfc3339(s: &str) -> Option<Time> {
    parse(s.as_bytes(), false)
}

/// The strict RFC 3339 parser `time.Time.UnmarshalJSON` uses on the raw bytes
/// between the quotes (escapes are not decoded).
pub fn parse_rfc3339_strict(b: &[u8]) -> Option<Time> {
    parse(b, true)
}

fn digits(b: &[u8], at: usize, n: usize) -> Option<u32> {
    let part = b.get(at..at + n)?;
    let mut value = 0u32;
    for &c in part {
        if !c.is_ascii_digit() {
            return None;
        }
        value = value * 10 + u32::from(c - b'0');
    }
    Some(value)
}

fn parse(b: &[u8], strict: bool) -> Option<Time> {
    let year = digits(b, 0, 4)?;
    if b.get(4) != Some(&b'-') {
        return None;
    }
    let month = digits(b, 5, 2)?;
    if b.get(7) != Some(&b'-') {
        return None;
    }
    let day = digits(b, 8, 2)?;
    if b.get(10) != Some(&b'T') {
        return None;
    }
    // stdHour accepts one or two digits; the strict parser wants two.
    let mut i = 11;
    let hour = if b.get(i + 1).is_some_and(u8::is_ascii_digit) {
        i += 2;
        digits(b, 11, 2)?
    } else {
        if strict {
            return None;
        }
        i += 1;
        digits(b, 11, 1)?
    };
    if b.get(i) != Some(&b':') {
        return None;
    }
    let minute = digits(b, i + 1, 2)?;
    if b.get(i + 3) != Some(&b':') {
        return None;
    }
    let second = digits(b, i + 4, 2)?;
    i += 6;
    let mut nanos = 0u32;
    if matches!(b.get(i), Some(b'.') | Some(b',')) && b.get(i + 1).is_some_and(u8::is_ascii_digit) {
        if strict && b[i] == b',' {
            return None;
        }
        let mut n = i + 1;
        while b.get(n).is_some_and(u8::is_ascii_digit) {
            n += 1;
        }
        let frac = &b[i + 1..n];
        let mut scale = 0;
        for &c in frac.iter().take(9) {
            nanos = nanos * 10 + u32::from(c - b'0');
            scale += 1;
        }
        while scale < 9 {
            nanos *= 10;
            scale += 1;
        }
        i = n;
    }
    let offset_secs: i32;
    match b.get(i) {
        Some(b'Z') => {
            offset_secs = 0;
            i += 1;
        }
        Some(&sign @ (b'+' | b'-')) => {
            if b.len() < i + 6 || b[i + 3] != b':' {
                return None;
            }
            let hh = digits(b, i + 1, 2)?;
            let mm = digits(b, i + 4, 2)?;
            if hh > 24 || mm > 60 {
                return None;
            }
            if strict && (hh >= 24 || mm >= 60) {
                return None;
            }
            let secs = (hh * 3600 + mm * 60) as i32;
            offset_secs = if sign == b'-' { -secs } else { secs };
            i += 6;
        }
        _ => return None,
    }
    if i != b.len() {
        return None;
    }
    if !(1..=12).contains(&month) || hour >= 24 || minute >= 60 || second >= 60 {
        return None;
    }
    let date = NaiveDate::from_ymd_opt(year as i32, month, day)?;
    let naive = date.and_hms_nano_opt(hour, minute, second, nanos)?;
    let utc = naive - TimeDelta::seconds(i64::from(offset_secs));
    let offset = FixedOffset::east_opt(offset_secs).unwrap_or(FixedOffset::east_opt(0)?);
    Some(Utc.from_utc_datetime(&utc).with_timezone(&offset))
}

/// `t.Format(time.RFC3339Nano)`, which is also `t.MarshalJSON` without quotes.
pub fn format_rfc3339_nano(t: &Time) -> String {
    let mut out = t.format("%Y-%m-%dT%H:%M:%S").to_string();
    let nanos = t.timestamp_subsec_nanos() % 1_000_000_000;
    if nanos != 0 {
        let frac = format!("{nanos:09}");
        out.push('.');
        out.push_str(frac.trim_end_matches('0'));
    }
    let offset = t.offset().local_minus_utc();
    if offset == 0 {
        out.push('Z');
    } else {
        let sign = if offset < 0 { '-' } else { '+' };
        let abs = offset.abs();
        out.push_str(&format!("{sign}{:02}:{:02}", abs / 3600, abs % 3600 / 60));
    }
    out
}

/// serde helpers that write a Go `time.Time` the way `encoding/json` does.
pub mod serde_go {
    use super::{Time, format_rfc3339_nano};
    use serde::Serializer;

    pub fn serialize<S: Serializer>(t: &Time, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&format_rfc3339_nano(t))
    }

    pub mod option {
        use super::super::{Time, format_rfc3339_nano};
        use serde::Serializer;

        pub fn serialize<S: Serializer>(t: &Option<Time>, s: S) -> Result<S::Ok, S::Error> {
            match t {
                Some(t) => s.serialize_str(&format_rfc3339_nano(t)),
                None => s.serialize_none(),
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_like_go() {
        let t = parse_rfc3339("2026-09-24T12:00:00Z").unwrap();
        assert_eq!(format_rfc3339_nano(&t), "2026-09-24T12:00:00Z");
        let t = parse_rfc3339("2026-09-24T12:00:00.123456789123+02:00").unwrap();
        assert_eq!(
            format_rfc3339_nano(&t),
            "2026-09-24T12:00:00.123456789+02:00"
        );
        assert!(parse_rfc3339("2026-09-24T1:00:00Z").is_some());
        assert!(parse_rfc3339_strict(b"2026-09-24T1:00:00Z").is_none());
        assert!(parse_rfc3339("2026-09-24t12:00:00Z").is_none());
        assert!(parse_rfc3339("2026-09-24 12:00:00Z").is_none());
        assert!(parse_rfc3339("2026-02-30T12:00:00Z").is_none());
        assert!(parse_rfc3339("2026-09-24T12:00:60Z").is_none());
        assert!(parse_rfc3339("2026-09-24T12:00:00,5Z").is_some());
        assert!(parse_rfc3339_strict(b"2026-09-24T12:00:00,5Z").is_none());
        assert!(parse_rfc3339("not a time").is_none());
        assert!(parse_rfc3339("").is_none());
        assert!(is_zero(&parse_rfc3339("0001-01-01T00:00:00Z").unwrap()));
    }

    #[test]
    fn rounds_half_away_from_zero() {
        let m = TimeDelta::minutes(1);
        assert_eq!(round(TimeDelta::seconds(30), m), m);
        assert_eq!(round(TimeDelta::seconds(29), m), TimeDelta::zero());
        assert_eq!(round(TimeDelta::seconds(-30), m), -m);
    }
}
