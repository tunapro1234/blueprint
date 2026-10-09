//! Port of the ANSI/dim/italic stripping in internal/tmux/tmux.go
//! (`ansiSeq`, `dimSeg`, `StripDim`) and hermes.go (`hermesGhostSeg`).

use std::sync::LazyLock;

use regex::Regex;

/// `ansiSeq`: one SGR escape sequence.
pub(crate) static ANSI_SEQ: LazyLock<Regex> =
    LazyLock::new(|| Regex::new("\x1b\\[[0-9;]*m").expect("ansiSeq"));

/// `dimSeg`: one dim-rendered segment, up to the reset that ends it.
static DIM_SEG: LazyLock<Regex> =
    LazyLock::new(|| Regex::new("\x1b\\[2m.*?\x1b\\[(?:0|22)m").expect("dimSeg"));

/// `hermesGhostSeg`: one italic-rendered segment (Hermes placeholder/ghost
/// text), up to `\x1b[0m` or `\x1b[23m`.
pub(crate) static HERMES_GHOST_SEG: LazyLock<Regex> =
    LazyLock::new(|| Regex::new("\x1b\\[3m.*?\x1b\\[(?:0|23)m").expect("hermesGhostSeg"));

/// `ansiSeq.ReplaceAllString(s, "")`: removes ANSI colour, keeps dim text.
pub(crate) fn strip_ansi(s: &str) -> String {
    ANSI_SEQ.replace_all(s, "").into_owned()
}

/// `StripDim` removes dim-rendered segments (placeholders and ghost
/// suggestions render dim in both Codex and Claude Code composers), then
/// strips the remaining ANSI colour codes.
pub fn strip_dim(s: &str) -> String {
    let undimmed = DIM_SEG.replace_all(s, "");
    ANSI_SEQ.replace_all(&undimmed, "").into_owned()
}

/// `hermesGhostSeg.ReplaceAllString(s, "")`.
pub(crate) fn strip_italic(s: &str) -> String {
    HERMES_GHOST_SEG.replace_all(s, "").into_owned()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strip_dim_removes_dim_segments_and_colour() {
        assert_eq!(
            strip_dim("\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[22m"),
            "› "
        );
        assert_eq!(strip_dim("a\x1b[2mb\x1b[0mc\x1b[2md\x1b[0me"), "ace");
        // `.` does not cross a newline, exactly like Go's regexp.
        assert_eq!(strip_dim("\x1b[2mx\ny\x1b[0m"), "x\ny");
        assert_eq!(strip_ansi("\x1b[38;5;244m──\x1b[39m"), "──");
    }
}
