## What and why

<!-- What does this change, and what problem does it solve? Link the issue if there is one. -->

## How it was tested

<!-- The commands you ran and what they showed. Delivery changes need a regression test,
ideally in scripts/test_local_cli.py with a private tmux server. -->

- [ ] `make check` (go test, go vet, govulncheck and the build)
- [ ] `python3 -m unittest scripts.test_local_cli` (needed for changes to delivery, tmux or the CLI)
- [ ] Tests ran in temporary roots only (`HOME`, `BP_HOME`, a private tmux socket), never against a real install

## Checklist

- [ ] Targets the `dev` branch
- [ ] A change to the user's environment is behind a module, and `bp disable` / `bp uninstall` undo it
- [ ] Inbound text, delivery, identity or API changes are checked against docs/security/threat-model.md
- [ ] Docs updated (README, docs/usage.md, docs/configuration.md or docs/api.md) and a CHANGELOG.md "Unreleased" entry for anything users notice
- [ ] No secrets, tokens, transcripts or private conversation content in code, tests or fixtures
- [ ] Code, comments and docs are in English
