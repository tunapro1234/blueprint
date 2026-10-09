//! Port of internal/cache/activity.go.

use serde::Serialize;

use crate::go_struct;
use crate::gotime::{self, Time};

/// A live observation, independent of historical usage. Unknown blocks
/// unattended delivery but must never be displayed as working or idle.
///
/// Serializes with Go's field order, names and `omitempty` rules.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Activity {
    #[serde(rename = "binding_conflicts", skip_serializing_if = "Vec::is_empty")]
    pub binding_conflicts: Vec<String>,
    #[serde(rename = "historical_bindings", skip_serializing_if = "Vec::is_empty")]
    pub historical_bindings: Vec<String>,
    pub state: String,
    pub source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub recovery_hint: String,
    #[serde(with = "gotime::serde_go")]
    pub observed_at: Time,
    #[serde(
        with = "gotime::serde_go::option",
        skip_serializing_if = "Option::is_none"
    )]
    pub last_event_at: Option<Time>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub thread_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub transcript_path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub binding: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub last_turn_error: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub screen_busy: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub turn_busy: Option<bool>,
    pub delivery_blocked: bool,
}

impl Default for Activity {
    fn default() -> Self {
        Activity {
            binding_conflicts: Vec::new(),
            historical_bindings: Vec::new(),
            state: String::new(),
            source: String::new(),
            reason: String::new(),
            recovery_hint: String::new(),
            observed_at: gotime::zero(),
            last_event_at: None,
            thread_id: String::new(),
            transcript_path: String::new(),
            binding: String::new(),
            last_turn_error: false,
            screen_busy: None,
            turn_busy: None,
            delivery_blocked: false,
        }
    }
}

go_struct!(Activity {
    binding_conflicts: "binding_conflicts",
    historical_bindings: "historical_bindings",
    state: "state",
    source: "source",
    reason: "reason",
    recovery_hint: "recovery_hint",
    observed_at: "observed_at",
    last_event_at: "last_event_at",
    thread_id: "thread_id",
    transcript_path: "transcript_path",
    binding: "binding",
    last_turn_error: "last_turn_error",
    screen_busy: "screen_busy",
    turn_busy: "turn_busy",
    delivery_blocked: "delivery_blocked",
});
