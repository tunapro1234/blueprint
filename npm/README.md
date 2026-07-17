# @tunapro/blueprint

This package installs the native `bp` agent-infrastructure CLI for Linux or macOS on x64 or arm64. The postinstall script downloads the matching binary from [bp.tunapro.xyz](https://bp.tunapro.xyz), verifies it against the published SHA-256 checksum, and exposes it as `bp`.

```sh
npm install -g @tunapro/blueprint
```

For remote connections and image paste, create `~/.config/bp/config`:

```ini
REMOTE=user@host
REMOTE_METHOD=mosh
```

Copy an image locally, then run `bp img`. The command uploads the image over SSH, prints its server path, and copies that path back to the local text clipboard. Wayland needs `wl-clipboard`, X11 needs `xclip`, and macOS needs `pngpaste` for image reads.

Optional kitty binding:

```conf
map ctrl+shift+i launch --type=background bp img
```

Recommended tmux clipboard, scroll, and OSC52 settings:

```tmux
set -g mouse on
set -g history-limit 100000
setw -g mode-keys vi
set -sg escape-time 10
set -s set-clipboard on
set -as terminal-features ',xterm*:clipboard'
set -g allow-passthrough on
```

This directory is ready for packaging, but it must not be published automatically. When ready, authenticate with the intended npm account, review `npm pack --dry-run`, and run `npm publish` from this directory manually.
