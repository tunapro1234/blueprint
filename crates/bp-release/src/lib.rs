//! bp-release: port of Go `internal/buildinfo` and `internal/release` (plus the
//! release-side helpers of `cmd/bp/update.go`). See PLAN.md §4.

pub mod buildinfo;
mod fsutil;
pub mod gojson;
pub mod release;
pub mod update;

pub use buildinfo::{ExecutableStat, Identity};
pub use release::{Checker, Context, Manifest, newer, platform, version};
