package ui

// layout is where everything sits on screen.
//
// render and the mouse handler both derive it from the same inputs, which is
// what lets a click be turned back into the chat or message drawn at that row.
//
// Screen shape: two bordered panels filling every row but the last, which is
// the status and help line.
//
//	row 0            ╭ rail ─────╮╭ conversation ────────╮
//	rows 1..          │ title    ││ chat name            │
//	                  │ ──────── ││ ──────────────────── │
//	                  │ chats    ││ transcript           │
//	                  │          ││ ──────────────────── │
//	                  │          ││ › input              │
//	row height-2      ╰──────────╯╰──────────────────────╯
//	row height-1     help / status
type layout struct {
	width  int
	height int

	// railTotal includes the rail's own border columns.
	railTotal int
	railInner int
	convInner int

	contentRows int // rows inside a panel, between its borders

	tabRow   int // screen row of the tab strip
	chatTop  int // screen row of the first chat entry
	chatRows int // rows available to chat entries

	transcriptTop  int // screen row of the first transcript line
	transcriptRows int

	replying  bool
	statusRow int
}

// chatEntryRows is how many rows one chat takes: name, preview, a blank line
// for breathing room, then the separator.
const chatEntryRows = 4

func computeLayout(width, height int, replying bool) layout {
	width = max(width, 50)
	height = max(height, 12)

	l := layout{
		width:     width,
		height:    height,
		railTotal: min(max(width/3, 24), 38),
		replying:  replying,
		statusRow: height - 1,
	}

	l.railInner = l.railTotal - 2
	l.convInner = max(width-l.railTotal-2, 20)

	// One row goes to the status line, two to the panel's top and bottom
	// borders.
	l.contentRows = max(height-3, 4)

	// Row 0 is the panel's top border, so the tab strip is row 1 and the rule
	// row 2, which puts the first chat on row 3.
	l.tabRow = 1
	l.chatTop = 3
	l.chatRows = max(l.contentRows-2, 1)

	// The conversation spends two rows on the chat name and a rule, then the
	// transcript, then a rule, an optional reply bar, and the input.
	replyRows := 0
	if replying {
		replyRows = 1
	}
	l.transcriptTop = 3
	l.transcriptRows = max(l.contentRows-4-replyRows, 1)

	return l
}

// visibleChats is how many chat entries fit in the rail.
func (l layout) visibleChats() int {
	// The last entry needs no trailing separator, hence the +1.
	return max((l.chatRows+1)/chatEntryRows, 1)
}

// chatAtRow maps a screen row inside the rail to a chat offset, or reports
// false when the row is outside the list.
func (l layout) chatAtRow(screenY, firstChat int) (int, bool) {
	if screenY < l.chatTop || screenY >= l.chatTop+l.chatRows {
		return 0, false
	}
	return firstChat + (screenY-l.chatTop)/chatEntryRows, true
}

// inRail reports whether a column falls inside the rail panel.
func (l layout) inRail(screenX int) bool {
	return screenX < l.railTotal
}
