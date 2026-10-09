//! Port of the pure tests of internal/tmux/composer_test.go.

use super::fixtures::*;
use crate::screen::composer::*;
use crate::screen::gostr::strip_space;
use crate::screen::harness::claude::is_composer_box_border;
use crate::screen::region::{
    ComposerVerdict, busy, classify_composer, composer_content, is_composer_border, typing,
};

#[test]
fn composer_box_reads_the_whole_box() {
    let pane = claude_pane(&[
        "❯ -the sixth section is missing from the conversation draft",
        "  -the team introduction could move earlier",
        "",
        "  - there was no technical question",
    ]);
    let text = composer_box(&pane).expect("box not found in a live-shaped pane");
    let rows: Vec<&str> = text.split('\n').collect();
    assert_eq!(rows.len(), 4, "interior blank row preserved: {text:?}");
    assert_eq!(
        rows[2], "",
        "interior blank row was not preserved: {text:?}"
    );
    assert!(
        !text.contains('❯'),
        "prompt marker was not stripped: {text:?}"
    );
    assert!(
        !text.contains("summary") && !text.contains("INSERT"),
        "box leaked rows from outside the borders: {text:?}"
    );
    let want = strip_space(
        "-the sixth section is missing from the conversation draft-the team introduction could move earlier- there was no technical question",
    );
    assert_eq!(strip_space(&text), want);
}

#[test]
fn composer_box_single_row_matches_todays_reading() {
    let pane = claude_pane(&["❯ deploy the new bar chips"]);
    let text = composer_box(&pane).expect("single-row box not found");
    assert_eq!(strip_space(&text), composer_content(&pane));
}

#[test]
fn claude_composer_nbsp_prompt_is_empty() {
    for row in ["❯\u{a0}", "❯ \u{a0}\u{a0}"] {
        let pane = claude_pane(&[row]);
        assert!(composer_empty(&pane), "ComposerEmpty({row:?}) = false");
        assert!(!typing(&pane), "Typing({row:?}) = true");
    }
}

#[test]
fn composer_box_refuses_unfamiliar_structures() {
    let cases: Vec<(&str, String)> = vec![
        ("only one border above the status line", collapsed_pane()),
        (
            "no status footer",
            format!("  transcript\n{BOX_BORDER_TOP}\n\u{276f} yazi\n{BOX_BORDER_BOTTOM}\n"),
        ),
        ("no composer at all", "just some output\n".to_string()),
        (
            "marker on a continuation row",
            format!(
                "  x\n{BOX_BORDER_TOP}\n\u{276f} first\n\u{276f} second\n{BOX_BORDER_BOTTOM}\n{BOX_STATUS}\n"
            ),
        ),
        (
            "no marker on the first row",
            format!("  x\n{BOX_BORDER_TOP}\n  first\n{BOX_BORDER_BOTTOM}\n{BOX_STATUS}\n"),
        ),
        ("modal picker instead of a composer", picker_pane()),
        (
            "modal picker over a surviving status footer",
            picker_over_status_pane(),
        ),
    ];
    for (name, pane) in &cases {
        assert_eq!(composer_box(pane), None, "{name}");
    }
    assert!(
        !composer_filled(&collapsed_pane()),
        "collapsed empty composer reported as filled"
    );
    for pane in [picker_pane(), picker_over_status_pane()] {
        assert_eq!(
            stuck_paste(&pane, &["1. first option", STUCK_MESSAGE]),
            None,
            "a picker row was claimed as our own paste"
        );
        assert_eq!(
            composer_block_reason(&pane, &[STUCK_MESSAGE]),
            BLOCKED_BY_FOREIGN_TEXT
        );
    }
}

#[test]
fn composer_box_reads_the_labelled_top_border() {
    for line in [
        BOX_BORDER_TOP,
        BOX_BORDER_BOTTOM,
        "\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} kavram-outreach \u{2500}\u{2500}",
        "\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} 12 more lines \u{2500}\u{2500}\u{2500}",
        "  \u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} probot-main \u{2500}\u{2500}  ",
        "\u{001b}[38;5;244m\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} op-main \u{2500}\u{2500}\u{001b}[39m",
    ] {
        assert!(
            is_composer_box_border(line),
            "border not recognised: {line:?}"
        );
    }
    for line in [
        "",
        "  plain text",
        "\u{276f} \u{2500}\u{2500}\u{2500} written by the user \u{2500}\u{2500}\u{2500}",
        "bir \u{2500} iki \u{2500} uc",
        "\u{2500}\u{2500} short \u{2500}\u{2500}",
        "  -- INSERT -- \u{23f5}\u{23f5} bypass permissions on",
    ] {
        assert!(!is_composer_box_border(line), "false border: {line:?}");
    }
    assert!(
        !is_composer_border(&strip_space(BOX_BORDER_TOP)),
        "the shared pure-run border test was widened"
    );
    assert!(is_composer_border(&strip_space(BOX_BORDER_BOTTOM)));
}

#[test]
fn composer_box_on_an_empty_live_composer() {
    let pane = claude_pane(&[EMPTY_ROW]);
    let text = composer_box(&pane).expect("no box on an empty live-shaped composer");
    assert_eq!(strip_space(&text), "");
    assert!(
        !composer_filled(&pane) && !typing(&pane),
        "empty composer reported as filled"
    );
    assert_eq!(composer_block_reason::<&str>(&pane, &[]), "");
}

#[test]
fn composer_box_prefers_the_nearest_border_above_the_box() {
    let pane = claude_pane(&["\u{276f} one line"]);
    let (text, top) = composer_box_at(&pane).expect("box");
    assert_eq!(strip_space(&text), "oneline");
    assert_eq!(top, 2, "top border index (the box's own border)");
    assert!(
        !composer_box_scrolled(&pane, &text, top),
        "a box with transcript above it was called scrolled"
    );
}

#[test]
fn classify_paste_tells_ours_from_foreign() {
    let msg = "[server-main] roadmap review: we need to finish the goal-system section today, especially hierarchical assignment";
    let other = "[ada] a completely different topic: dashboard colors";
    let wrap_a = format!("❯ {}", &msg[..40]);
    let wrap_b = format!("  {}", &msg[40..]);
    let cases: Vec<(&str, String, PasteVerdict, &str)> = vec![
        (
            "exact",
            claude_pane1(&format!("❯ {msg}")),
            PasteVerdict::Exact,
            msg,
        ),
        (
            "exact across a wrap",
            claude_pane(&[&wrap_a, &wrap_b]),
            PasteVerdict::Exact,
            msg,
        ),
        (
            "exact match on a second candidate",
            claude_pane1(&format!("❯ {other}")),
            PasteVerdict::Exact,
            other,
        ),
        (
            "truncated from the front",
            claude_pane1(&format!("❯ {}", &msg[60..])),
            PasteVerdict::Damaged,
            msg,
        ),
        (
            "truncated at the end",
            claude_pane1(&format!("❯ {}", &msg[..70])),
            PasteVerdict::Damaged,
            msg,
        ),
        (
            "middle chunk missing",
            claude_pane1(&format!("❯ {}{}", &msg[..45], &msg[80..])),
            PasteVerdict::Damaged,
            msg,
        ),
        (
            "foreign line",
            claude_pane1("❯ my unfinished question is still here"),
            PasteVerdict::Foreign,
            "",
        ),
        (
            "short foreign line that our text contains",
            claude_pane1("❯ roadmap"),
            PasteVerdict::Foreign,
            "",
        ),
        (
            "claude paste chip is not readable",
            claude_pane1("❯ [Pasted text #1 +12 lines]"),
            PasteVerdict::Foreign,
            "",
        ),
        (
            "codex chip is not readable",
            claude_pane1("❯ [Pasted Content 1024 chars]"),
            PasteVerdict::Foreign,
            "",
        ),
        (
            "empty composer",
            claude_pane1(EMPTY_ROW),
            PasteVerdict::Foreign,
            "",
        ),
        (
            "unreadable structure",
            format!("❯ {msg}\n"),
            PasteVerdict::Foreign,
            "",
        ),
    ];
    for (name, pane, verdict, matched) in &cases {
        let candidates = [msg, other];
        let got = classify_paste(pane, &candidates);
        assert_eq!(got, (*verdict, *matched), "{name}");
    }
}

#[test]
fn related_paste_ignores_short_overlaps() {
    let long = "plan taslagi ".repeat(20);
    for short in ["ok", "evet", "plan taslagi", "/compact"] {
        assert!(!related_paste(&strip_space(short), &strip_space(&long)));
        assert!(!related_paste(&strip_space(&long), &strip_space(short)));
    }
}

#[test]
fn composer_block_reason_names_what_a_human_must_fix() {
    let cases: Vec<(&str, String, &str)> = vec![
        ("empty", claude_pane1(EMPTY_ROW), ""),
        (
            "our own hanging paste is not a block",
            claude_pane1(&format!("❯ {STUCK_MESSAGE}")),
            "",
        ),
        (
            "our damaged paste is not a block",
            claude_pane1(&format!("❯ {}", &STUCK_MESSAGE[55..])),
            "",
        ),
        (
            "chip",
            claude_pane1("❯ [Pasted text #1 +12 lines]"),
            BLOCKED_BY_PASTE_CHIP,
        ),
        (
            "codex chip",
            claude_pane1("❯ [Pasted Content 1024 chars]"),
            BLOCKED_BY_PASTE_CHIP,
        ),
        (
            "foreign line",
            claude_pane1("❯ my unfinished question is still here"),
            BLOCKED_BY_FOREIGN_TEXT,
        ),
        (
            "short fragment",
            claude_pane1("❯ /rename wor"),
            BLOCKED_BY_FOREIGN_TEXT,
        ),
        (
            "busy",
            format!(
                "✻ Working… (23s · Esc to interrupt)\n{}",
                claude_pane1(EMPTY_ROW)
            ),
            BLOCKED_BY_BUSY_PANE,
        ),
    ];
    for (name, pane, want) in &cases {
        assert_eq!(
            composer_block_reason(pane, &[STUCK_MESSAGE]),
            *want,
            "{name}"
        );
    }
}

/// `TestComposerBoxAgainstLiveCaptures`: runs only when PANEDIR points at a
/// directory of read-only pane captures; prints structure, never text.
#[test]
fn composer_box_against_live_captures() {
    let Ok(dir) = std::env::var("PANEDIR") else {
        eprintln!("skipped: set PANEDIR to a directory of read-only pane captures");
        return;
    };
    if dir.is_empty() {
        return;
    }
    let (mut readable, mut total) = (0, 0);
    for entry in std::fs::read_dir(&dir).expect("read PANEDIR") {
        let entry = entry.expect("entry");
        if entry.file_type().expect("type").is_dir() {
            continue;
        }
        let data = std::fs::read(entry.path()).expect("read capture");
        let pane = String::from_utf8_lossy(&data);
        total += 1;
        let at = composer_box_at(&pane);
        if at.is_some() {
            readable += 1;
        }
        let (text, top) = at.clone().unwrap_or_default();
        eprintln!(
            "{:<24} ok={:<5} rows={:<3} chars={:<5} scrolled={:<5} typing={:<5} filled={:<5} busy={:<5} reason={:?}",
            entry.file_name().to_string_lossy(),
            at.is_some(),
            composer_box_rows(&text),
            strip_space(&text).len(),
            at.is_some() && composer_box_scrolled(&pane, &text, top),
            typing(&pane),
            composer_filled(&pane),
            busy(&pane),
            composer_block_reason::<&str>(&pane, &[]),
        );
    }
    assert!(total > 0, "PANEDIR holds no captures");
    assert!(
        readable * 2 > total,
        "composerBox read only {readable} of {total} live panes"
    );
}

#[test]
fn wide_rune_message_is_ours_not_damaged() {
    let sent =
        "[bp] WARNING: on the command line ✍️ / ✅ DO NOT WRITE emoji — invisible carries Unicode.";
    let rendered = sent.replacen("invisible", "invisble", 1);
    let pane = claude_pane1(&format!("❯ {rendered}"));
    assert_eq!(classify_paste(&pane, &[sent]), (PasteVerdict::Exact, sent));
    let plain =
        "[bp] WARNING: on the command line do not write emoji, invisible carries Unicode friend.";
    let plain_pane = claude_pane1(&format!("❯ {}", plain.replacen("invisible", "invisble", 1)));
    assert_eq!(
        classify_paste(&plain_pane, &[plain]).0,
        PasteVerdict::Damaged
    );
    assert!(
        !has_wide_runes(plain),
        "a plain ASCII message was called wide"
    );
    for wide in ["✍️", "✅", "🚀", "日本語"] {
        assert!(has_wide_runes(wide), "{wide:?} was not recognised as wide");
    }
}

#[test]
fn submit_time_comparison_tolerates_wide_rune_render() {
    let message = "[bp] WARNING: on the command line ✍️ / ✅ DO NOT WRITE emoji — invisible carries Unicode, the security scan blocks it.";
    let want = strip_space(message);
    let rendered = message.replacen("invisible", "invisble", 1);
    let pane = claude_pane1(&format!("❯ {rendered}"));
    assert_eq!(classify_composer(&pane, &want), ComposerVerdict::Mine);
    assert_eq!(
        classify_composer(
            &claude_pane1("❯ a completely different sentence written by a person ✍️"),
            &want
        ),
        ComposerVerdict::Other
    );
    let plain = strip_space("[bp] plain-text message with no wide characters and enough length.");
    assert_eq!(
        classify_composer(
            &claude_pane1("❯ [bp] plain-text message with no wide charcters and enough length."),
            &plain
        ),
        ComposerVerdict::Other
    );
}

#[test]
fn damaged_paste_refuses_incomplete_view() {
    let message = "[server-main] a sufficiently long message; we need to distinguish whether it is torn or shifted; that is all.";
    let torn = format!("\u{276f} {}", &message[40..]);
    assert!(
        !damaged_paste(&screen_filling_pane(&[&torn]), &[message]),
        "a scrolled composer was declared damaged"
    );
    assert!(
        damaged_paste(&claude_pane1(&torn), &[message]),
        "a torn paste on a complete view was not recognised"
    );
}

#[test]
fn busy_pane_fixture_reads_busy() {
    // busyPane is the fixture the client tests deliver into; its pure half.
    assert!(busy(&busy_pane(&[EMPTY_ROW])));
}
