package ui

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	_ "github.com/mattn/go-sqlite3"

	"whatsnative/db"
)

const (
	daruJID      = "1@s.whatsapp.net"
	christinaJID = "2@s.whatsapp.net"
)

// fixtureStore builds a real store on a throwaway database, so these tests
// cover the SQL as well as the rendering.
func fixtureStore(t *testing.T) *db.MessageStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	store, err := db.NewMessageStore(conn)
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}

	sent := time.Date(2026, time.August, 12, 14, 30, 0, 0, time.UTC)
	messages := []db.Message{
		{ID: "a", ChatJID: daruJID, SenderJID: daruJID, Sender: "You", Content: "El Psy Kongroo", Timestamp: sent, FromMe: true},
		{ID: "b", ChatJID: daruJID, SenderJID: daruJID, Sender: "Daru", Content: "lol ok Okarin, did you finish the IBN 5100 emulator or nah", Timestamp: sent.Add(time.Minute)},
		{ID: "c", ChatJID: christinaJID, SenderJID: christinaJID, Sender: "Christina", Content: "I will end you.", Timestamp: sent.Add(-time.Hour)},
		{
			ID: "d", ChatJID: daruJID, SenderJID: daruJID, Sender: "Daru",
			Timestamp: sent.Add(2 * time.Minute),
			Media:     db.Media{Kind: db.MediaDocument, Name: "divergence.pdf", Size: 2048, Mime: "application/pdf"},
		},
	}
	for _, message := range messages {
		if err := store.SaveMessage(message); err != nil {
			t.Fatalf("save fixture message: %v", err)
		}
	}

	for jid, name := range map[string]string{daruJID: "Daru", christinaJID: "Christina"} {
		if err := store.SaveContact(jid, name); err != nil {
			t.Fatalf("save fixture contact: %v", err)
		}
	}
	return store
}

func fixturePage(t *testing.T, width, height int) ConversationsPage {
	t.Helper()

	store := fixtureStore(t)
	page := openConversationsPage(&app{messages: store, width: width, height: height})

	chats, err := store.Chats()
	if err != nil {
		t.Fatalf("load fixture chats: %v", err)
	}
	page.chats = chats

	messages, err := store.Messages(chats[0].JID, historyLimit)
	if err != nil {
		t.Fatalf("load fixture messages: %v", err)
	}
	page.messages = messages
	page.status = ""
	return page
}

// --- store ---------------------------------------------------------------

func TestChatsAreOrderedByActivity(t *testing.T) {
	chats, err := fixtureStore(t).Chats()
	if err != nil {
		t.Fatalf("load chats: %v", err)
	}

	if len(chats) != 2 {
		t.Fatalf("got %d chats, want 2", len(chats))
	}
	if chats[0].Name != "Daru" {
		t.Errorf("first chat is %q, want Daru (most recent)", chats[0].Name)
	}
}

// A message with no text still needs a preview, or the chat list row is blank.
func TestMediaMessagePreview(t *testing.T) {
	chats, err := fixtureStore(t).Chats()
	if err != nil {
		t.Fatalf("load chats: %v", err)
	}
	if got := chats[0].LastMessage; got != "[document] divergence.pdf" {
		t.Errorf("preview is %q, want the document summary", got)
	}
}

func TestSaveMessageIsIdempotent(t *testing.T) {
	store := fixtureStore(t)

	duplicate := db.Message{
		ID: "a", ChatJID: daruJID, Sender: "You", Content: "El Psy Kongroo",
		Timestamp: time.Date(2026, time.August, 12, 14, 30, 0, 0, time.UTC), FromMe: true,
	}
	if err := store.SaveMessage(duplicate); err != nil {
		t.Fatalf("re-save message: %v", err)
	}

	messages, err := store.Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 3 {
		t.Errorf("got %d messages, want 3 -- the duplicate was stored", len(messages))
	}
}

func TestMessagesAreChronological(t *testing.T) {
	messages, err := fixtureStore(t).Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i].Timestamp.Before(messages[i-1].Timestamp) {
			t.Fatal("messages are not oldest first")
		}
	}
}

// Reactions are stored per person, so reacting twice replaces rather than
// stacks, and an empty emoji takes the reaction back.
func TestReactionsReplaceAndRemove(t *testing.T) {
	store := fixtureStore(t)

	react := func(emoji string) {
		t.Helper()
		err := store.SaveReaction(daruJID, db.Reaction{
			MessageID: "a", SenderJID: christinaJID, Sender: "Christina",
			Emoji: emoji, Timestamp: time.Now(),
		})
		if err != nil {
			t.Fatalf("save reaction: %v", err)
		}
	}

	react("👍")
	react("❤️")

	messages, err := store.Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if got := len(messages[0].Reactions); got != 1 {
		t.Fatalf("got %d reactions, want 1 -- reacting twice stacked", got)
	}
	if got := messages[0].Reactions[0].Emoji; got != "❤️" {
		t.Errorf("reaction is %q, want the newer one", got)
	}

	react("")
	messages, err = store.Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatalf("reload messages: %v", err)
	}
	if got := len(messages[0].Reactions); got != 0 {
		t.Errorf("got %d reactions after removal, want 0", got)
	}
}

// --- layout --------------------------------------------------------------

// The renderer has to fill the screen exactly: too few lines and the input row
// floats, too many and Bubble Tea scrolls the alt screen.
func TestRenderFillsExactlyTheTerminalHeight(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{80, 24}, {120, 40}, {60, 12}, {100, 30},
	} {
		page := fixturePage(t, size.width, size.height)
		lines := strings.Split(page.render(), "\n")

		if len(lines) != size.height {
			t.Errorf("%dx%d: got %d lines, want %d", size.width, size.height, len(lines), size.height)
		}
	}
}

// The reply bar takes a row from the panes rather than pushing the input off
// the bottom of the screen.
func TestReplyBarKeepsTheHeight(t *testing.T) {
	c := fixturePage(t, 80, 24)
	c.replyTo = c.messages[0]
	page := c

	lines := strings.Split(page.render(), "\n")
	if len(lines) != 24 {
		t.Fatalf("got %d lines with a reply bar, want 24", len(lines))
	}
	if !strings.Contains(page.render(), c.replyTo.Sender) {
		t.Error("reply bar does not name who is being replied to")
	}
}

func TestRenderNeverExceedsTerminalWidth(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		page := fixturePage(t, width, 24)

		for i, line := range strings.Split(page.render(), "\n") {
			if got := displayWidth(line); got > width {
				t.Errorf("width %d: line %d is %d cells: %q", width, i, got, line)
			}
		}
	}
}

func TestLongMessageWraps(t *testing.T) {
	page := fixturePage(t, 80, 24)
	lines := page.transcript(page.app.width / 2)

	if len(lines) < 4 {
		t.Fatalf("expected the long message to wrap, got %d lines", len(lines))
	}
}

// --- mouse ---------------------------------------------------------------

// Clicking the second chat in the rail opens it. Chats are two rows tall.
func TestClickOnRailOpensChat(t *testing.T) {
	page := fixturePage(t, 80, 24)
	l := computeLayout(80, 24, false)

	next, cmd := page.handleClick(tea.Mouse{X: 1, Y: l.chatTop + chatEntryRows, Button: tea.MouseLeft})
	opened := next.(ConversationsPage)

	if opened.cursor != 1 {
		t.Errorf("cursor = %d, want 1", opened.cursor)
	}
	if cmd == nil {
		t.Fatal("expected a command loading the clicked chat")
	}
	loaded, ok := findMessagesLoaded(runCmd(cmd))
	if !ok {
		t.Fatal("no messagesLoadedMsg among the commands")
	}
	if loaded.chatJID != christinaJID {
		t.Errorf("loading %q, want %q", loaded.chatJID, christinaJID)
	}
}

// Clicking a message in the transcript picks it out for reply and reactions.
func TestClickOnTranscriptSelectsMessage(t *testing.T) {
	page := fixturePage(t, 80, 24)
	l := computeLayout(80, 24, false)

	// The transcript is bottom aligned, so its last row is the newest message.
	lastRow := l.transcriptTop + l.transcriptRows - 1
	next, _ := page.handleClick(tea.Mouse{X: l.railTotal + 3, Y: lastRow, Button: tea.MouseLeft})
	clicked := next.(ConversationsPage)

	if clicked.selected != len(page.messages)-1 {
		t.Errorf("selected = %d, want %d", clicked.selected, len(page.messages)-1)
	}
	if clicked.focus != focusMessages {
		t.Error("clicking the transcript should move focus to it")
	}
}

// A click below the panes, on the input row, must not select anything.
func TestClickOutsidePanesIsIgnored(t *testing.T) {
	page := fixturePage(t, 80, 24)
	l := computeLayout(80, 24, false)

	next, _ := page.handleClick(tea.Mouse{X: 5, Y: l.statusRow, Button: tea.MouseLeft})
	if next.(ConversationsPage).selected != -1 {
		t.Error("a click on the input row selected a message")
	}
}

func TestWheelScrollsTranscript(t *testing.T) {
	page := fixturePage(t, 80, 24)
	l := computeLayout(80, 24, false)

	next, _ := page.handleWheel(tea.Mouse{X: l.railTotal + 3, Y: l.transcriptTop, Button: tea.MouseWheelUp})
	if next.(ConversationsPage).scroll == 0 {
		t.Error("wheel up over the transcript did not scroll it")
	}
}

// --- pieces --------------------------------------------------------------

func TestReactionSummaryCountsAndKeepsOrder(t *testing.T) {
	summary := reactionSummary([]db.Reaction{
		{Emoji: "👍"}, {Emoji: "❤️"}, {Emoji: "👍"},
	})

	if !strings.Contains(summary, "👍 2") {
		t.Errorf("summary %q should count the repeated reaction", summary)
	}
	if !strings.Contains(summary, "❤️ 1") {
		t.Errorf("summary %q should show a count even for a single reaction", summary)
	}
	if strings.Index(summary, "👍") > strings.Index(summary, "❤️") {
		t.Errorf("summary %q should keep first-seen order", summary)
	}
}

// runCmd flattens a command, following batches, so a test can look for the one
// message it cares about.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}

	var out []tea.Msg
	for _, inner := range batch {
		out = append(out, runCmd(inner)...)
	}
	return out
}

func findMessagesLoaded(msgs []tea.Msg) (messagesLoadedMsg, bool) {
	for _, msg := range msgs {
		if loaded, ok := msg.(messagesLoadedMsg); ok {
			return loaded, true
		}
	}
	return messagesLoadedMsg{}, false
}

func TestParseSendCommand(t *testing.T) {
	for _, tc := range []struct {
		input   string
		path    string
		caption string
		ok      bool
	}{
		{"hello there", "", "", false},
		{"/send /tmp/a.png", "/tmp/a.png", "", true},
		{"/img /tmp/a.png look at this", "/tmp/a.png", "look at this", true},
		{"/sticker", "", "", true},
		{"/unknown /tmp/a.png", "", "", false},
	} {
		path, caption, ok := parseSendCommand(tc.input)
		if ok != tc.ok || path != tc.path || caption != tc.caption {
			t.Errorf("parseSendCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.input, path, caption, ok, tc.path, tc.caption, tc.ok)
		}
	}
}

// Cutting a coloured string must not measure the escapes as visible width,
// nor leave the colour switched on for the rest of the line.
func TestTruncateHandlesColour(t *testing.T) {
	styled := accentStyle.Render("hello world, this is far too long")

	cut := truncate(styled, 10)
	if got := displayWidth(cut); got > 10 {
		t.Errorf("cut string is %d cells wide, want <= 10", got)
	}
	// lipgloss closes with the empty SGR form, which resets just as "0m" does.
	if !strings.HasSuffix(cut, reset) && !strings.HasSuffix(cut, "\x1b[m") {
		t.Errorf("cut string %q should close the colour it opened", cut)
	}
}

// Half-block rendering packs two pixels per cell, so the aspect ratio has to
// account for cells being about twice as tall as they are wide.
func TestFitCellsPreservesAspect(t *testing.T) {
	columns, rows := fitCells(100, 100, 40, 40)
	if columns != 40 || rows != 20 {
		t.Errorf("square image fitted to %dx%d cells, want 40x20", columns, rows)
	}

	columns, rows = fitCells(100, 400, 40, 40)
	if rows > 40 || columns > 40 {
		t.Errorf("tall image overflowed its box at %dx%d", columns, rows)
	}
}

func TestRenderWithNoChats(t *testing.T) {
	page := openConversationsPage(&app{width: 80, height: 24})

	lines := strings.Split(page.render(), "\n")
	if len(lines) != 24 {
		t.Errorf("got %d lines, want 24", len(lines))
	}
}

// Every rendered row must be exactly the terminal width under the ruler in
// force. Bubble Tea clips rows to its own measurement, so a row measured any
// other way is either cut short or padded, and the border drifts either way.
func TestPanesHoldTheirWidthInEveryScript(t *testing.T) {
	names := []string{
		"العربية والراء",       // Arabic, right-to-left
		"हिन्दी संदेश",         // Devanagari, combining marks
		"日本語のメッセージ",            // CJK, double width
		"Пример сообщения",     // Cyrillic
		"👨‍👩‍👧‍👦 family emoji", // ZWJ sequence, one grapheme
		"한국어 메시지",              // Hangul
	}

	store := fixtureStore(t)
	base := time.Date(2026, time.August, 12, 16, 0, 0, 0, time.UTC)
	for i, name := range names {
		jid := fmt.Sprintf("%d@s.whatsapp.net", 100+i)
		if err := store.SaveContact(jid, name); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMessage(db.Message{
			ID: fmt.Sprintf("m%d", i), ChatJID: jid, SenderJID: jid,
			Sender: name, Content: name + " " + name, Timestamp: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	chats, err := store.Chats()
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{60, 80, 100} {
		page := openConversationsPage(&app{messages: store, width: width, height: 24})
		page.chats = chats
		page.status = ""

		messages, err := store.Messages(chats[0].JID, historyLimit)
		if err != nil {
			t.Fatal(err)
		}
		page.messages = messages

		for i, line := range strings.Split(page.render(), "\n") {
			// Exactly full under the ruler in force, so Bubble Tea neither
			// clips the row nor leaves a gap before the border.
			if got := measure(line); got != width {
				t.Errorf("width %d: row %d measures %d, want exactly %d", width, i, got, width)
			}
		}
	}
}
