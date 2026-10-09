//! Port of internal/cache/local.go.

use std::fs;

use serde::Serialize;

use crate::go_struct;
use crate::godecode::unmarshal_ok;
use crate::gotime::{self, Time};

/// Created by bp's launcher, not inferred from cwd or TMUX env. It is
/// telemetry only and never grants a sender identity or hierarchy rights.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct LocalBinding {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub home: String,
    #[serde(rename = "cwd", skip_serializing_if = "String::is_empty")]
    pub cwd: String,
    pub path: String,
    pub pid: i64,
    pub harness: String,
}
go_struct!(LocalBinding {
    home: "home",
    cwd: "cwd",
    path: "path",
    pid: "pid",
    harness: "harness"
});

#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct LocalObservation {
    pub session_id: String,
    pub transcript_path: String,
    pub cwd: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub window: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context: Option<i64>,
    #[serde(with = "gotime::serde_go")]
    pub observed_at: Time,
}

fn is_zero_i64(v: &i64) -> bool {
    *v == 0
}

impl Default for LocalObservation {
    fn default() -> Self {
        LocalObservation {
            session_id: String::new(),
            transcript_path: String::new(),
            cwd: String::new(),
            model: String::new(),
            window: 0,
            context: None,
            observed_at: gotime::zero(),
        }
    }
}
go_struct!(LocalObservation {
    session_id: "session_id",
    transcript_path: "transcript_path",
    cwd: "cwd",
    model: "model",
    window: "window",
    context: "context",
    observed_at: "observed_at",
});

/// `^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`
fn local_session_id(id: &str) -> bool {
    let b = id.as_bytes();
    b.len() == 36
        && b.iter().enumerate().all(|(i, &c)| match i {
            8 | 13 | 18 | 23 => c == b'-',
            _ => matches!(c, b'0'..=b'9' | b'a'..=b'f'),
        })
}

/// Reads the observation file a bp-launched session writes.
pub fn read_local_observation(
    binding: Option<&LocalBinding>,
    pid: i64,
) -> Result<LocalObservation, String> {
    let mut observation = LocalObservation::default();
    let Some(binding) = binding.filter(|b| b.pid > 0 && b.pid == pid) else {
        return Err("local launch PID no longer matches pane".to_owned());
    };
    let data = fs::read(&binding.path).map_err(|err| {
        format!(
            "waiting for {} session observation (reopen older bp sessions): {}",
            binding.harness,
            go_path_error("open", &binding.path, &err)
        )
    })?;
    if !unmarshal_ok(&data, &mut observation) {
        return Err("invalid local session observation".to_owned());
    }
    if !local_session_id(&observation.session_id)
        || !observation.cwd.starts_with('/')
        || !observation.transcript_path.starts_with('/')
        || gotime::is_zero(&observation.observed_at)
    {
        return Err("incomplete local session observation".to_owned());
    }
    Ok(observation)
}

/// Formats an `os.PathError` the way Go prints it.
pub(crate) fn go_path_error(op: &str, path: &str, err: &std::io::Error) -> String {
    let text = match err.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".to_owned(),
        std::io::ErrorKind::PermissionDenied => "permission denied".to_owned(),
        _ => {
            let text = err.to_string();
            match text.find(" (os error") {
                Some(at) => text[..at].to_lowercase(),
                None => text,
            }
        }
    };
    format!("{op} {path}: {text}")
}
