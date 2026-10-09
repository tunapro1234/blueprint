//! Port of internal/codexauth/codexauth_test.go.

use super::*;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use chrono::{TimeZone, Utc};
use serde_json::{Value, json};

/// The reference clock for every case.
fn now() -> Time {
    Utc.with_ymd_and_hms(2026, 8, 10, 13, 15, 0)
        .unwrap()
        .fixed_offset()
}

/// Appears inside every fixture token so a leak into any field is detectable.
const TOKEN_MARKER: &str = "SECRETTOKENMATERIAL";

fn jwt(claims: Value) -> String {
    let header = URL_SAFE_NO_PAD.encode(br#"{"alg":"RS256","typ":"JWT"}"#);
    let body = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&claims).unwrap());
    let sig = URL_SAFE_NO_PAD.encode(format!("sig-{TOKEN_MARKER}"));
    format!("{header}.{body}.{sig}")
}

fn expiring(at: Time) -> String {
    jwt(json!({"exp": at.timestamp(), "email": "someone@example.com", "jti": TOKEN_MARKER}))
}

fn h(n: i64) -> TimeDelta {
    TimeDelta::hours(n)
}

fn m(n: i64) -> TimeDelta {
    TimeDelta::minutes(n)
}

fn rfc(t: Time) -> String {
    gotime::format_rfc3339_nano(&t)
}

struct Fixture {
    _dir: tempfile::TempDir,
    path: String,
}

fn write_raw(body: &str) -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("auth.json");
    fs::write(&path, body).unwrap();
    Fixture {
        path: path.to_string_lossy().into_owned(),
        _dir: dir,
    }
}

/// A fixture shaped like the real file, including the access_token that must
/// not influence the verdict.
fn write_auth(last_refresh: Time, id_exp: Time, access_exp: Time) -> Fixture {
    write_raw(
        &json!({
            "auth_mode": "chatgpt",
            "OPENAI_API_KEY": null,
            "last_refresh": rfc(last_refresh),
            "tokens": {
                "id_token": expiring(id_exp),
                "access_token": expiring(access_exp),
                "refresh_token": format!("rt_{TOKEN_MARKER}"),
                "account_id": format!("acct_{TOKEN_MARKER}"),
            },
        })
        .to_string(),
    )
}

#[test]
fn test_check_at() {
    let now = now();
    let missing_dir = tempfile::tempdir().unwrap();
    let missing = missing_dir
        .path()
        .join("auth.json")
        .to_string_lossy()
        .into_owned();
    type Case<'a> = (
        &'a str,
        Box<dyn Fn() -> (Option<Fixture>, String)>,
        Status,
        &'a str,
    );
    let cases: Vec<Case> = vec![
        (
            "healthy session refreshed minutes ago",
            Box::new(move || fix(write_auth(now - m(10), now + m(50), now + h(240)))),
            Status::Ok,
            "last_refresh 10m old, id_token valid for 50m",
        ),
        (
            // The measured outage: access_token.exp is a week in the FUTURE.
            "revoked session with future access_token exp",
            Box::new(move || fix(write_auth(now - h(72), now - h(75), now + h(8 * 24)))),
            Status::Expired,
            "last_refresh 3d old (>1d), id_token expired 3d 3h ago",
        ),
        (
            "fresh refresh with expired id_token is only stale",
            Box::new(move || fix(write_auth(now - m(95), now - m(35), now + h(9 * 24)))),
            Status::Stale,
            "id_token expired 35m ago, last_refresh 1h 35m old; awaiting next refresh",
        ),
        (
            "old refresh with live id_token is only stale",
            Box::new(move || fix(write_auth(now - h(40), now + m(30), now + h(5 * 24)))),
            Status::Stale,
            "last_refresh 1d 16h old (>1d) but id_token still valid for 30m",
        ),
        (
            "missing file",
            Box::new(move || (None, missing.clone())),
            Status::Unknown,
            "auth.json not found at ",
        ),
        (
            "unresolvable path",
            Box::new(|| (None, String::new())),
            Status::Unknown,
            "CODEX_HOME could not be resolved",
        ),
        (
            "malformed json",
            Box::new(|| fix(write_raw(r#"{"last_refresh": "#))),
            Status::Unknown,
            "auth.json is not valid JSON",
        ),
        (
            "no last_refresh",
            Box::new(move || {
                fix(write_raw(
                    &json!({"tokens": {"id_token": expiring(now + h(1))}}).to_string(),
                ))
            }),
            Status::Unknown,
            "auth.json has no last_refresh",
        ),
        (
            "last_refresh is not a timestamp",
            Box::new(move || {
                fix(write_raw(
                    &json!({"last_refresh": "yesterday", "tokens": {"id_token": expiring(now + h(1))}}).to_string(),
                ))
            }),
            Status::Unknown,
            "auth.json last_refresh is not an RFC3339 time",
        ),
        (
            "no id_token",
            Box::new(move || {
                fix(write_raw(
                    &json!({"last_refresh": rfc(now - m(5)), "tokens": {"access_token": expiring(now + h(240))}})
                        .to_string(),
                ))
            }),
            Status::Unknown,
            "auth.json has no tokens.id_token",
        ),
        (
            "id_token claims segment is not base64url",
            Box::new(move || {
                fix(write_raw(
                    &json!({"last_refresh": rfc(now - m(5)), "tokens": {"id_token": format!("aaa.{TOKEN_MARKER}!!!.ccc")}})
                        .to_string(),
                ))
            }),
            Status::Unknown,
            "id_token claims segment is not base64url",
        ),
        (
            "id_token is not a jwt",
            Box::new(move || {
                fix(write_raw(
                    &json!({"last_refresh": rfc(now - m(5)), "tokens": {"id_token": format!("opaque-{TOKEN_MARKER}")}})
                        .to_string(),
                ))
            }),
            Status::Unknown,
            "id_token is not a three-segment JWT",
        ),
        (
            "id_token claims are not json",
            Box::new(move || {
                let body = URL_SAFE_NO_PAD.encode(format!("not json {TOKEN_MARKER}"));
                fix(write_raw(
                    &json!({"last_refresh": rfc(now - m(5)), "tokens": {"id_token": format!("aaa.{body}.ccc")}})
                        .to_string(),
                ))
            }),
            Status::Unknown,
            "id_token claims segment is not JSON",
        ),
        (
            "id_token has no exp claim",
            Box::new(move || {
                fix(write_raw(
                    &json!({"last_refresh": rfc(now - m(5)), "tokens": {"id_token": jwt(json!({"sub": TOKEN_MARKER}))}})
                        .to_string(),
                ))
            }),
            Status::Unknown,
            "id_token claims carry no exp",
        ),
        (
            "last_refresh in the future yields no verdict",
            Box::new(move || fix(write_auth(now + h(2), now + h(3), now + h(240)))),
            Status::Unknown,
            "clock skew, no verdict",
        ),
    ];
    for (name, path, want, want_reason) in cases {
        let (_fixture, path) = path();
        let state = check_at(&path, &now);
        assert_eq!(state.status, want, "{name}: reason {:?}", state.reason);
        assert!(
            state.reason.contains(want_reason),
            "{name}: reason = {:?}, want {want_reason:?}",
            state.reason
        );
        if state.status == Status::Ok {
            assert_eq!(want, Status::Ok, "{name}: unexpected healthy verdict");
        }
        let rendered = format!("{state:?}");
        assert!(
            !rendered.contains(TOKEN_MARKER),
            "{name}: token material leaked: {rendered}"
        );
        assert!(
            !rendered.contains("acct_") && !rendered.contains("rt_"),
            "{name}: leaked: {rendered}"
        );
        assert_eq!(state.checked, now, "{name}");
    }
}

fn fix(f: Fixture) -> (Option<Fixture>, String) {
    let path = f.path.clone();
    (Some(f), path)
}

/// Broken must be true only for the one state that proves access is gone.
#[test]
fn test_broken_only_for_expired() {
    for (status, want) in [
        (Status::Unknown, false),
        (Status::Ok, false),
        (Status::Stale, false),
        (Status::Expired, true),
    ] {
        let mut state = check_at("", &now());
        state.status = status;
        assert_eq!(state.broken(), want, "{status}");
    }
}

/// The verdict must rest on last_refresh and id_token only.
#[test]
fn test_access_token_exp_is_not_evidence() {
    let now = now();
    let fixture = write_auth(now - m(10), now + m(50), now - h(30 * 24));
    let healthy = check_at(&fixture.path, &now);
    assert_eq!(healthy.status, Status::Ok, "{healthy:?}");
    assert_eq!(healthy.id_token_exp, now + m(50));
    assert_eq!(healthy.last_refresh, now - m(10));
}

#[test]
fn test_home_uses_codex_home_env() {
    let env = |codex: &'static str, home: &'static str| {
        move |key: &str| match key {
            "CODEX_HOME" => Some(codex.to_owned()),
            "HOME" => Some(home.to_owned()),
            _ => None,
        }
    };
    assert_eq!(home_from(&env("/custom/codex", "/root")), "/custom/codex");
    assert_eq!(
        path_from(&home_from(&env("/custom/codex", "/root"))),
        "/custom/codex/auth.json"
    );
    assert_eq!(home_from(&env("", "/home/someone")), "/home/someone/.codex");
    assert_eq!(home_from(&env("  ", "")), "");
}

#[test]
fn test_short_age() {
    let s = TimeDelta::seconds;
    for (input, want) in [
        (TimeDelta::zero(), "0m"),
        (s(29), "0m"),
        (s(30), "1m"),
        (s(90), "2m"),
        (h(1), "1h"),
        (m(95), "1h 35m"),
        (h(24), "1d"),
        (h(72), "3d"),
        (h(75), "3d 3h"),
        (-h(75), "3d 3h"),
        (h(40), "1d 16h"),
        (h(7 * 24 + 1), "7d 1h"),
    ] {
        assert_eq!(short_age(input), want, "{input}");
    }
}
