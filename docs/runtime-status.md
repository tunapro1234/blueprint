# Runtime status

`bp status --json` exposes the observation used by status, the bar and delivery
checks. Calls made at different times can legitimately report different states.

| Field | Meaning |
| --- | --- |
| `schema_version` | Current schema: `2` |
| `producer` | CLI executable SHA-256, PID and build information |
| `daemon`, `daemon_verification` | Daemon record and verification against its running executable |
| `agents[].name`, `display_name` | Stable registry identity and optional native display name |
| `activity.state` | `idle`, `working`, `unknown`, `blocked`, `dead` |
| `activity.source`, `reason` | Observation source and missing/conflicting evidence |
| `activity.observed_at`, `last_event_at` | Observation time versus native event time |
| `activity.thread_id`, `binding`, `transcript_path` | Exact conversation binding |
| `activity.screen_busy`, `turn_busy` | Separate evidence; absence is not false |
| `activity.delivery_blocked` | Safety decision, including user draft and unknown runtime |
| `usage_observed_at`, `usage_scope` | Context snapshot time/scope, not spending in a time window |

`busy=true` can mean delivery is unsafe; it does not necessarily mean active work.
Monitoring must use the structured activity state and freshness. An old cumulative
usage total or transcript mtime is not evidence of current work or recent spending.

An empty composer does not prove idle. Conflicting positive working evidence and
an idle record produce uncertainty. A disconnected remote client's frozen screen
cannot override a failed app-server observation.

Compact boundaries may invalidate earlier context observations. Metadata and
replayed pre-compact assistant records must not count as new work or usage. Unknown
context/TTL stays unknown; cache warmth is an estimate, not a guarantee of reuse.

The bar selects the actual runtime's provider quota (`gpt` / `cc`) and shows usage,
not remaining allowance. Context, provider quota and period spending are different
measurements. See [configuration](configuration.md) for display settings.
