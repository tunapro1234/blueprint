//! Go `strings`/`unicode` behavior the screen parsers rely on.
//!
//! `unicode.IsSpace` is the Unicode White_Space property, which is exactly
//! what `char::is_whitespace` tests (NBSP and U+0085 included), so Go's
//! `strings.TrimSpace` is `str::trim` and `strings.Fields` is
//! `split_whitespace`.

/// `stripSpace`: removes every `unicode.IsSpace` rune.
pub(crate) fn strip_space(s: &str) -> String {
    s.chars().filter(|c| !c.is_whitespace()).collect()
}

/// `stripSpace1`: `strings.Join(strings.Fields(s), " ")`.
pub(crate) fn strip_space1(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// `strings.ToLower`: per-rune simple lowercase mapping. Rust's
/// `str::to_lowercase` differs for final sigma and U+0130, so map rune by rune
/// and keep the first rune of a multi-rune mapping (Go maps U+0130 to 'i').
pub(crate) fn go_lower(s: &str) -> String {
    s.chars()
        .map(|c| {
            if c.is_ascii() {
                c.to_ascii_lowercase()
            } else {
                c.to_lowercase().next().unwrap_or(c)
            }
        })
        .collect()
}

/// The `" \t "` cutset Go passes to `strings.TrimRight`/`TrimLeft`.
pub(crate) const SPACE_TAB_NBSP: [char; 3] = [' ', '\t', '\u{a0}'];

/// `len([]rune(s))`.
pub(crate) fn rune_len(s: &str) -> usize {
    s.chars().count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_lower_maps_rune_by_rune() {
        assert_eq!(go_lower("ESC To Interrupt"), "esc to interrupt");
        assert_eq!(go_lower("İSTANBUL"), "istanbul");
        assert_eq!(go_lower("ΟΔΟΣ"), "οδοσ");
    }

    #[test]
    fn strip_space_covers_unicode_spaces() {
        assert_eq!(strip_space("a\u{a0}b\u{85}c\u{3000}d \te"), "abcde");
        assert_eq!(strip_space1("  a \u{a0} b\t c "), "a b c");
    }
}
