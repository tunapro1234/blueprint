#!/bin/sh
set -eu

SERVER_ROOT=/srv/blueprint
INSTALL_MODE=${BP_INSTALL_MODE:-}
YES=${BP_YES:-0}
# Opt-in only: modules to enable and whether to start the guided coordinator.
ENABLE=${BP_ENABLE:-}
ONBOARD=${BP_ONBOARD:-no}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --yes|-y) YES=1 ;;
        --local) INSTALL_MODE=local ;;
        --server) INSTALL_MODE=server ;;
        --client) INSTALL_MODE=client ;;
        --onboard) ONBOARD=yes ;;
        --enable)
            [ "$#" -gt 1 ] || { printf 'error: --enable needs a module list, e.g. sessions,bar\n' >&2; exit 1; }
            shift
            ENABLE=${ENABLE:+$ENABLE,}$1
            ;;
        --enable=*) ENABLE=${ENABLE:+$ENABLE,}${1#--enable=} ;;
        *) printf 'error: unknown option: %s\n' "$1" >&2; exit 1 ;;
    esac
    shift
done

say() {
    printf '%s\n' "$*"
}

link_server_binary() {
    source_binary="$SERVER_ROOT/bp"
    target_binary=/usr/local/bin/bp
    if [ -e /etc/blueprint/home ] && [ "$(cat /etc/blueprint/home)" != "$SERVER_ROOT" ]; then
        say "error: /etc/blueprint/home selects another installation; preserving it"
        exit 1
    fi

    if [ ! -x "$source_binary" ]; then
        if ! command -v make >/dev/null 2>&1; then
            say "error: $source_binary is missing and make is not installed"
            exit 1
        fi
        say "building bp in $SERVER_ROOT"
        make -C "$SERVER_ROOT" build
    fi

    link_existed=no
    [ -e "$target_binary" ] || [ -L "$target_binary" ] && link_existed=yes
    home_existed=no
    [ -e /etc/blueprint/home ] && home_existed=yes
    if [ -w /usr/local/bin ]; then
        ln -sfn "$source_binary" "$target_binary"
    elif command -v sudo >/dev/null 2>&1; then
        sudo ln -sfn "$source_binary" "$target_binary"
    else
        say "error: cannot write /usr/local/bin; rerun as root"
        exit 1
    fi
    # Explicit machine selection preserves server behavior for existing agents.
    if [ -w /etc ]; then
        mkdir -p /etc/blueprint
        printf '%s\n' "$SERVER_ROOT" >/etc/blueprint/home
    else
        sudo mkdir -p /etc/blueprint
        printf '%s\n' "$SERVER_ROOT" | sudo tee /etc/blueprint/home >/dev/null
    fi
    RECORD_BP=$source_binary
    if [ "$link_existed" = no ]; then
        record_install link "$target_binary" "$source_binary"
    fi
    if [ "$home_existed" = no ]; then
        record_install file /etc/blueprint/home "$SERVER_ROOT"
    fi
    say "installed $target_binary -> $source_binary"
}

client_platform() {
    os=$(uname -s)
    arch=$(uname -m)
    case "$os" in
        Linux) os=linux ;;
        Darwin) os=darwin ;;
        *) say "error: unsupported operating system: $os"; exit 1 ;;
    esac
    case "$arch" in
        x86_64|amd64) arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) say "error: unsupported architecture: $arch"; exit 1 ;;
    esac
    PLATFORM="$os-$arch"
}

configure_remote() {
    config_dir="$HOME/.config/bp"
    config_file="$config_dir/config"
    if [ -f "$config_file" ]; then
        say "keeping existing $config_file"
        return
    fi

    remote=${BP_REMOTE:-}
    if [ -z "$remote" ]; then
        if [ ! -r /dev/tty ]; then
            say "error: set BP_REMOTE=user@host for a non-interactive install"
            exit 1
        fi
        printf 'Remote blueprint server (user@host): ' >/dev/tty
        IFS= read -r remote </dev/tty
    fi
    if [ -z "$remote" ]; then
        say "error: REMOTE cannot be empty"
        exit 1
    fi
    case "$remote" in
        *' '*|*'	'*) say "error: REMOTE must be a single user@host or SSH host"; exit 1 ;;
    esac

    method=${BP_REMOTE_METHOD:-mosh}
    case "$method" in
        mosh|ssh) ;;
        *) say "error: BP_REMOTE_METHOD must be mosh or ssh"; exit 1 ;;
    esac

    mkdir -p "$config_dir"
    umask 077
    printf 'REMOTE=%s\nREMOTE_METHOD=%s\n' "$remote" "$method" >"$config_file"
    say "created $config_file"
}

install_client_binary() {
    check_home_owner
    backup_binary=
    fetch_verified_release
    configure_remote
    install_dir="$HOME/.local/bin"
    mkdir -p "$install_dir"
    if [ -e "$install_dir/bp" ]; then
        backup_binary=$(mktemp "$install_dir/bp.before-client.XXXXXX")
        cp -p "$install_dir/bp" "$backup_binary"
    fi
    candidate_binary=$(mktemp "$install_dir/.bp-client.XXXXXX")
    cp "$local_tmp/bp" "$candidate_binary"
    chmod 755 "$candidate_binary"
    mv "$candidate_binary" "$install_dir/bp"
    RECORD_BP=$install_dir/bp
    if [ -z "${backup_binary:-}" ]; then
        record_install binary "$install_dir/bp"
    fi
    say "installed $install_dir/bp ($release_verified)"
}

fetch_verified_release() {
    client_platform
    local_tmp=$(mktemp -d "${TMPDIR:-/tmp}/bp-install.XXXXXX")
    trap 'rm -rf "$local_tmp"' EXIT HUP INT TERM
    if [ -n "${BP_LOCAL_BINARY:-}" ]; then
        cp "$BP_LOCAL_BINARY" "$local_tmp/bp"
        release_verified="from BP_LOCAL_BINARY; not signature-checked"
    else
        release_verified="signature and SHA-256 verified"
        command -v curl >/dev/null 2>&1 || { say "error: curl is required"; exit 1; }
        local_base=https://bp.tunapro.xyz
        local_version=${BP_VERSION:-$(curl --proto '=https' --proto-redir '=https' -fsSL "$local_base/latest.version")}
        printf '%s' "$local_version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { say "error: invalid release version"; exit 1; }
        release_base="$local_base/releases/v$local_version"
        curl --proto '=https' --proto-redir '=https' -fsSL "$release_base/checksums.txt" -o "$local_tmp/checksums"
        curl --proto '=https' --proto-redir '=https' -fsSL "$release_base/checksums.sig" -o "$local_tmp/checksums.sig"
        cat >"$local_tmp/release.pub" <<'BP_RELEASE_PUBLIC_KEY'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAs72CR5QTdrLCQfDS6dHh/igbOIL6gw5ayGufCHnADUw=
-----END PUBLIC KEY-----
BP_RELEASE_PUBLIC_KEY
        release_openssl=openssl
        for candidate_ssl in /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
            if [ -x "$candidate_ssl" ]; then release_openssl=$candidate_ssl; break; fi
        done
        "$release_openssl" pkeyutl -verify -pubin -inkey "$local_tmp/release.pub" -rawin -in "$local_tmp/checksums" -sigfile "$local_tmp/checksums.sig" >/dev/null 2>&1 || {
            say "error: release signature could not be verified; OpenSSL with Ed25519 support is required (macOS: brew install openssl@3)"; exit 1;
        }
        [ "$(head -n 1 "$local_tmp/checksums")" = "# bp-release $local_version" ] || { say "error: signed release version mismatch"; exit 1; }
        curl --proto '=https' --proto-redir '=https' -fsSL "$release_base/bp-$PLATFORM" -o "$local_tmp/bp"
        expected=$(awk -v name="bp-$PLATFORM" '$2 == name {print $1}' "$local_tmp/checksums")
        if command -v sha256sum >/dev/null 2>&1; then
            actual=$(sha256sum "$local_tmp/bp" | awk '{print $1}')
        else
            actual=$(shasum -a 256 "$local_tmp/bp" | awk '{print $1}')
        fi
        [ -n "$expected" ] && [ "$expected" = "$actual" ] || { say "error: SHA-256 mismatch"; exit 1; }
    fi
    chmod 755 "$local_tmp/bp"
    case "$("$local_tmp/bp" help)" in
        *'bp setup'*'bp config path|check'*'bp run '*) ;;
        *) say "error: published binary does not support local YAML setup yet; keeping your installation"; exit 1 ;;
    esac
}

# needs_tmux reports whether an opt-in the user chose runs agents in tmux.
needs_tmux() {
    case "$ONBOARD" in yes|1|true|auto) return 0 ;; esac
    case ",$ENABLE," in *,sessions,*|*,bar,*) return 0 ;; esac
    return 1
}

ensure_tmux() {
    if ! command -v tmux >/dev/null 2>&1; then
        if [ "$YES" != 1 ]; then
            say "error: tmux is required for the sessions/bar modules and onboarding; install it with your package manager, or rerun with --yes to allow dependency installation"
            exit 1
        fi
        case "$(uname -s)" in
            Darwin)
                command -v brew >/dev/null 2>&1 || { say "error: tmux is missing; install Homebrew, then rerun this command"; exit 1; }
                brew install tmux
                ;;
            Linux)
                command -v apt-get >/dev/null 2>&1 || { say "error: install tmux with your package manager, then rerun this command"; exit 1; }
                if [ "$(id -u)" = 0 ]; then
                    apt-get update
                    apt-get install -y tmux
                else
                    sudo apt-get update
                    sudo apt-get install -y tmux
                fi
                ;;
        esac
    fi
}

# Local mode changes nothing outside bp's own files: it installs the binary,
# bp's config and book under ~/.blueprint and a short skill that tells the
# user's agents bp exists. It does not install tmux, edit shell rc files or
# tmux options, start services or open an agent. Each of those is an opt-in
# module (bp modules, bp enable <module>).
# check_home_owner refuses a run as another user than the owner of $HOME
# (typically sudo with HOME kept), which would leave root-owned files there.
check_home_owner() {
    home_uid=$(stat -c %u "$HOME" 2>/dev/null || stat -f %u "$HOME" 2>/dev/null || echo "")
    if [ -n "$home_uid" ] && [ "$home_uid" != "$(id -u)" ] && [ "${BP_ALLOW_FOREIGN_HOME:-}" != 1 ]; then
        say "error: running as uid $(id -u) but $HOME belongs to uid $home_uid; run the installer as that user (or set BP_ALLOW_FOREIGN_HOME=1 if you mean it)"
        exit 1
    fi
}

install_local() {
    check_home_owner
    BP_HOME=${BP_HOME:-$HOME/.blueprint}
    export BP_HOME
    local_existing=no
    if [ -e "$HOME/.local/bin/bp" ] || [ -f "$BP_HOME/agentbook.json" ]; then
        local_existing=yes
    fi
    client_platform
    if needs_tmux; then
        ensure_tmux
    fi
    fetch_verified_release
    if [ -n "$ENABLE" ]; then
        set -- --enable "$ENABLE"
    else
        set --
    fi
    "$local_tmp/bp" setup --check "$@" || { say "error: installation preflight failed; existing binary preserved"; exit 1; }
    local_bin="$HOME/.local/bin"
    local_backup=
    mkdir -p "$local_bin"
    if [ -e "$local_bin/bp" ]; then
        local_backup=$(mktemp "$local_bin/bp.before-local.XXXXXX")
        cp -p "$local_bin/bp" "$local_backup"
    fi
    local_candidate=$(mktemp "$local_bin/.bp-install.XXXXXX")
    cp "$local_tmp/bp" "$local_candidate"
    chmod 755 "$local_candidate"
    mv "$local_candidate" "$local_bin/bp"
    if ! "$local_bin/bp" setup; then
        if [ -n "$local_backup" ]; then
            restore_candidate=$(mktemp "$local_bin/.bp-restore.XXXXXX")
            cp -p "$local_backup" "$restore_candidate"
            mv "$restore_candidate" "$local_bin/bp"
            say "error: setup failed; previous binary restored ($local_backup)"
        else
            failed_install=$(mktemp "$local_bin/bp.failed-install.XXXXXX")
            mv "$local_bin/bp" "$failed_install"
            say "error: setup failed; unsuccessful binary retained at $failed_install"
        fi
        exit 1
    fi
    if [ -z "$local_backup" ]; then
        "$local_bin/bp" _install-record binary "$local_bin/bp" || true
    fi
    say "installed $local_bin/bp"
    [ -z "$local_backup" ] || say "previous binary backup: $local_backup"
    case ":${PATH:-}:" in
        *":$local_bin:"*) ;;
        *) say "note: $local_bin is not on your PATH; add it, or run $local_bin/bp" ;;
    esac
    enable_modules "$local_bin/bp"
    if [ -z "$ENABLE" ]; then
        say "Nothing else on this machine was changed. Your agents can now use bp:"
    else
        say "Your agents can now use bp:"
    fi
    say "  bp status, bp msg <agent> <text>, bp help"
    say "Optional modules (tmux sessions, status bar, accounts, WhatsApp, web UI): bp modules"
    say "Remove everything bp added: bp uninstall"
    case "$ONBOARD" in
        yes|1|true|auto) onboard_local ;;
        *) say "Guided setup with a coordinator agent: bp onboard" ;;
    esac
}

# enable_modules runs bp enable for each module the user asked for with
# --enable or BP_ENABLE (comma separated). Nothing is enabled otherwise.
enable_modules() {
    [ -n "$ENABLE" ] || return 0
    old_ifs=$IFS
    IFS=,
    for module in $ENABLE; do
        IFS=$old_ifs
        [ -n "$module" ] || continue
        "$1" enable "$module" || say "warning: bp enable $module failed; run it again after fixing the reason above"
    done
    IFS=$old_ifs
}

# onboard_local starts the guided coordinator only when asked (--onboard or
# BP_ONBOARD=yes) and only on a fresh installation with a real terminal.
onboard_local() {
    if [ "$local_existing" = yes ]; then
        say "existing installation preserved; run bp onboard explicitly if you want to configure a main agent"
    elif [ ! -f "$BP_HOME/main/onboarding.json" ]; then
        # tmux needs the actual terminal device name, not the /dev/tty alias.
        # stdin is the installer pipe in curl | sh, so `tty` cannot identify it.
        # tmux also writes through fd 0: open read/write, then duplicate it.
        terminal_name=$(ps -o tty= -p "$$" 2>/dev/null | tr -d '[:space:]') || terminal_name=
        terminal_device=
        case "$terminal_name" in
            pts/*|tty*) terminal_device=/dev/$terminal_name ;;
        esac
        if [ -z "${TMUX:-}" ] && [ -n "$terminal_device" ] && [ -c "$terminal_device" ] && [ -r "$terminal_device" ] && [ -w "$terminal_device" ]; then
            if ! "$local_bin/bp" onboard 0<>"$terminal_device" 1>&0 2>&0; then
                say "bp is installed; onboarding did not finish. Run bp onboard from your terminal to continue."
            fi
        else
            say "run bp onboard in a terminal to choose your CLI and start the main agent"
        fi
    fi
}

tmux_is_configured() {
    tmux_file=$HOME/.tmux.conf
    [ -f "$tmux_file" ] || return 1
    grep -Fqx "set -g mouse on" "$tmux_file" &&
        grep -Fqx "set -g history-limit 100000" "$tmux_file" &&
        grep -Fqx "setw -g mode-keys vi" "$tmux_file" &&
        grep -Fqx "set -sg escape-time 10" "$tmux_file" &&
        grep -Fqx "set -s set-clipboard on" "$tmux_file" &&
        grep -Fqx "set -as terminal-features ',xterm*:clipboard'" "$tmux_file" &&
        grep -Fqx "set -g allow-passthrough on" "$tmux_file"
}

append_tmux_line() {
    tmux_line=$1
    if ! grep -Fqx "$tmux_line" "$HOME/.tmux.conf" 2>/dev/null; then
        printf '%s\n' "$tmux_line" >>"$HOME/.tmux.conf"
        record_install line "$HOME/.tmux.conf" "$tmux_line"
    fi
}

# record_install tells bp what the installer changed so bp uninstall can undo
# exactly that. Older binaries without the command are ignored.
record_install() {
    if [ -n "${RECORD_BP:-}" ] && [ -x "$RECORD_BP" ]; then
        "$RECORD_BP" _install-record "$@" >/dev/null 2>&1 || true
    fi
}

configure_tmux() {
    if tmux_is_configured; then
        say "tmux clipboard and scroll settings are already configured"
        return
    fi

    tmux_write=no
    case "$YES" in
        1|yes|true) tmux_write=yes ;;
    esac
    if [ "$tmux_write" != yes ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
        printf 'Add recommended tmux clipboard and scroll settings to ~/.tmux.conf? [y/N] ' >/dev/tty
        IFS= read -r tmux_answer </dev/tty || tmux_answer=
        case "$tmux_answer" in
            y|Y|yes|YES) tmux_write=yes ;;
        esac
    fi
    if [ "$tmux_write" != yes ]; then
        say "skipped tmux setup (rerun with --yes or BP_YES=1 to apply it)"
        return
    fi

    touch "$HOME/.tmux.conf"
    if ! grep -Fqx "# blueprint: clipboard, scroll, and mosh integration" "$HOME/.tmux.conf"; then
        printf '\n# blueprint: clipboard, scroll, and mosh integration\n' >>"$HOME/.tmux.conf"
        record_install line "$HOME/.tmux.conf" "# blueprint: clipboard, scroll, and mosh integration"
    fi
    append_tmux_line "set -g mouse on"
    append_tmux_line "set -g history-limit 100000"
    append_tmux_line "setw -g mode-keys vi"
    append_tmux_line "set -sg escape-time 10"
    append_tmux_line "set -s set-clipboard on"
    append_tmux_line "set -as terminal-features ',xterm*:clipboard'"
    append_tmux_line "set -g allow-passthrough on"
    say "updated $HOME/.tmux.conf (reload with: tmux source-file ~/.tmux.conf)"
}

print_client_notes() {
    install_dir="$HOME/.local/bin"
    case ":${PATH:-}:" in
        *":$install_dir:"*) ;;
        *) say "note: add $install_dir to your PATH" ;;
    esac
    say "try: bp con server-main"
    say "kitty keybinding (add to ~/.config/kitty/kitty.conf):"
    say "map ctrl+shift+i launch --type=background bp img"
}

case "$INSTALL_MODE" in
    server) link_server_binary; ACTIVE_MODE=server ;;
    client) install_client_binary; ACTIVE_MODE=client ;;
    local) install_local; exit 0 ;;
    "") install_local; exit 0 ;;
    *) say "error: BP_INSTALL_MODE must be server, client or local"; exit 1 ;;
esac

configure_tmux
if [ "$ACTIVE_MODE" = client ]; then
    print_client_notes
fi
