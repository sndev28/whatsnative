# Working in this repository

Read [ARCHITECTURE.md](ARCHITECTURE.md) first for how the packages fit
together. This file is the rules; that one is the map.

## Build and verify

```bash
go build ./... && go vet ./... && go test ./...
```

All three, every time, before calling anything done. `go build` passing is not
evidence a change works — several bugs in this codebase's history compiled
cleanly and were wrong.

To rebuild the binary the desktop entry launches:

```bash
go build -o whatsnative .
```

## Non-negotiables

**`render` must stay pure.** It takes state and returns a string. No database
queries, no network, no file reads. Anything that could block is a `tea.Cmd`.
This is the single easiest way to freeze the UI.

**Never trust a fix you have not measured.** This codebase has a history of
plausible fixes that were wrong:

- A soft memory limit to curb history-sync memory: 8.7× slower, no reduction
  in peak.
- A fontconfig `emoji` alias to fix emoji rendering: inert, because the
  terminal asks for a codepoint, not that family.
- "Scroll just enough to keep the cursor visible", derived per render: had
  exactly one answer for any cursor past the first page, which is why
  clicking a chat threw the list to the bottom.

Measure first, then fix, then measure again. Temporary benchmark files are
fine; delete them before you finish.

**Verify against real data where it is cheap.** There is a real database at
`my_database.db`. Copy it to a scratch directory and run against the copy —
never the original. A migration that passes on an empty fixture and fails on
39,000 real messages has taught you nothing.

**Test the behaviour, not the implementation.** Every test in this repo says
in its comment *what would break for the user* if it failed. A test named
`TestFooReturnsTrue` is not worth writing.

## Things that will bite you

**Column width.** Do not reach for lipgloss's layout helpers (`JoinHorizontal`,
`Border`) on anything inside a panel. They re-pad to their own width
measurement and undo the reservations in `ui/style.go`. The frames are drawn
by hand on purpose. If borders start drifting, that is why.

**Protobuf zero values.** `ProtocolMessage_REVOKE` is `0`, and `GetType()` on
a nil pointer returns `0`. A missing nil check here once made every incoming
message look like a deletion and stopped message delivery entirely. Check the
pointer before you check the enum.

**Messages arrive wrapped.** View-once, disappearing and
document-with-caption messages are a message inside a message. Call `unwrap`
— or rather, call `describe`, which does it for you. Nothing at the top level
is set on these, so unwrapped they look empty and get silently dropped.

**Struct fields default to the zero value.** When `showPhotos`/`showStickers`
moved onto `app`, 28 test call sites that built `app{}` directly would have
silently started rendering with everything off. Go will not warn you. After
adding a field to a shared struct, grep for every literal that constructs it.

**libsignal logs to stdout.** Which is the terminal the UI draws on.
`logger.CaptureSignalLogs` must run before the first decryption. Do not move
it later in startup.

## Conventions

**Comments explain why, not what.** The code says what it does. A comment
earns its place by recording the reason a thing is strange — the constraint,
the bug it prevents, the obvious approach that did not work. If it restates
the line below it, delete it.

**Match the surrounding style.** This codebase has a consistent voice:
lowercase unexported helpers, `--` for parenthetical asides in comments, and
error messages that read as sentences. Follow what is there.

**Prefer the boring fix.** Two caches, one mutex-guarded store, one event
channel. Reach for a new abstraction only when the simple thing has been
tried and measured.

## Error handling

Errors that the user can act on go to the status line via `c.fail`. Errors
that are merely unfortunate — a thumbnail that would not decode, a read
receipt that did not send — are logged and swallowed, because a banner over
someone's conversation is worse than a missing preview.

`Session` emits `Failure{Err}` for anything worth surfacing. Do not panic
outside `main`.

## Privacy

This app holds someone's entire message history and their contacts' names and
identifiers.

- **Never put real data in tests.** Real LIDs were once committed to this
  repository as test fixtures and pushed publicly. Use obviously-invented
  values: `10000000000001@lid`, names from fiction.
- **Prefer aggregate queries when investigating.** Row counts, whether a join
  resolved, digit lengths — these answer most questions without reading
  anyone's messages.
- **Copy the database before experimenting.** Never run a migration against
  `my_database.db` itself.

## Release

CI builds and tests every push. A release happens only when a `v*` tag is
pushed:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

`go-sqlite3` is cgo, so the release artifact is a native linux/amd64 build
against the runner's glibc. Adding macOS or Windows means adding runners, not
setting `GOOS`.
