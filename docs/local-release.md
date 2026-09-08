# Build and release

```sh
make check
python3 -m unittest scripts.test_local_cli
make release RELEASE_KEY=/private/path/release-ed25519.pem
make release RELEASE_KEY=/private/path/release-ed25519.pem PUBLISH=1
```

The version is `internal/release/version.txt`; npm uses the same version. Commit
source changes before building. The publisher builds a separate checkout of that
commit, excluding untracked files and concurrent edits, for four Linux/macOS targets,
creates a manifest with revision and hashes, signs it and the installer's checksum
list, verifies both signatures, and prepares a complete version directory.

`PUBLISH=1` publishes `site/releases/v<version>/` and switches `latest.version`
last. Published versions are immutable; bump the version instead of replacing
files. If promotion stops after the immutable directory is complete, rerun
`PUBLISH=1` from the same source commit: signatures and hashes are rechecked before
only the legacy URLs and latest pointer are promoted again. Mutable legacy binary
URLs remain for older clients. New installers and
updaters download from the immutable version path.

The Ed25519 private key must be outside Git with mode 0600. The public key is
embedded in the CLI, shell installer and npm package. Keep those copies consistent.
The native updater uses Go verification; the shell installer requires OpenSSL with
Ed25519 support. First installation still trusts the installer obtained by HTTPS;
its public key can be compared with the repository copy through an independent
channel. A checksum fetched from the same host alone is not publisher authentication.

`bp update --check --json` reports a verified available release. Automatic checks
are bounded, cached and never submit prompts to agents. `bp update` retains the
old executable, validates the candidate before replacement, and restores the old
binary if local setup fails. Setup preflight detects unsupported shells and invalid
books/configuration. Configuration, aliases and history are preserved. A partial
setup error can leave newly created setup files; the error is reported rather than
claiming the whole installation was rolled back.

Server rollout is separate: verify the CLI and actual daemon executable, restart
only the bp service when required, and preserve agent processes. Older local
workers can remain old until their session exits; `bp q --retry` uses current code
and normal delivery guards without resending the user's message.

Publish npm only after the matching signed native version is available. Run
`npm test` and then `npm publish` from `npm/`; the prepublish gate verifies the
package layout, source version/key and all four public native artifacts. A package
installation failure exits nonzero. npm-managed installations should update via npm
to keep package metadata consistent. No license change is implied by a release.

Release tests use generated test keys, fake CLIs and isolated tmux servers; no model
requests are necessary. Mac builds are cross-compiled, not a substitute for native
laptop verification.
