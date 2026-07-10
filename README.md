# blueprint (`bp`)

`bp` combines the server's agent/tmux, message queue, usage-policy, and WhatsApp
client operations in one standalone Go binary. `bp daemon` is a supervisor that
schedules the existing Python/Node jobs without rewriting their business logic.

Build and verify:

```sh
make check
```

The binary is written to `/srv/blueprint/bp`.

Run `bp help` for a command summary. `blueprint.service` is at the repository
root; it is not installed or enabled under `/etc` until cutover.
