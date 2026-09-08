# Build and release

```sh
make check
python3 -m unittest scripts.test_local_cli
make release
```

`make release` builds Linux/macOS binaries for amd64/arm64 under `site/` and writes
SHA-256 checksums. Copy the current installer to `site/install.sh` when publishing.
The local installer verifies the downloaded binary against the published checksum,
backs up an existing binary and preserves configuration and shell aliases.

Release checks should cover CLI behavior, isolated fake-harness tests, all target
builds, and downloaded public artifacts. No model calls are needed for the harness
tests. A matching checksum checks consistency with the download host; it is not an
independent publisher signature. Immutable signed releases and automatic update
notifications are not implemented yet.

Publish source changes to the development branch before recording release artifacts.
Do not commit machine configuration, native transcripts, operational incident logs,
private keys or build caches. Preserve any required operational evidence outside
the public repository.

Server rollout must verify the CLI binary and the actual running daemon executable
separately. Restart only the configured bp service when required. Local delivery
workers from an older launch can remain old until that session exits; a new CLI's
`bp q --retry` retries through normal guards without resending the user's message.
Shared Codex daemons and other agent processes are not bp release dependencies.
