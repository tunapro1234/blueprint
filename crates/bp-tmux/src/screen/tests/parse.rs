//! Port of the pure tests of internal/tmux/parse_test.go.

use super::fixtures::*;
use crate::screen::gostr::strip_space;
use crate::screen::harness::codex::codex_pane;
use crate::screen::region::*;

#[test]
fn typing_uses_only_last_prompt() {
    let mut pane = String::from("❯ old message\nresponse\n  ❯ \u{a0}  \t\n");
    assert!(
        !typing(&pane),
        "empty final composer was reported as typing"
    );
    pane.push_str("output\n  ❯ new message\n");
    assert!(
        typing(&pane),
        "non-empty final composer was not reported as typing"
    );
}

#[test]
fn typing_recognizes_codex_prompt() {
    assert!(!typing("output\n  › \u{a0} \t\n"));
    assert!(typing("output\n  › draft message\n"));
}

#[test]
fn typing_no_prompt() {
    assert!(!typing("normal output\n❯\u{a0}\nmore output"));
}

#[test]
fn busy_reads_the_live_spinner() {
    let quoted_deep: Vec<&str> = std::iter::once(QUOTED_SPINNER).chain(filler(9)).collect();
    let spinner_deep: Vec<&str> = std::iter::once("✻ Baking… (30s · ↓ 943 tokens)")
        .chain(filler(9))
        .collect();
    let cases: Vec<(&str, String, bool)> = vec![
        (
            "spinner with thinking segment",
            spinner_pane(&[
                "  agent: output",
                "✻ Baking… (2m 32s · ↓ 6.1k tokens · thought for 6s)",
            ]),
            true,
        ),
        (
            "spinner short timer",
            spinner_pane(&["  agent: output", "✽ Baking… (30s · ↓ 943 tokens)"]),
            true,
        ),
        (
            "spinner other verb",
            spinner_pane(&["  agent: output", "✻ Symbioting… (29s · ↓ 163 tokens)"]),
            true,
        ),
        (
            "spinner dim frame glyph",
            spinner_pane(&["  agent: output", "· Symbioting… (2m 13s · ↓ 422 tokens)"]),
            true,
        ),
        (
            "spinner before the counter appears",
            spinner_pane(&["  agent: output", "✽ Unravelling…"]),
            true,
        ),
        (
            "spinner before the counter, dim frame",
            spinner_pane(&["  agent: output", "· Misting…"]),
            true,
        ),
        (
            "counter without a token segment",
            spinner_pane(&[
                "  agent: output",
                "✻ Marinating… (1s · thinking with medium effort)",
            ]),
            true,
        ),
        (
            "counter with tokens and thinking",
            spinner_pane(&[
                "  agent: output",
                "· Marinating… (5s · ↓ 256 tokens · thought for 2s)",
            ]),
            true,
        ),
        (
            "finished turn",
            spinner_pane(&["  agent: output", "✻ Baked for 3s"]),
            false,
        ),
        (
            "finished turn with a background shell",
            spinner_pane(&[
                "  agent: output",
                "✻ Baked for 6m 19s · 1 shell still running",
            ]),
            false,
        ),
        (
            "waiting for a background agent",
            spinner_pane(&[
                "  agent: output",
                "✻ Waiting for 1 background agent to finish",
            ]),
            false,
        ),
        (
            "tool box with spinner",
            spinner_pane(&[
                "  ⎿  $ go test ./... (27s · 28 lines)",
                "     (ctrl+b ctrl+b (twice) to run in background)",
                "✽ Baking… (30s · ↓ 943 tokens)",
            ]),
            true,
        ),
        (
            "quoted spinner in the transcript",
            spinner_pane(&quoted_deep),
            false,
        ),
        (
            "spinner row far above the box",
            spinner_pane(&spinner_deep),
            false,
        ),
        (
            "quoted spinner above the box",
            spinner_pane(&["  agent: output", QUOTED_SPINNER]),
            false,
        ),
        (
            "legacy esc-to-interrupt spinner",
            spinner_pane(&["  agent: output", "✻ Working… (23s · esc to interrupt)"]),
            true,
        ),
        (
            "background shell footer",
            spinner_pane(&[
                "  agent: output",
                "⏵⏵ bypass permissions on · 2 shells · esc to interrupt",
            ]),
            false,
        ),
        (
            "idle pane",
            spinner_pane(&["  agent: output", "  agent: done"]),
            false,
        ),
    ];
    for (name, pane, want) in &cases {
        assert_eq!(busy(pane), *want, "{name}");
    }
}

#[test]
fn busy_requires_live_indicator() {
    assert!(busy("✻ Working… (23s · Esc to interrupt)"));
    assert!(busy(
        "⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt"
    ));
    assert!(
        !busy(r#"transcript quoting "esc to interrupt" in a rule message"#)
            && !busy("Esc to interrupt")
            && !busy("⏵⏵ bypass permissions on · 2 shells · esc to interrupt")
            && !busy("ready"),
        "false positive busy"
    );
}

#[test]
fn codex_busy_queue_detection() {
    assert!(codex_busy_queue(BUSY_QUEUE_CHIP));
    assert!(!codex_busy_queue(COLLAPSED_CHIP) && !codex_busy_queue(EXPANDED_CHIP));
    assert!(!codex_busy_queue(GHOST_COMPOSER) && !codex_busy_queue("output\n  ›   \t\n"));
    assert!(!codex_busy_queue(
        "✻ Working… (23s · Esc to interrupt)\n❯ \n"
    ));
    assert!(!codex_busy_queue(
        "\x1b[1m›\x1b[0m please tab to queue message for me later\n"
    ));
}

#[test]
fn codex_paste_chip_detection() {
    assert!(codex_paste_chip(COLLAPSED_CHIP));
    assert!(codex_paste_chip(EXPANDED_CHIP));
    assert!(codex_paste_chip("› [Pasted Content 2000 chars]\n"));
    assert!(!codex_paste_chip(GHOST_COMPOSER));
    assert!(!codex_paste_chip("output\n  ›   \t\n"));
    assert!(!codex_paste_chip("❯ real user text\n"));
    assert!(!codex_paste_chip(
        "❯ I just Pasted Content 5 chars into the box\n"
    ));
    assert!(!codex_paste_chip("  gpt-5.6-sol low · /tmp\n"));
}

#[test]
fn is_agent_command_whitelist() {
    for cmd in ["claude", "codex", "bwrap"] {
        assert!(is_agent_command(cmd), "{cmd}");
    }
    for cmd in ["zsh", "bash", "sh", "dash", "fish", "tmux", "node", ""] {
        assert!(!is_agent_command(cmd), "{cmd}");
    }
}

/// The pure half of `TestNodePaneNeedsCodexOnScreen` (the Send half needs
/// the client harness).
#[test]
fn node_pane_needs_codex_on_screen() {
    assert!(codex_pane(
        "• Working (0s • esc to interrupt)\n› Ask Codex to do anything\n  gpt-5.6-sol medium fast · /srv/probot/out-codex\n"
    ));
    for row in [
        "› Ask Codex to do anything",
        "• Working (0s • esc to interrupt)",
        "  gpt-5.6-sol medium fast · /srv/probot/out-codex",
        "[Pasted Content 1024 chars]",
    ] {
        assert!(
            codex_pane(&format!("{row}\n")),
            "marker not recognised: {row:?}"
        );
    }
    assert!(!codex_pane(
        "the user said: Ask Codex to do anything, then wait\n"
    ));
    assert!(!crate::screen::is_agent_pane(
        "node",
        "npm run dev\n> build succeeded\n"
    ));
}

#[test]
fn composer_trail_detection() {
    assert_eq!(
        composer_trail(&format!("❯ /compact\n\n{COMPOSER_BORDER}  -- INSERT --\n")),
        (1, false, true)
    );
    assert_eq!(
        composer_trail(&format!("❯ /compact\n\n\n{COMPOSER_BORDER}")),
        (2, false, true)
    );
    assert_eq!(
        composer_trail(&format!("❯ /compact\n{COMPOSER_BORDER}")),
        (0, false, true)
    );
    assert_eq!(
        composer_trail(&format!(
            "❯ a very long single line message that\n  wraps onto a second rendered line\n{COMPOSER_BORDER}"
        )),
        (0, true, true)
    );
    assert!(
        composer_trail(&format!(
            "❯ long message that\n  wraps here\n\n{COMPOSER_BORDER}"
        ))
        .1
    );
    assert!(!composer_trail("❯ /compact\n").2);
    let pane = "\x1b[39m❯  \x1b[38;5;153m/compact\x1b[39m\n\n\x1b[38;5;244m──────────\x1b[39m\n";
    assert_eq!(composer_trail(pane), (1, false, true));
}

#[test]
fn auth_expired_detection() {
    let border = COMPOSER_BORDER.trim_end_matches('\n');
    let expired = format!(
        "  \x1b[38;5;153m> summarize the repo\x1b[39m\n\x1b[39m❯  \x1b[39m\n\x1b[38;5;244m{border}\x1b[39m\n  \x1b[2m⏵⏵ bypass permissions on\x1b[0m   \x1b[31m●\x1b[39m Login expired · Please run /login\n"
    );
    assert!(auth_expired(&expired), "expired-login footer not detected");
    assert!(auth_expired(&format!(
        "❯  \n{COMPOSER_BORDER}\x1b[2m● Login expired · Please run /login\x1b[0m\n"
    )));
    let transcript = format!(
        "  ● Login expired · Please run /login is the banner we are fixing\n  I grepped for \"login expired\" across the fleet\n❯  \n{COMPOSER_BORDER}  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
    );
    assert!(!auth_expired(&transcript));
    let typed =
        format!("❯ why does ● Login expired · Please run /login show up?\n{COMPOSER_BORDER}");
    let wrapped =
        format!("❯ why does ● Login expired ·\n  Please run /login show up?\n{COMPOSER_BORDER}");
    assert!(!auth_expired(&typed) && !auth_expired(&wrapped));
    assert!(
        !auth_expired(&format!(
            "❯  \n{COMPOSER_BORDER}  ⏵⏵ bypass permissions on\n"
        )) && !auth_expired("✻ Working… (23s · Esc to interrupt)\n")
            && !auth_expired("● Login expired · Please run /login\n")
    );
    assert!(!auth_expired(&format!(
        "❯  \n{COMPOSER_BORDER}  login expired means the session died\n"
    )));
}

#[test]
fn classify_composer_table() {
    let want = strip_space("deploy the new bar chips");
    let cases: Vec<(&str, String, ComposerVerdict)> = vec![
        (
            "empty",
            format!("❯  \n{COMPOSER_BORDER}"),
            ComposerVerdict::Cleared,
        ),
        (
            "exact",
            format!("❯ deploy the new bar chips\n{COMPOSER_BORDER}"),
            ComposerVerdict::Mine,
        ),
        (
            "wrapped first row only",
            format!("❯ deploy the new\n{COMPOSER_BORDER}"),
            ComposerVerdict::Mine,
        ),
        (
            "user appended",
            format!("❯ deploy the new bar chips please\n{COMPOSER_BORDER}"),
            ComposerVerdict::Mine,
        ),
        (
            "claude paste chip",
            format!("❯ [Pasted text #1 +12 lines]\n{COMPOSER_BORDER}"),
            ComposerVerdict::Mine,
        ),
        (
            "codex paste chip",
            COLLAPSED_CHIP.to_string(),
            ComposerVerdict::Mine,
        ),
        (
            "foreign fragment",
            format!("❯ ls -la /srv\n{COMPOSER_BORDER}"),
            ComposerVerdict::Other,
        ),
    ];
    for (name, pane, verdict) in &cases {
        assert_eq!(classify_composer(pane, &want), *verdict, "{name}");
    }
}
