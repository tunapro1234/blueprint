//! Port of the pure tests of internal/tmux/claude_viewport_test.go,
//! usage_limit_test.go and remote_status_test.go.

use super::fixtures::*;
use crate::error::{Error, ErrorKind};
use crate::screen::composer::*;
use crate::screen::harness::claude::claude_composer_tail_matches;
use crate::screen::region::*;

const USAGE_NOTICES: [&str; 3] = [
    "  ⚠ Usage limit reached · limit resets 5:40pm · clau.de/wrap-up · /upgrade to keep using …",
    "  ⚠ While you wait, start a new cloud session by claiming a $250 credit",
    "  ⚠ /low-priority to continue now at lower priority · uses your weekly limit",
];

/// `claudePaneWithUsageLimit`: notices inserted under the bottom border.
pub fn claude_pane_with_usage_limit(pane: &str, notices: &[&str]) -> String {
    let lines: Vec<&str> = pane.split('\n').collect();
    for (i, line) in lines.iter().enumerate() {
        if *line == BOX_BORDER_BOTTOM {
            let mut out: Vec<&str> = lines[..=i].to_vec();
            out.extend_from_slice(notices);
            out.extend_from_slice(&lines[i + 1..]);
            return out.join("\n");
        }
    }
    pane.to_string()
}

/// `wordWrap`: Claude's word wrap at `width` runes.
pub fn word_wrap(text: &str, width: usize) -> Vec<String> {
    let mut rows = Vec::new();
    let mut runes: Vec<char> = text.chars().collect();
    while runes.len() > width {
        let mut cut = width;
        for i in (1..=width).rev() {
            if runes[i] == ' ' {
                cut = i + 1;
                break;
            }
        }
        rows.push(runes[..cut].iter().collect());
        runes = runes[cut..].to_vec();
    }
    rows.push(runes.iter().collect());
    rows
}

/// `claudeViewportTerminal.pane`: what a Claude pane of `width` columns
/// showing at most `visible_rows` composer rows renders for `composer`.
pub fn claude_viewport_pane(
    composer: &str,
    width: i64,
    visible_rows: usize,
    usage_limited: bool,
) -> String {
    let pane = if composer.is_empty() {
        claude_pane(&[EMPTY_ROW])
    } else {
        let mut rows = word_wrap(composer, composer_content_width(width) as usize);
        if rows.len() > visible_rows {
            rows = rows[rows.len() - visible_rows..].to_vec();
        }
        let composer_rows: Vec<String> = rows
            .iter()
            .enumerate()
            .map(|(i, row)| {
                if i == 0 {
                    format!("❯ {row}")
                } else {
                    format!("  {row}")
                }
            })
            .collect();
        let refs: Vec<&str> = composer_rows.iter().map(String::as_str).collect();
        claude_pane(&refs)
    };
    if usage_limited {
        claude_pane_with_usage_limit(&pane, &USAGE_NOTICES)
    } else {
        pane
    }
}

#[test]
fn claude_composer_tail_viewport_matcher() {
    let want = format!("{}TAIL-0123456789", "abcdefgh".repeat(11));
    let tail = &want[want.len() - 48..];
    let foreign = "foreign text ".repeat(5);
    let cases: Vec<(&str, &str, bool)> = vec![
        ("tail substring", tail, true),
        ("middle substring without tail", &want[16..64], false),
        ("foreign text", &foreign, false),
        ("short suffix", &want[want.len() - 12..], false),
        ("lost head", &want[3..], false),
    ];
    for (name, got, expected) in cases {
        let pane = claude_pane1(&format!("❯ {got}"));
        assert_eq!(
            claude_composer_tail_matches(&pane, &want, 59),
            expected,
            "{name}"
        );
    }
    let pane = claude_pane1(&format!("❯ {tail}"));
    assert_eq!(
        paste_integrity(&pane, &want, Some(59)),
        PasteVerification::Intact
    );
    assert!(!claude_composer_tail_matches(&pane, &want, 0));
}

/// Pure half of the clipped-viewport delivery tests
/// (`TestClaudeClippedComposerDeliversOnce`,
/// `TestClaudeClippedSpacedMessageDeliversOnce`,
/// `TestClaudeClippedWordWrappedMessageDeliversOnce`): the clipped view of
/// our whole paste verifies as intact, holds the message and is ours.
#[test]
fn claude_clipped_viewport_of_our_paste_is_intact() {
    let digits = format!("{}END!", "0123456789".repeat(45));
    let spaced = "[probot-egitim] Starter Bot 1.7, 1.8, 1.9 yazimi. Brief: /srv/probot/egitim/mufredat/araclar/starter-bot-1-7-1-9-brief-2026-09-27.md (once onu, sonra oradaki Once oku listesini oku). 1.4-1.6 entegre edildi ve dev yayinda, koordinator duzeltmeleri brief icinde. Sira 1.7, 1.8, 1.9. Her derste teslim hazir mesaji, sonunda damitma ve skill-yedekle.";
    assert!(word_wrap(spaced, composer_content_width(59) as usize).len() > 6);
    let cases: Vec<(&str, i64, usize)> = vec![
        (&digits, 59, 6),
        (spaced, 59, 6),
        (FINANCE_MESSAGE, 64, 8),
        (FINANCE_MESSAGE, 64, 10),
        (FINANCE_MESSAGE, 64, 11),
    ];
    for (message, width, rows) in cases {
        let pane = claude_viewport_pane(message, width, rows, false);
        assert_eq!(
            paste_integrity(&pane, message, Some(width)),
            PasteVerification::Intact,
            "{width}x{rows}: {message:.30}"
        );
        assert!(composer_holds_message_at_width(&pane, message, width));
        assert_eq!(
            classify_composer_at_width(&pane, message, width),
            ComposerVerdict::Mine
        );
    }
}

/// Pure half of `TestClaudeClippedForeignDraftIsUntouched`.
#[test]
fn claude_clipped_foreign_draft_blocks() {
    let message = format!("{}END!", "0123456789".repeat(45));
    let pane = claude_viewport_pane(&"foreign draft ".repeat(40), 59, 6, false);
    assert_eq!(
        composer_block_reason(&pane, &[&message]),
        BLOCKED_BY_FOREIGN_TEXT
    );
}

pub const FINANCE_MESSAGE: &str = concat!(
    "[probot-finance] probot-finance (Claude, /srv/probot/finance) — ",
    "yardım: Tuna laptopundaki para-main agentı (kimliği \"para-main@tuna-laptop\") ",
    "bana [external:para-main@tuna-laptop] önekiyle mesaj attı ama ben ona geri ",
    "yazamıyorum: SendMessage \"para-main@tuna-laptop\" -> \"bare teammate name olmalı\", ",
    "\"para-main\" -> \"reachable değil\"; ListAgents ve bp status da görmüyor. Ona ",
    "nasıl cevap gönderirim? (bp p2p / bp msg para-main@tuna-laptop / bp attach ... ",
    "hangisi doğru, bağlantı kurulu mu?) Cevabı bp msg probot-finance ile at. ",
    "İletilecek metin hazır: bütçe formatı + equity kuralları, veri/rakam yok."
);

#[test]
fn wrapped_message_rows_counts_word_wrap() {
    let rows = wrapped_message_rows(FINANCE_MESSAGE, 64).expect("rows");
    let want = word_wrap(FINANCE_MESSAGE, composer_content_width(64) as usize).len() as i64;
    assert_eq!(rows, want);
    assert!(rows > 10);
    let long = "x".repeat(25);
    let cases: Vec<(&str, i64, i64)> = vec![
        ("", 14, 1),
        ("short", 14, 1),
        ("0123456789", 14, 1),
        ("0123456789a", 14, 2),
        ("aaaa bbbb cc", 14, 2),
        ("aaaa bbb c", 14, 1),
        (&long, 14, 3),
        ("one\ntwo", 14, 2),
    ];
    for (message, width, want) in cases {
        assert_eq!(
            wrapped_message_rows(message, width).ok(),
            Some(want),
            "{message:?} {width}"
        );
    }
    let err = wrapped_message_rows("x", COMPOSER_INDENT + COMPOSER_RIGHT_MARGIN)
        .expect_err("a pane with no content columns must be an error");
    assert_eq!(err.to_string(), "invalid pane width 4");
}

#[test]
fn composer_rune_width_follows_go_table() {
    assert_eq!(composer_rune_width('a'), 1);
    assert_eq!(composer_rune_width('\u{0301}'), 0); // Mn
    assert_eq!(composer_rune_width('\u{20DD}'), 0); // Me
    assert_eq!(composer_rune_width('\u{200D}'), 0);
    assert_eq!(composer_rune_width('日'), 2);
    assert_eq!(composer_rune_width('🚀'), 2);
    assert_eq!(composer_rune_width('✅'), 1); // outside Go's wide ranges
    assert_eq!(composer_rune_width('\u{FE0F}'), 0); // Mn
}

#[test]
fn composer_clear_budget_sizes_from_the_message() {
    let pane = claude_pane(&[EMPTY_ROW]);
    assert_eq!(composer_clear_budget(&pane, "short", 80).ok(), Some(5));
    let long = "x".repeat(76 * 70);
    assert_eq!(composer_clear_budget(&pane, &long, 80).ok(), Some(64));
    assert!(composer_clear_budget(&pane, "x", 4).is_err());
}

#[test]
fn usage_limit_detection_uses_claude_footer() {
    let full_notice = USAGE_NOTICES[0];
    let incident = claude_pane_with_usage_limit(
        &claude_pane(&[
            "❯ [blueprint] [blueprint] q696812417 delivered at 09:18, complete and once; writer is",
            "  working on it. Cause: ...",
            "  no action needed from you.",
        ]),
        &USAGE_NOTICES,
    );
    let cases: Vec<(&str, String, bool)> = vec![
        ("incident screen", incident, true),
        (
            "transcript only",
            format!(
                "  agent: ⚠ Usage limit reached · limit resets 5:40pm\n{}",
                claude_pane(&[EMPTY_ROW])
            ),
            false,
        ),
        (
            "truncated notice",
            claude_pane_with_usage_limit(
                &claude_pane(&[EMPTY_ROW]),
                &["  ⚠ Usage limit reached · limit res…"],
            ),
            true,
        ),
        ("normal idle Claude", claude_pane(&[EMPTY_ROW]), false),
        (
            "ANSI and mixed case",
            claude_pane_with_usage_limit(
                &claude_pane(&[EMPTY_ROW]),
                &["\x1b[2m  ⚠ uSaGe LiMiT ReAcHeD · limit resets 5:40pm …\x1b[0m"],
            ),
            true,
        ),
    ];
    for (name, pane, want) in &cases {
        let reason = usage_limit_reason(pane);
        assert_eq!(!reason.is_empty(), *want, "{name}: {reason:?}");
    }
    let pane = claude_pane_with_usage_limit(&claude_pane(&[EMPTY_ROW]), &[full_notice]);
    let reason = usage_limit_reason(&pane);
    assert!(reason.contains("limit resets 5:40pm"), "{reason:?}");
    let err = usage_limit_error(&pane);
    assert!(err.is(ErrorKind::UsageLimited));
    assert_eq!(err.to_string(), reason);
    let bare =
        claude_pane_with_usage_limit(&claude_pane(&[EMPTY_ROW]), &["  ⚠ Usage limit reached"]);
    assert_eq!(
        usage_limit_error(&bare),
        Error::UsageLimited { suffix: None }
    );
}

/// Pure half of `TestSendRefusesUsageLimitedClaudeBeforePaste`.
#[test]
fn usage_limited_viewport_is_detected_before_paste() {
    let pane = claude_viewport_pane("", 80, 8, true);
    assert!(!usage_limit_reason(&pane).is_empty());
    assert!(composer_empty(&pane));
}

#[test]
fn remote_control_status_table() {
    let url = "https://claude.ai/code/session_01ABC";
    let active = format!(
        "❯ /remote-control\n  ⎿  /remote-control is active · Continue here, on your phone, or at {url}\n"
    );
    let disconnected = "● Remote Control disconnected — signed-in claude.ai account or organization changed on this machine — run /remote-control to start a session for the current account\n";
    let cases: Vec<(&str, String, RemoteControl, &str)> = vec![
        ("never", "❯ hello\n".into(), RemoteControl::Unknown, ""),
        ("active", active.clone(), RemoteControl::Active, url),
        (
            "wrapped url",
            format!(
                "  /remote-control is active · Continue here, on your phone, or at\n  {url}.\n"
            ),
            RemoteControl::Active,
            url,
        ),
        (
            "dropped",
            format!("{active}{disconnected}"),
            RemoteControl::Disconnected,
            "",
        ),
        (
            "reconnected",
            format!(
                "{active}{disconnected}❯ /remote-control\n  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_02NEW\n"
            ),
            RemoteControl::Active,
            "https://claude.ai/code/session_02NEW",
        ),
        (
            "quoted disconnect",
            format!("{active}     compec-main: ● Remote Control disconnected — signed-in\n"),
            RemoteControl::Active,
            url,
        ),
        (
            "quoted active",
            format!("> the line reads /remote-control is active · see {url}\n"),
            RemoteControl::Unknown,
            url,
        ),
    ];
    for (name, pane, state, want_url) in &cases {
        let (got_state, got_url, reason) = remote_control_status(pane);
        assert_eq!((got_state, got_url.as_str()), (*state, *want_url), "{name}");
        assert_eq!(
            got_state == RemoteControl::Disconnected,
            !reason.is_empty(),
            "{name}: reason={reason:?}"
        );
    }
    let (_, _, reason) = remote_control_status(&format!("{active}{disconnected}"));
    assert!(
        reason.starts_with("Remote Control disconnected"),
        "{reason:?}"
    );
}
