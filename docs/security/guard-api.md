# guard interface (locked) and the delivery seam

Package `blueprint/internal/guard`, branch `feat/guard`. The signatures below
are locked: changes after this point are additive only.

## Inbound framing

```go
type Source struct {
    Transport, Peer, PeerID, AgentClaim, Room, Channel string
}
type Framed struct { Text, Nonce string; Findings []Finding }

func Frame(src Source, text string) (Framed, error)              // random nonce
func LoadFramer(stateDir string) (*Framer, error)                 // <state>/guard/frame.key, 0600
func NewFramer(key []byte) (*Framer, error)
func (f *Framer) Frame(src Source, text string) (Framed, error)   // nonce = HMAC(key, transport|peerID|channel|attempt)
func (f *Framer) Envelope(from string, src Source, stored string) (Framed, error) // "[from] body" -> "[from] " + frame(body)
func Body(text string) string                                    // canonical body of a frame or plain text
func NeedsFrame(transport string) bool                           // false only for "" and LocalTransports (bp-api/http, bp-api/socket, bp-api/mcp)
const Notice, BodyPrefix string
```

Every body line starts with `| `, so no body line can begin with a bp
envelope, a frame marker or a slash command. The open and close markers carry
the nonce, which never occurs in the body. Provenance values are sanitised to
single tokens.

## Scan, Redact, Policy, Watch

```go
func Scan(text string) []Finding                                 // flags only, never drops
func Redact(text string, p RedactPolicy) (string, []Finding)     // outbound
func MaxSeverity(fs []Finding) Severity
func Summary(fs []Finding) string

type Policy struct {
    Capabilities []Capability // send, lookup, rooms, board; nil = send+lookup
    Expose, Rooms []string
    RatePerHour, Burst, MaxBytes int
    Redact RedactPolicy
}
func (p Policy) CheckSend(target string, size int) Decision
func (p Policy) CheckLookup(name string) Decision                 // deny must look like not-found on the wire
func (p Policy) CheckRoom(c Capability, room string, size int) Decision
func NewLimiter() *Limiter; func (l *Limiter) Allow(peer string, p Policy) Decision

func NewWatch(cfg WatchConfig, sink Sink) *Watch
func (w *Watch) Observe(ev Event)                                 // EvDenied, EvLookup, EvInbound, EvSensitive, EvOutbound
type AuditSink struct{ StateDir string; Events bool; Errors io.Writer } // alerts -> audit.jsonl guard.reach.{probe,enumeration,secret,relay}
func Sensitive(tool, input string) (string, bool)
```

Audit: high findings on inbound text are written as `guard.finding` with
severity `alert`; warn-level flags stay as `guard.flags` fields on the
transport's accepted event.

## Delivery seam (proposal for blueprint to wire)

The stored body stays raw. Framing happens when msgq turns a record into
pane input, keyed off `Origin`:

1. `msgq.Message` gains an unexported, unserialised field `wire string` and
   `func (m Message) Wire() string` (returns `wire`, or `Msg` when empty).
2. `msgq.Queue` gains `Render func(Message) (string, error)`; `cmd/bp` sets it
   right after `msgq.New`, using one `guard.LoadFramer(stateDir)`:

   ```go
   q.Render = func(m msgq.Message) (string, error) {
       if m.Origin == nil || !guard.NeedsFrame(m.Origin.Transport) {
           return m.Msg, nil // local caller: same trust domain as bp msg
       }
       f, err := framer.Envelope(m.From, guard.Source{Transport: m.Origin.Transport,
           Peer: m.Origin.PeerAlias, PeerID: m.Origin.PeerID,
           AgentClaim: m.Origin.AgentClaim, Channel: m.ID}, m.Msg)
       return f.Text, err
   }
   ```

3. `pendingRecords()` (msgq.go:651) is the single place dispatch reads
   records. After `read(path)`, set `message.wire` from `q.Render`. A render
   error makes the record a `badRecord`: held and visible, never pasted raw.
   Because `wire` is not serialised, `writePending`, `finish`, `done/` and
   `messages.jsonl` keep the raw body, and replay dedup in idempotent.go is
   unchanged.
4. Replace `Msg` with `Wire()` at every site that pastes or looks for the
   pasted text: `PendingTexts` (msgq.go:801,804), `CloseDelivered` (873),
   `SubmitStuck`/`Witness`/`ClearDelivered` (1178, 1183, 1211),
   `CanWitness`/`ExactPaste`/`DamagedPaste` (1294, 1305, 1306),
   validate-before-paste and witness (1699, 1710), pane checks (1748, 1750,
   1786, 1836, 1852), `blocked` (1961), `deliver` (2014), post-paste checks
   (2098, 2122, 2124). Keep `Msg` for `RecentIdentical` (local sender dedup),
   `noticeText` (1386, owner-facing head) and display.
5. Tests: a framed record survives restart with an identical `Wire()`; the
   witness closes a delivered framed record exactly once; a render failure
   holds the record; `done/` and `messages.jsonl` hold the raw body.

The render is deterministic per record (key + Peer ID + record ID), so a
restart or a second dispatch pass produces the same pane text and the
existing witness logic needs no other change.

## Who frames what

- Queue records: msgq at delivery through `Queue.Render` (blueprint wires
  it); bp-term only writes the bytes it is given.
- bp-api's own stores (API inbox, room history, board values): bp-api frames
  at read time with `framer.Frame(Source{Transport, Peer, PeerID, AgentClaim,
  Room, Channel: <stable record id>}, body)`; on error it returns an error,
  never the raw body.
- The decision is `guard.NeedsFrame(origin.Transport)`, not
  `PeerAuthenticated`: an authenticated P2P peer is still outside this
  machine.
