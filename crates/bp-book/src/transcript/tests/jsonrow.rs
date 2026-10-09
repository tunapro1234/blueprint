//! Port of internal/cache/jsonrow_test.go.

use crate::transcript::{
    definitely_other_compact_record_type, has_compact_record_type_prefix, may_have_record_type,
};

#[test]
fn test_may_have_record_type_skips_only_unambiguous_other_types() {
    for (name, row, want) in [
        (
            "different type",
            r#"{"type":"session_meta","payload":{"type":"assistant"}}"#,
            false,
        ),
        (
            "type after nested value",
            r#"{"payload":{"type":"session_meta"},"type":"assistant"}"#,
            true,
        ),
        ("case folded field", r#"{"TYPE":"assistant"}"#, true),
        ("escaped key", r#"{"ty\u0070e":"session_meta"}"#, true),
        ("escaped value", r#"{"type":"session\u005fmeta"}"#, true),
        (
            "duplicate field",
            r#"{"type":"session_meta","type":"assistant"}"#,
            true,
        ),
        (
            "quoted nested field",
            r#"{"type":"session_meta","payload":"{\"type\":\"assistant\"}"}"#,
            false,
        ),
        (
            "malformed object",
            r#"{"type":"session_meta","payload":"#,
            true,
        ),
        (
            "trailing content",
            r#"{"type":"session_meta"} garbage"#,
            true,
        ),
        ("leading whitespace", r#" {"type":"session_meta"}"#, false),
        (
            "relevant leading whitespace",
            r#" {"type":"assistant"}"#,
            true,
        ),
    ] {
        assert_eq!(
            may_have_record_type(row.as_bytes(), &["assistant"]),
            want,
            "{name}: {row}"
        );
    }
}

#[test]
fn test_has_compact_record_type_prefix_is_positive_only() {
    for (line, want) in [
        (r#"{"type":"session_meta","payload":{}}"#, true),
        (r#"{"type":"session_meta_extra"}"#, false),
        (r#" {"type":"session_meta"}"#, false),
        (r#"{"payload":{"type":"session_meta"}}"#, false),
    ] {
        assert_eq!(
            has_compact_record_type_prefix(line.as_bytes(), &["session_meta"]),
            want,
            "{line}"
        );
    }
}

#[test]
fn test_definitely_other_compact_record_type_skips_only_unambiguous_rows() {
    for (line, want) in [
        (r#"{"type":"queue-operation","operation":"enqueue"}"#, true),
        (r#"{"type":"assistant","message":{}}"#, false),
        (
            r#"{"type":"queue-operation","payload":{"type":"user"}}"#,
            false,
        ),
        (r#"{"type":"queue-operation","Type":"user"}"#, false),
        (
            r#"{"type":"queue-operation","content":"quoted \"type\""}"#,
            false,
        ),
        (r#" {"type":"queue-operation"}"#, false),
        (r#"{"type":"queue-operation","payload":"#, true),
    ] {
        assert_eq!(
            definitely_other_compact_record_type(line.as_bytes(), &["assistant", "user"]),
            want,
            "{line}"
        );
    }
}
