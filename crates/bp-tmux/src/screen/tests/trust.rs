//! Port of the pure tests of internal/tmux/claude_trust_test.go and
//! codex_trust_test.go.

use crate::screen::trust::*;

const CODEX_TRUST_PANE: &str = r#"  Folder access
  /work/new
  Trust this folder? Codex can read, edit, and run files here, subject to your permission
  settings. Folder settings can run code automatically, even without a model request. Continue
  only if you trust these files. Your trust decision will be saved.
› 1. Trust and continue
  2. Back to Agent Command Center
  enter continue · esc back
"#;

#[test]
fn codex_trust_modal_table() {
    let back = CODEX_TRUST_PANE
        .replacen("› 1. Trust", "  1. Trust", 1)
        .replacen("  2. Back", "› 2. Back", 1);
    let no_footer = CODEX_TRUST_PANE.replacen("enter continue · esc back", "", 1);
    let cases: Vec<(&str, &str, &str, bool)> = vec![
        ("requested folder", CODEX_TRUST_PANE, "/work/new", true),
        ("other folder", CODEX_TRUST_PANE, "/work/other", false),
        ("back selected", &back, "/work/new", false),
        ("no footer", &no_footer, "/work/new", false),
        (
            "composer",
            "› trust this folder /work/new\n",
            "/work/new",
            false,
        ),
    ];
    for (name, pane, dir, want) in cases {
        assert_eq!(codex_trust_modal(pane, dir), want, "{name}");
    }
    assert!(codex_trust_screen(CODEX_TRUST_PANE));
    assert!(!codex_trust_screen("› explain why we trust this folder\n"));
}

/// `TestCodexOpenDoesNotTreatForeignTrustScreenAsReady`, screen half: a trust
/// screen for another folder is not answered, but is still a trust screen.
#[test]
fn codex_foreign_trust_screen_is_not_answered() {
    let pane = CODEX_TRUST_PANE.replacen("/work/new", "/work/elsewhere", 1);
    assert!(!codex_trust_modal(&pane, "/work/new"));
    assert!(codex_trust_screen(&pane));
    assert_eq!(crate::screen::launch_wait(&pane), "harness trust prompt");
}

const CLAUDE_TRUST_FIXTURE: &str = r#"Accessing workspace:

 /srv/agent

 Quick safety check: Is this a project you created or one you trust? (Like your own
 code, a well-known open source project, or work from your team).

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
"#;

const CLAUDE_TRUST_FIXTURE_283: &str = r#"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent command claude --dangerously-skip-permissions
[insert root@host agent]# CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent

────────────────────────────────────────
 Accessing workspace:

 /srv/agent

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source
 project, or work from your team). If not, take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
"#;

#[test]
fn claude_trust_modal_table() {
    let f = CLAUDE_TRUST_FIXTURE;
    let f283 = CLAUDE_TRUST_FIXTURE_283;
    let moved = f283.replacen("❯ No, exit", "  No, exit", 1).replacen(
        "  Yes, I trust",
        "❯ Yes, I trust",
        1,
    );
    use ClaudeTrustAction::{Confirm, None as Nothing, Select};
    let cases: Vec<(&str, String, ClaudeTrustAction)> = vec![
        ("startup", f.to_string(), Confirm),
        (
            "restricted alternative",
            f.replace("No, exit", "No, continue without these permissions"),
            Confirm,
        ),
        (
            "wrong directory",
            f.replace("/srv/agent", "/srv/other"),
            Nothing,
        ),
        (
            "words in composer",
            "❯ Quick safety check trust this folder directory".to_string(),
            Nothing,
        ),
        (
            "quoted modal in composer",
            format!("──────────\n❯ {f}──────────\n"),
            Nothing,
        ),
        ("historical modal", format!("{f}❯ user draft\n"), Nothing),
        ("refusal selected", f.replace("❯ 1.", "  1."), Nothing),
        (
            "missing footer",
            f.replace("Enter to confirm · Esc to cancel", ""),
            Nothing,
        ),
        ("unselected approval", f.replace("❯ 1.", "1."), Nothing),
        (
            "numbered refusal selected above nothing",
            f.replace("❯ 1.", "  1.").replacen("  2. No", "❯ 2. No", 1),
            Nothing,
        ),
        ("2.1.283 refusal first", f283.to_string(), Select),
        (
            "2.1.283 restricted refusal",
            f283.replace("No, exit", "No, continue without these permissions"),
            Select,
        ),
        ("2.1.283 approval selected", moved, Confirm),
        (
            "2.1.283 wrong directory",
            f283.replace("/srv/agent", "/srv/other"),
            Nothing,
        ),
        (
            "2.1.283 missing footer",
            f283.replace("Enter to confirm · Esc to cancel", ""),
            Nothing,
        ),
        (
            "2.1.283 approval not adjacent",
            f283.replacen(
                "   Yes, I trust this folder",
                "   Other\n   Yes, I trust this folder",
                1,
            ),
            Nothing,
        ),
        (
            "2.1.283 in composer",
            format!("──────────\n❯ {f283}──────────\n"),
            Nothing,
        ),
        (
            "2.1.283 rule inside",
            f283.replacen(" Security guide", " ──────────\n Security guide", 1),
            Nothing,
        ),
    ];
    for (name, pane, want) in &cases {
        assert_eq!(claude_trust_modal(pane, "/srv/agent"), *want, "{name}");
    }
}

/// Screen half of `TestOpenClaudeTrustMovesOffDefaultRefusal`: the terminal
/// model of claude_trust_test.go driven by the recognizer alone, without a
/// client. Down toggles the selection; Enter on the approval opens.
#[test]
fn claude_trust_terminal_reaches_approval_within_bound() {
    let screen = |approval: bool| {
        if approval {
            CLAUDE_TRUST_FIXTURE_283
                .replacen("❯ No, exit", "  No, exit", 1)
                .replacen("  Yes, I trust", "❯ Yes, I trust", 1)
        } else {
            CLAUDE_TRUST_FIXTURE_283.to_string()
        }
    };
    let mut approval = false;
    let mut keys = Vec::new();
    let mut ignore = 1;
    for _ in 0..CLAUDE_TRUST_MAX_KEYS {
        let key = match claude_trust_modal(&screen(approval), "/srv/agent") {
            ClaudeTrustAction::Select => "Down",
            ClaudeTrustAction::Confirm => "Enter",
            ClaudeTrustAction::None => break,
        };
        keys.push(key);
        if ignore > 0 {
            ignore -= 1;
            continue;
        }
        if key == "Down" {
            approval = !approval;
        } else {
            break;
        }
    }
    assert!(keys.join(" ").starts_with("Down Down Enter"), "{keys:?}");
    assert!(approval);
}
