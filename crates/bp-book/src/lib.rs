//! bp-book: see PLAN.md §4 for the Go packages this crate replaces.
//!
//! Wave 1: `internal/cache` (as [`transcript`]), `internal/codexauth` and
//! `internal/codexrpc`, plus the Go JSON/time semantics they need
//! ([`godecode`], [`gotime`]).

pub mod codexauth;
pub mod codexrpc;
pub mod godecode;
pub mod gotime;
pub mod transcript;
