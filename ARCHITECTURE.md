# Architecture

How whatsnative is put together, and why the seams fall where they do.

For the rules a contributor should follow, see [CLAUDE.md](CLAUDE.md). For the
reasoning behind what the interface does, see [DESIGN.md](DESIGN.md).

## The shape of it

```
                    ┌──────────────────────────────────┐
   WhatsApp ────────▶  client   (whatsmeow ⇄ our types) │
    servers         └────────────────┬─────────────────┘
                                     │  events (chan any, buffered 256)
                                     ▼
                    ┌──────────────────────────────────┐
                    │  ui       (Bubble Tea, one loop)  │
                    └────────────────┬─────────────────┘
                                     │  reads and writes
                                     ▼
                    ┌──────────────────────────────────┐
                    │  db       (SQLite, shared file)   │
                    └──────────────────────────────────┘
```

Four packages, each with one job:

| Package  | Lines | Job |
|----------|-------|-----|
| `client` | ~1600 | Talks to WhatsApp. Translates protobufs into our own types. |
| `db`     | ~1650 | Owns the SQLite schema and every query. |
| `ui`     | ~3800 | Draws the terminal, handles input. |
| `logger` | ~85   | Routes three different libraries' logs into files. |

The dependency arrows only ever point one way: `ui → client → db`, and
`ui → db`. `db` imports neither of the others, and `client` never imports
`ui`. That is what keeps the store testable without a network and the
rendering testable without a database connection to WhatsApp.

## The one-way event stream

`client` never calls into `ui`. whatsmeow delivers events on its own
goroutines, and Bubble Tea's update loop is single-threaded, so the two are
joined by exactly one channel:

```go
// client/session.go
func (s *Session) Events() <-chan any

// ui/window.go — the only goroutine that bridges them
go func() {
    for event := range session.Events() {
        p.Send(event)
    }
}()
```

`Session` translates whatsmeow's protocol events into a small set of its own
(`NewMessage`, `ReceiptChanged`, `ReadStateSynced`, `HistorySynced`,
`Failure`, …). The UI never sees a protobuf. This is why `ui` can be tested by
constructing those structs directly.

The channel is buffered at 256 so a busy render loop does not stall
whatsmeow's goroutines, but the buffer is deliberately finite: when the UI
falls far enough behind, `emit` blocks, and that backpressure is what stops
memory growing without bound during a large sync.

## Rendering: everything is a value

Bubble Tea copies the model on every update, so pages are values, not
pointers:

```go
type PageInterface interface {
    render() string
    action(tea.Msg) (PageInterface, tea.Cmd)
}
```

`action` returns the *interface*, which is what lets one page replace itself
with another — `ConversationsPage` returns a `SettingsPage`, `LoginPage`
returns a `ConversationsPage` once paired.

Anything that must be identical across pages lives on `*app` (terminal size,
the session, the store, the picture toggles). Pages hold a pointer to it
precisely because they are copied and it must not be.

**`render` is pure.** It reads state and returns a string; it never queries
the database or the network. Anything that could block is a `tea.Cmd` that
runs off the loop and comes back as a message. Breaking this rule is how a
TUI freezes.

### Three page implementations

- `LoginPage` — the QR pairing screen.
- `ConversationsPage` — the main screen; ~1700 lines, most of the app.
- `SettingsPage` — toggles, and the pattern to copy for a new screen: it
  holds the page that opened it and hands that exact value back on `esc`, so
  nothing about the conversation underneath has to be rebuilt.

## The store

One SQLite file, shared with whatsmeow, which owns its own `whatsmeow_*`
tables. Ours:

| Table | Holds |
|-------|-------|
| `chats` | One row per conversation: name, kind, pinned, unread, read cursor |
| `messages` | Every message, with media metadata and the original protobuf |
| `contacts` | Address-book names |
| `aliases` | LID ↔ phone-number pairs for the same person |
| `reactions` | One row per person per message |
| `emoji_uses` | Reaction counts, so the picker orders itself by use |
| `settings` | Key/value preferences |

Migrations are additive and re-run safely at every startup: the base schema
uses `CREATE TABLE IF NOT EXISTS`, and new columns are added one at a time
only when `pragma_table_info` says they are missing. There is no version
number and no down-migration. A database from an earlier build picks up new
features rather than being thrown away.

**Names are resolved at read time, not write time.** The same person can
appear as a LID in one chat and a phone number in another, and the address
book arrives long after the messages do. Resolving on write would freeze
whatever was known at that moment; resolving in the query means a contact
saved today fixes every message from last year. This is why `Messages()` and
`Chats()` carry the `COALESCE` ladders they do.

## Where the awkward parts are

Four things in this codebase look over-engineered until you know why.

**Column width is measured twice.** `ui/width.go` picks between two rulers by
asking the terminal what it supports, because Bubble Tea's renderer clips rows
to its own measurement. Getting this wrong makes panel borders drift. The
frames in `ui/style.go` are drawn by hand rather than with lipgloss for the
same reason: lipgloss re-pads to its own measurement and undoes the
reservation.

**Messages arrive wrapped.** A view-once photo is a message containing a
message. `unwrap` in `client/message.go` peels those containers, and it is
called inside `describe` so that history sync, quoted replies and thumbnail
backfill all get it without anyone having to remember.

**Read state has two directions.** `Session.MarkRead` sends a receipt out when
you read something here. `onMarkChatAsRead` applies what comes back when you
read it on your phone. Both are needed; neither substitutes for the other.

**libsignal logs to stdout.** Which is the terminal the UI draws on.
`logger.CaptureSignalLogs` redirects it before the first decryption, and it
must stay before the first decryption — libsignal installs its own logger
lazily on first use.

## Performance notes

Numbers from measuring this app on a real 39,000-message database, not
estimates:

- **SQLite is not the bottleneck.** ~45,000 inserts/second through
  `SaveMessage`, one statement at a time. Batching was measured and was not
  worth the complexity.
- **History sync is the memory peak.** One blob of 20,000 messages holds
  ~48 MB parsed, ~95 MB at peak. It is freed correctly — heap after GC is
  ~3 MB — but Go returns pages to the OS lazily, so `onHistorySync` calls
  `debug.FreeOSMemory` when it finishes. Measured at ~80 MB handed back per
  blob.
- **A soft memory limit made things worse.** `debug.SetMemoryLimit` was tried
  and measured: 8.7× slower for no reduction in peak, because the blob is
  genuinely live and the GC just thrashes against it. Do not re-add it.
- **Pictures are cached per box size.** `ui/media.go` keys rendered images by
  path and drops the whole cache when the box size changes, since a resize
  makes every existing entry unreachable. Capped at 500 entries.

When memory is suspect, do not guess — the app has a profiler:

```bash
WHATSNATIVE_PPROF=localhost:6060 ./whatsnative
go tool pprof -top http://localhost:6060/debug/pprof/heap
```

`inuse_space` shows what is held; `alloc_space` shows what churned through.
For this app those tell very different stories.

## Environment variables

| Variable | Effect |
|----------|--------|
| `WHATSNATIVE_PPROF` | Address to serve pprof on. Absent = no profiler. |
| `WHATSNATIVE_ASCII` | `0` disables ASCII folding; `strict` folds emoji too. |
| `WHATSNATIVE_WIDTH` | `grapheme` or `wc`, overriding terminal detection. |
| `WHATSNATIVE_RESYNC` | Forces a full contact re-sync on next connect. |

Picture toggles are **not** environment variables — they are in the settings
page (`ctrl+g`) and persist in the `settings` table.
