#!/bin/sh
set -eu

SERVER_ROOT=/srv/blueprint
INSTALL_MODE=${BP_INSTALL_MODE:-}

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
    download_url="https://bp.trasumanar.ai/bp-$PLATFORM"
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
    case ":${PATH:-}:" in
        *":$install_dir:"*) ;;
        *) say "note: add $install_dir to your PATH" ;;
    esac
    say "try: bp con server-main"
}

case "$INSTALL_MODE" in
    server) link_server_binary ;;
    client) install_client_binary ;;
    "")
        if [ -d "$SERVER_ROOT" ]; then
            link_server_binary
        else
            install_client_binary
        fi
        ;;
    *) say "error: BP_INSTALL_MODE must be server or client"; exit 1 ;;
esac
