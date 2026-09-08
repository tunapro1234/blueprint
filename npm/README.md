# @tunapro/blueprint

Native Blueprint CLI for Linux/macOS, amd64/arm64. Each npm version downloads
its matching immutable native release, verifies its Ed25519 signature and SHA-256,
and fails installation if verification fails.

```sh
npm install -g @tunapro/blueprint
bp setup
bp onboard
```

The shell installer is the primary installation path; see the repository README.
Run `bp version` and `bp doctor` to inspect the installation. For an npm-managed
binary, update the npm package to keep npm metadata aligned with the executable.
The package does not automatically enable CLI permission bypass or modify global
tmux settings.
