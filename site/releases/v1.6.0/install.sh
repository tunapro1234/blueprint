#!/bin/sh
set -eu

SERVER_ROOT=/srv/blueprint
INSTALL_MODE=${BP_INSTALL_MODE:-}
YES=${BP_YES:-0}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --yes|-y) YES=1 ;;
        --local) INSTALL_MODE=local ;;
        --client) INSTALL_MODE=client ;;
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

    if [ ! -x "$source_binary" ]; then
        if ! command -v make >/dev/null 2>&1; then
            say "error: $source_binary is missing and make is not installed"
            exit 1
        fi
        say "building bp in $SERVER_ROOT"
        make -C "$SERVER_ROOT" build
    fi

    if [ -w /usr/local/bin ]; then
        ln -sfn "$source_binary" "$target_binary"
    elif command -v sudo >/dev/null 2>&1; then
        sudo ln -sfn "$source_binary" "$target_binary"
    else
        say "error: cannot write /usr/local/bin; rerun as root"
        exit 1
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
    say "installed $install_dir/bp (signature and SHA-256 verified)"
}

fetch_verified_release() {
    client_platform
    local_tmp=$(mktemp -d "${TMPDIR:-/tmp}/bp-install.XXXXXX")
    trap 'rm -rf "$local_tmp"' EXIT HUP INT TERM
    if [ -n "${BP_LOCAL_BINARY:-}" ]; then
        cp "$BP_LOCAL_BINARY" "$local_tmp/bp"
    else
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

# Local mode installs no daemon or remote peer and does not edit tmux options.
install_local() {
    client_platform
    if ! command -v tmux >/dev/null 2>&1; then
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
    fetch_verified_release
    "$local_tmp/bp" setup --check || { say "error: installation preflight failed; existing binary preserved"; exit 1; }
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
    say "installed $local_bin/bp; no remote setup required"
    if [ "${BP_ONBOARD:-auto}" = skip ]; then
        say "onboarding skipped; run bp onboard when ready"
    elif [ ! -f "${BP_HOME:-$HOME/.blueprint}/main/onboarding.json" ]; then
        if [ -z "${TMUX:-}" ] && ( : </dev/tty ) 2>/dev/null; then
            "$local_bin/bp" onboard </dev/tty >/dev/tty 2>/dev/tty
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
    "")
        if [ -d "$SERVER_ROOT" ]; then
            link_server_binary
            ACTIVE_MODE=server
        else
            install_local
            exit 0
        fi
        ;;
    *) say "error: BP_INSTALL_MODE must be server, client or local"; exit 1 ;;
esac

configure_tmux
if [ "$ACTIVE_MODE" = client ]; then
    print_client_notes
fi
