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
    client_platform
    install_dir="$HOME/.local/bin"
    target_binary="$install_dir/bp"
    download_url="https://bp.tunapro.xyz/bp-$PLATFORM"
    temp_binary=$(mktemp "${TMPDIR:-/tmp}/bp.XXXXXX")
    trap 'rm -f "$temp_binary"' EXIT HUP INT TERM

    if ! command -v curl >/dev/null 2>&1; then
        say "error: curl is required"
        exit 1
    fi
    say "downloading $download_url"
    curl -fsSL "$download_url" -o "$temp_binary"
    chmod 755 "$temp_binary"
    mkdir -p "$install_dir"
    mv "$temp_binary" "$target_binary"
    trap - EXIT HUP INT TERM

    configure_remote
    say "installed $target_binary"
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
    local_tmp=$(mktemp -d "${TMPDIR:-/tmp}/bp-install.XXXXXX")
    trap 'rm -rf "$local_tmp"' EXIT HUP INT TERM
    if [ -n "${BP_LOCAL_BINARY:-}" ]; then
        cp "$BP_LOCAL_BINARY" "$local_tmp/bp"
    else
        command -v curl >/dev/null 2>&1 || { say "error: curl is required"; exit 1; }
        local_base=https://bp.tunapro.xyz
        curl --proto '=https' --proto-redir '=https' -fsSL "$local_base/bp-$PLATFORM" -o "$local_tmp/bp"
        curl --proto '=https' --proto-redir '=https' -fsSL "$local_base/checksums.txt" -o "$local_tmp/checksums"
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
    local_bin="$HOME/.local/bin"
    mkdir -p "$local_bin"
    if [ -e "$local_bin/bp" ]; then
        local_backup=$(mktemp "$local_bin/bp.before-local.XXXXXX")
        cp -p "$local_bin/bp" "$local_backup"
    fi
    local_candidate=$(mktemp "$local_bin/.bp-install.XXXXXX")
    cp "$local_tmp/bp" "$local_candidate"
    chmod 755 "$local_candidate"
    mv "$local_candidate" "$local_bin/bp"
    "$local_bin/bp" setup
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
