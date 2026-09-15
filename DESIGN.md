# Design

Why the interface behaves the way it does. [ARCHITECTURE.md](ARCHITECTURE.md)
covers how it is built; this covers what it should feel like.

## What this is for

A WhatsApp client that belongs in a terminal. Not a web app in a box — if the
answer to a design question is "the PWA does it this way", that is not an
argument. The point is an app that is fast, keyboard-driven, and small enough
to leave open all day.

Three things it is measured on:

1. **It does not get in the way.** No spinner where a cached answer would do.
2. **It does not surprise you.** The same keystroke does the same thing.
3. **It does not lie.** What is on screen is what is actually true.

## Principles, and what they cost

### Nothing happens that you did not ask for

Scrolling past a conversation is not a request to read it. This sounds
obvious; it was not always true here. Moving the highlight used to open each
chat in turn, which marked them read on the phone and pulled a transcript
down the wire for every row you passed.

So: the **highlight** and the **open conversation** are separate state. Arrow
keys and the wheel move the highlight. `enter` and a click open. The rail
shows both — a bar where the highlight is, a green name on whatever is open —
because once two things can differ, the interface owes you a way to tell them
apart.

The same rule governs read receipts: they are sent when you open a chat or
send into it, never as a side effect of navigation.

### The screen tells the truth

A photo that could not be decoded says `no preview`. A photo you turned off
in settings just says `[photo]` — deliberately *not* `no preview`, because
that would claim a failure that did not happen.

A view-once photo is labelled `[view once]` even though we keep it like any
other. The sender believes it is gone; a reader who cannot tell which is
which is being misled by omission.

Deleted messages leave `[message deleted]` rather than vanishing. A silent
gap would renumber the conversation under whatever you were reading.

### Keyboard first, mouse where it helps

Everything has a key. The mouse is supported where it is genuinely faster —
clicking a chat, clicking a message, the wheel, double-click to reply — and
nowhere else. There are no hit targets you could only find by hunting.

Keys are chosen so the common ones are reachable: `ctrl+f` find, `ctrl+r`
reply, `ctrl+e` react, `ctrl+y` copy, `ctrl+o` open, `ctrl+g` settings. The
help line at the bottom lists them, because a TUI with hidden bindings is a
TUI only its author can use.

### Slow things are opt-out, not opt-in

Pictures render inline by default. That is the better experience, and most
chats are fine.

But a chat full of distinct stickers has to decode every one of them on open
— measured at 50ms for 80 stickers, versus 0.1ms with them off. So photos and
stickers each have a toggle, defaulting to on, that skips the decode
*entirely* rather than making it faster. Off, the message draws as the same
chip already used for a picture that has not downloaded.

The default stays on because changing what an existing user sees needs asking
for, not assuming. The toggle exists because they should not have to live
with it.

### Adapt to use, but not underfoot

The reaction picker offers nine emoji on the number keys, ordered by how often
you have actually used them, backfilled with sensible defaults. An emoji you
reach for through the custom slot (`0`) climbs in like any other.

But it is refreshed only *after* a reaction is sent, never while the picker is
open. A list that reordered between you pressing `ctrl+e` and pressing `3`
would send the wrong emoji. Adaptive interfaces have to hold still at the
moment of use.

## Visual language

Deliberately sparse. Colour carries meaning, never decoration:

| | Means |
|---|---|
| Green (`#25D366`) | The open conversation, unread badges, the active thing |
| Bold, uncoloured | A chat you are not in |
| Muted grey | Timestamps, previews, hints — present but not competing |
| `▌` | Where the highlight is |
| `^` | Pinned |

Two panels, rounded borders, one status line. The status line is the only
place transient messages appear, and it clears itself — no toast stack, no
notification history.

Per-sender colours in groups are derived from a hash of the JID, so the same
person is the same colour every time you open the chat. Stable identity
matters more than an ideal palette.

## Text, and the honest compromise

Non-Latin scripts break the column arithmetic: Bubble Tea and the terminal
disagree about how wide a Malayalam conjunct is, and the disagreement shows up
as a panel border drifting mid-list.

Rather than ship drifting borders, message text is folded to ASCII. This is a
real loss and it is recorded as one — `WHATSNATIVE_ASCII=0` turns it off for
terminals that cope. Emoji are kept, because both rulers agree they are two
cells wide.

The right fix is to measure the way the terminal does. Until then the app
prefers a correct layout with degraded text over correct text in a broken
layout, and says so rather than pretending the problem does not exist.

## Things deliberately not built

- **Calls.** A terminal cannot do them and a stub would be worse than nothing.
- **Notifications.** The OS owns that, and the phone already does it.
- **A config file.** Four environment variables for developer-facing knobs,
  a settings page for user-facing ones. A third mechanism would be one too
  many.
- **Vim keybindings.** Tempting, but the app is not modal, and half-modal is
  worse than neither.

## When adding something

Ask, in order:

1. **Does it make the common case faster, or only the rare case possible?**
   The rare case usually belongs in a key binding, not on screen.
2. **What does it cost when it is not being used?** A feature that decodes,
   fetches, or redraws when you are not looking at it is a feature with a
   permanent bill.
3. **Can the user tell what happened?** If it can fail, it needs to say so.
   If it changed state, that state needs to be visible.
4. **Does it hold still?** Anything that reorders, resizes, or moves under a
   hand that is already moving is worse than something static.
