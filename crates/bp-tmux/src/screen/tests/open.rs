//! Port of the pure tests of internal/tmux/open_test.go.

use crate::screen::region::*;

#[test]
fn is_shell_command_table() {
    for cmd in ["zsh", "bash", "sh", "dash", "fish"] {
        assert!(is_shell_command(cmd), "{cmd}");
    }
    for cmd in ["claude", "codex", "bwrap", "vim", "tmux", "node", ""] {
        assert!(!is_shell_command(cmd), "{cmd}");
    }
}

#[test]
fn remote_control_menu_detection() {
    let menu = "Remote Control\n❯ Continue\n  Show QR code\n  Disconnect this session\nEnter to select · Esc to cancel\n";
    assert!(remote_control_menu(menu), "full RC menu not detected");
    assert!(
        remote_control_menu("Remote Control\n❯ Continue\nEnter to select\n"),
        "RC menu without Disconnect line not detected"
    );
    assert!(
        !remote_control_menu("Resume from summary\n❯ Resume full session\nEnter to select\n"),
        "resume picker misdetected as RC menu"
    );
    assert!(
        !remote_control_menu("❯ \n"),
        "plain composer misdetected as RC menu"
    );
}
