//! Port of internal/codexauth/codexauth.go.
//!
//! Answers one question offline: can this machine still talk to the Codex API
//! at all? The package touches nothing but `$CODEX_HOME/auth.json`: no
//! network, no tmux, no process inspection.
//!
//! It never returns, logs, or formats token material or the account id. Only
//! timestamps, durations, the file path, and JSON field names ever reach a
//! `reason` string.

use std::fs;
use std::io;

use base64::Engine as _;
use base64::alphabet::URL_SAFE;
use base64::engine::{DecodePaddingMode, GeneralPurpose, GeneralPurposeConfig};
use chrono::TimeDelta;

use crate::go_struct;
use crate::godecode::unmarshal_ok;
use crate::gotime::{self, Duration, Time};

/// How long `last_refresh` may lag before the session counts as unmaintained.
/// A session in use is rewritten roughly hourly; 24h is ~24 refresh intervals
/// of headroom.
fn stale_after() -> Duration {
    TimeDelta::hours(24)
}

/// The verdict about the local session.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Status {
    /// The file could not be read or understood; no claim is made.
    Unknown,
    /// last_refresh is recent and id_token has not expired.
    Ok,
    /// One signal is off but not both. Not evidence of broken access.
    Stale,
    /// last_refresh frozen past the limit AND the id_token it minted expired.
    Expired,
}

impl Status {
    /// The Go string value (`"unknown"`, `"ok"`, `"stale"`, `"expired"`).
    pub fn as_str(self) -> &'static str {
        match self {
            Status::Unknown => "unknown",
            Status::Ok => "ok",
            Status::Stale => "stale",
            Status::Expired => "expired",
        }
    }
}

impl std::fmt::Display for Status {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The full answer, including the evidence it rests on.
#[derive(Debug, Clone, PartialEq)]
pub struct State {
    pub status: Status,
    /// Short human-readable explanation, free of token material and account id.
    pub reason: String,
    /// The file that was consulted, even when reading it failed.
    pub path: String,
    /// auth.json's last_refresh; zero when absent or unparseable.
    pub last_refresh: Time,
    /// The `exp` claim of tokens.id_token; zero when unavailable.
    /// tokens.access_token is deliberately NOT parsed: it is not evidence.
    pub id_token_exp: Time,
    /// The reference clock the verdict used.
    pub checked: Time,
}

impl State {
    /// Whether access is provably unusable, the only state that justifies
    /// telling a user their access is gone.
    pub fn broken(&self) -> bool {
        self.status == Status::Expired
    }
}

/// Resolves CODEX_HOME for THIS process, falling back to `~/.codex`.
pub fn home() -> String {
    home_from(&|key| std::env::var(key).ok())
}

/// [`home`] with the environment injected (Go's `os.Getenv` and
/// `os.UserHomeDir`, which reads `$HOME`).
pub fn home_from(getenv: &dyn Fn(&str) -> Option<String>) -> String {
    if let Some(home) = getenv("CODEX_HOME") {
        let home = home.trim();
        if !home.is_empty() {
            return home.to_owned();
        }
    }
    match getenv("HOME") {
        Some(user) if !user.is_empty() => crate::transcript::glob::join(&[&user, ".codex"]),
        _ => String::new(),
    }
}

/// The auth.json this package reads.
pub fn path() -> String {
    path_from(&home())
}

/// [`path`] for an already resolved home.
pub fn path_from(home: &str) -> String {
    if home.is_empty() {
        return String::new();
    }
    crate::transcript::glob::join(&[home, "auth.json"])
}

/// Reads the resolved auth.json and judges it against the current clock.
pub fn check() -> State {
    check_at(&path(), &gotime::now())
}

#[derive(Default)]
struct AuthFile {
    last_refresh: String,
    tokens: AuthTokens,
}
go_struct!(AuthFile {
    last_refresh: "last_refresh",
    tokens: "tokens"
});

#[derive(Default)]
struct AuthTokens {
    id_token: String,
}
go_struct!(AuthTokens {
    id_token: "id_token"
});

/// [`check`] with the file and clock injected.
pub fn check_at(path: &str, now: &Time) -> State {
    let mut state = State {
        status: Status::Unknown,
        reason: String::new(),
        path: path.to_owned(),
        last_refresh: gotime::zero(),
        id_token_exp: gotime::zero(),
        checked: *now,
    };
    if path.is_empty() {
        state.reason = "CODEX_HOME could not be resolved".to_owned();
        return state;
    }
    let data = match fs::read(path) {
        Ok(data) => data,
        Err(err) => {
            state.reason = if err.kind() == io::ErrorKind::NotFound {
                format!("auth.json not found at {path}")
            } else {
                // Only the syscall error is surfaced; the file body is not read.
                format!("auth.json unreadable: {}", go_read_error(path, &err))
            };
            return state;
        }
    };
    // Only the fields needed for the verdict are decoded.
    let mut file = AuthFile::default();
    if !unmarshal_ok(&data, &mut file) {
        state.reason = "auth.json is not valid JSON".to_owned();
        return state;
    }
    if file.last_refresh.trim().is_empty() {
        state.reason = "auth.json has no last_refresh".to_owned();
        return state;
    }
    let Some(last_refresh) = gotime::parse_rfc3339(file.last_refresh.trim()) else {
        state.reason = "auth.json last_refresh is not an RFC3339 time".to_owned();
        return state;
    };
    state.last_refresh = last_refresh;
    if file.tokens.id_token.trim().is_empty() {
        state.reason = "auth.json has no tokens.id_token".to_owned();
        return state;
    }
    let exp = match jwt_expiry(&file.tokens.id_token) {
        Ok(exp) => exp,
        Err(err) => {
            // jwt_expiry's errors describe structure only, never content.
            state.reason = format!("id_token {err}");
            return state;
        }
    };
    state.id_token_exp = exp;

    let refresh_age = gotime::sub(now, &last_refresh);
    if refresh_age < TimeDelta::zero() {
        state.reason = format!(
            "last_refresh is {} in the future; clock skew, no verdict",
            short_age(-refresh_age)
        );
        return state;
    }
    let stale_refresh = refresh_age > stale_after();
    let id_expired = *now >= exp;
    let stale = short_age(stale_after());
    match (stale_refresh, id_expired) {
        (true, true) => {
            state.status = Status::Expired;
            state.reason = format!(
                "last_refresh {} old (>{stale}), id_token expired {} ago",
                short_age(refresh_age),
                short_age(gotime::sub(now, &exp))
            );
        }
        (true, false) => {
            state.status = Status::Stale;
            state.reason = format!(
                "last_refresh {} old (>{stale}) but id_token still valid for {}",
                short_age(refresh_age),
                short_age(gotime::sub(&exp, now))
            );
        }
        (false, true) => {
            // The expected shape after an hour of not running codex.
            state.status = Status::Stale;
            state.reason = format!(
                "id_token expired {} ago, last_refresh {} old; awaiting next refresh",
                short_age(gotime::sub(now, &exp)),
                short_age(refresh_age)
            );
        }
        (false, false) => {
            state.status = Status::Ok;
            state.reason = format!(
                "last_refresh {} old, id_token valid for {}",
                short_age(refresh_age),
                short_age(gotime::sub(&exp, now))
            );
        }
    }
    state
}

/// Formats the `os.ReadFile` error text Go would print (`read <path>: …`).
fn go_read_error(path: &str, err: &io::Error) -> String {
    let op = if err.kind() == io::ErrorKind::PermissionDenied {
        "open"
    } else {
        "read"
    };
    crate::transcript::go_path_error(op, path, err)
}

#[derive(Default)]
struct Claims {
    exp: Option<i64>,
}
go_struct!(Claims { exp: "exp" });

/// Decodes a JWT's claims segment and returns its exp. The token is never
/// returned, echoed, or included in an error.
fn jwt_expiry(token: &str) -> Result<Time, &'static str> {
    let parts: Vec<&str> = token.split('.').collect();
    if parts.len() != 3 {
        return Err("is not a three-segment JWT");
    }
    // Go's RawURLEncoding: no padding, newlines ignored, trailing bits allowed.
    let engine = GeneralPurpose::new(
        &URL_SAFE,
        GeneralPurposeConfig::new()
            .with_decode_padding_mode(DecodePaddingMode::RequireNone)
            .with_decode_allow_trailing_bits(true),
    );
    let segment: String = parts[1]
        .trim_end_matches('=')
        .chars()
        .filter(|c| *c != '\r' && *c != '\n')
        .collect();
    let claims_json = engine
        .decode(segment)
        .map_err(|_| "claims segment is not base64url")?;
    let mut claims = Claims::default();
    if !unmarshal_ok(&claims_json, &mut claims) {
        return Err("claims segment is not JSON");
    }
    let exp = claims.exp.ok_or("claims carry no exp")?;
    Ok(gotime::unix(exp))
}

/// Formats a duration the way the rest of bp does: coarse, ASCII, and never
/// more than two units.
pub fn short_age(d: Duration) -> String {
    let d = if d < TimeDelta::zero() { -d } else { d };
    let d = gotime::round(d, TimeDelta::minutes(1));
    let total_minutes = d.num_minutes();
    let days = total_minutes / (24 * 60);
    let hours = total_minutes % (24 * 60) / 60;
    let minutes = total_minutes % 60;
    match () {
        _ if days > 0 && hours > 0 => format!("{days}d {hours}h"),
        _ if days > 0 => format!("{days}d"),
        _ if hours > 0 && minutes > 0 => format!("{hours}h {minutes}m"),
        _ if hours > 0 => format!("{hours}h"),
        _ => format!("{minutes}m"),
    }
}

#[cfg(test)]
mod tests;
