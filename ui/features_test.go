package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"whatsnative/db"
)

var stripANSIForTest = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	groupJID      = "800@g.us"
	statusJID     = "status@broadcast"
	newsletterJID = "900@newsletter"
)

// streamsStore has one chat of every kind, so the tabs have something to
// separate.
func streamsStore(t *testing.T) *db.MessageStore {
	t.Helper()

	store := fixtureStore(t)
	base := time.Date(2026, time.August, 12, 16, 0, 0, 0, time.UTC)

	seed := []db.Message{
		{ID: "s1", ChatJID: statusJID, SenderJID: daruJID, Content: "on the roof", Timestamp: base},
		{ID: "n1", ChatJID: newsletterJID, SenderJID: newsletterJID, Content: "issue 12", Timestamp: base},
		{ID: "g1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", Content: "first", Timestamp: base, IsGroup: true},
	}
	for _, message := range seed {
		if err := store.SaveMessage(message); err != nil {
			t.Fatalf("seed %s: %v", message.ID, err)
		}
	}

	if err := store.SaveChatName(newsletterJID, "Lab Notes", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatMeta(newsletterJID, db.KindNewsletter, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveChatName(statusJID, "Status Updates", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatMeta(statusJID, db.KindStatus, false, false, false); err != nil {
		t.Fatal(err)
	}
	return store
}

// Status and newsletters must not turn up among ordinary conversations.
func TestTabsSeparateTheStreams(t *testing.T) {
	store := streamsStore(t)

	for _, tc := range []struct {
		tab  tabKind
		want []string
	}{
		{tabChats, []string{daruJID, christinaJID, groupJID}},
		{tabStatus, []string{statusJID}},
		{tabChannels, []string{newsletterJID}},
	} {
		chats, err := store.Chats(tc.tab.kinds()...)
		if err != nil {
			t.Fatalf("load chats: %v", err)
		}

		got := map[string]bool{}
		for _, chat := range chats {
			got[chat.JID] = true
		}
		if len(got) != len(tc.want) {
			t.Errorf("tab %d returned %d chats, want %d", tc.tab, len(got), len(tc.want))
		}
		for _, jid := range tc.want {
			if !got[jid] {
				t.Errorf("tab %d is missing %s", tc.tab, jid)
			}
		}
	}
}

// Newsletters and status get their names from their own metadata, not a
// contact lookup that was never going to match.
func TestNewsletterAndStatusAreNamed(t *testing.T) {
	store := streamsStore(t)

	for _, tc := range []struct {
		tab  tabKind
		want string
	}{
		{tabChannels, "Lab Notes"},
		{tabStatus, "Status Updates"},
	} {
		chats, err := store.Chats(tc.tab.kinds()...)
		if err != nil {
			t.Fatal(err)
		}
		if len(chats) == 0 {
			t.Fatalf("tab %d has no chats", tc.tab)
		}
		if chats[0].Name != tc.want {
			t.Errorf("name is %q, want %q", chats[0].Name, tc.want)
		}
	}
}

// A pinned chat sits above more recent unpinned ones.
func TestPinnedChatsComeFirst(t *testing.T) {
	store := streamsStore(t)

	if err := store.SetChatMeta(christinaJID, db.KindChat, true, false, false); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	if chats[0].JID != christinaJID {
		t.Errorf("first chat is %s, want the pinned one", chats[0].JID)
	}
	if !chats[0].Pinned {
		t.Error("pinned flag did not survive the round trip")
	}
}

func TestUnreadCountsAndClearing(t *testing.T) {
	store := streamsStore(t)

	for range 3 {
		if err := store.BumpUnread(groupJID); err != nil {
			t.Fatal(err)
		}
	}

	find := func() db.Chat {
		t.Helper()
		chats, err := store.Chats(tabChats.kinds()...)
		if err != nil {
			t.Fatal(err)
		}
		for _, chat := range chats {
			if chat.JID == groupJID {
				return chat
			}
		}
		t.Fatal("group chat missing")
		return db.Chat{}
	}

	if got := find().Unread; got != 3 {
		t.Errorf("unread = %d, want 3", got)
	}
	if err := store.MarkRead(groupJID); err != nil {
		t.Fatal(err)
	}
	if got := find().Unread; got != 0 {
		t.Errorf("unread after opening = %d, want 0", got)
	}
}

// Clearing the local badge is not the same as telling WhatsApp: the messages
// still owing a receipt are tracked separately, and only what we sent stops
// coming back.
func TestUnackedIncomingTracksWhatStillOwesAReceipt(t *testing.T) {
	store := fixtureStore(t)

	pending, err := store.UnackedIncoming(daruJID, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Newest first, and never our own messages: "a" is from us.
	var ids []string
	for _, message := range pending {
		ids = append(ids, message.ID)
	}
	if got, want := strings.Join(ids, ","), "d,b"; got != want {
		t.Fatalf("pending = %q, want %q", got, want)
	}

	// Clearing the badge alone must not retire them; only the receipt does.
	if err := store.MarkRead(daruJID); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.UnackedIncoming(daruJID, 100); err != nil {
		t.Fatal(err)
	} else if len(pending) != 2 {
		t.Errorf("clearing the badge retired %d messages, want 0", 2-len(pending))
	}

	if err := store.SetReadThrough(daruJID, pending[0].Timestamp); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.UnackedIncoming(daruJID, 100); err != nil {
		t.Fatal(err)
	} else if len(pending) != 0 {
		t.Errorf("%d messages still pending after the receipt, want 0", len(pending))
	}

	// A message that arrives afterwards owes a receipt of its own.
	later := db.Message{
		ID: "e", ChatJID: daruJID, SenderJID: daruJID, Sender: "Daru",
		Content: "you there?", Timestamp: pending[0].Timestamp.Add(time.Hour),
	}
	if err := store.SaveMessage(later); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.UnackedIncoming(daruJID, 100); err != nil {
		t.Fatal(err)
	} else if len(pending) != 1 || pending[0].ID != "e" {
		t.Errorf("pending after a new message = %v, want just e", pending)
	}
}

// History sync backfills older messages, so the mark must never slide back and
// re-acknowledge a conversation that has already been read.
func TestReadThroughOnlyMovesForward(t *testing.T) {
	store := fixtureStore(t)

	newest := time.Date(2026, time.August, 12, 14, 32, 0, 0, time.UTC)
	if err := store.SetReadThrough(daruJID, newest); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReadThrough(daruJID, newest.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	pending, err := store.UnackedIncoming(daruJID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("an older mark reopened %d messages, want 0", len(pending))
	}
}

// A tag arrives as a bare @<number>, and it is usually a LID rather than a
// phone number, so the name has to be found through the alias table.
func TestMentionsResolveToNames(t *testing.T) {
	store := fixtureStore(t)

	// Invented addresses, in the shape WhatsApp actually uses: a LID is a long
	// run of digits, and the same person is reachable at a phone number too.
	const (
		daruLID   = "10000000000001@lid"
		ownLID    = "10000000000002@lid"
		ownPhone  = "919876543210@s.whatsapp.net"
		strangeID = "10000000000003"
	)
	if err := store.LinkJIDs(daruLID, daruJID); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}
	store.Identify("Rintaro Okabe", ownPhone, ownLID)

	sent := time.Date(2026, time.August, 12, 15, 0, 0, 0, time.UTC)
	tagged := db.Message{
		ID: "m1", ChatJID: groupJID, SenderJID: daruJID, Sender: "Daru",
		Content:   "@" + strings.TrimSuffix(daruLID, "@lid") + " @" + strings.TrimSuffix(ownLID, "@lid") + " @" + strangeID + " who is coming",
		Timestamp: sent, IsGroup: true,
	}
	if err := store.SaveMessage(tagged); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, 10)
	if err != nil {
		t.Fatal(err)
	}

	var got string
	for _, message := range messages {
		if message.ID == "m1" {
			got = message.Content
		}
	}

	want := "@Daru @Rintaro Okabe @" + strangeID + " who is coming"
	if got != want {
		t.Errorf("mentions resolved to\n  %q\nwant\n  %q", got, want)
	}
}

// Nothing that merely looks like a tag should be rewritten, and a number
// nobody answers to has to survive untouched rather than become a wrong name.
func TestUnknownMentionsAreLeftAlone(t *testing.T) {
	store := fixtureStore(t)

	sent := time.Date(2026, time.August, 12, 15, 0, 0, 0, time.UTC)
	original := "ring @911 or ext @4021 about invoice @99887766554433"
	if err := store.SaveMessage(db.Message{
		ID: "m2", ChatJID: groupJID, SenderJID: daruJID,
		Content: original, Timestamp: sent, IsGroup: true,
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "m2" && message.Content != original {
			t.Errorf("text was rewritten to %q, want %q", message.Content, original)
		}
	}
}

// A handle that already starts with an @ would otherwise render as "@@handle".
func TestSelfMentionDoesNotDoubleTheAt(t *testing.T) {
	store := fixtureStore(t)
	store.Identify("@hououin_kyouma", "10000000000002@lid")

	sent := time.Date(2026, time.August, 12, 15, 0, 0, 0, time.UTC)
	if err := store.SaveMessage(db.Message{
		ID: "m3", ChatJID: groupJID, SenderJID: daruJID,
		Content: "oi @10000000000002", Timestamp: sent, IsGroup: true,
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "m3" && message.Content != "oi @hououin_kyouma" {
			t.Errorf("content is %q, want %q", message.Content, "oi @hououin_kyouma")
		}
	}
}

// Receipts arrive out of order, so a delivery report must never undo a read.
func TestDeliveryStatusOnlyMovesForward(t *testing.T) {
	store := fixtureStore(t)

	if err := store.SetMessageStatus(daruJID, []string{"a"}, db.StatusRead); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMessageStatus(daruJID, []string{"a"}, db.StatusDelivered); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "a" && message.Status != db.StatusRead {
			t.Errorf("status fell back to %q, want it to stay read", message.Status)
		}
	}
}

func TestTickMarksReflectStatus(t *testing.T) {
	if tickMark(db.StatusPending) != "" {
		t.Error("a message not yet sent should have no tick")
	}
	if !strings.Contains(tickMark(db.StatusSent), "✓") {
		t.Error("sent should show one tick")
	}
	if !strings.Contains(tickMark(db.StatusDelivered), "✓✓") {
		t.Error("delivered should show two ticks")
	}
	if tickMark(db.StatusRead) == tickMark(db.StatusDelivered) {
		t.Error("read should look different from delivered")
	}
}

// Somebody with no entry in the address book is shown by the name they chose,
// marked with a tilde so it is clear it is not a saved contact.
func TestUnsavedSendersAreMarkedWithATilde(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "g2", ChatJID: groupJID, SenderJID: "777@s.whatsapp.net",
		PushName: "Suzuha", Content: "hello", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 17, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, message := range messages {
		if message.ID == "g2" {
			found = true
			if message.Sender != "~Suzuha" {
				t.Errorf("sender is %q, want %q", message.Sender, "~Suzuha")
			}
		}
	}
	if !found {
		t.Fatal("seeded message missing")
	}
}

// Two people in a group should not be the same colour.
func TestGroupSendersGetDifferentColours(t *testing.T) {
	first := senderStyle("111@s.whatsapp.net").Render("x")
	second := senderStyle("222@s.whatsapp.net").Render("x")

	if first == second {
		t.Error("different senders rendered identically")
	}
	if first != senderStyle("111@s.whatsapp.net").Render("x") {
		t.Error("the same sender should always get the same colour")
	}
}

// A run of messages from one person carries one name, not one per message.
func TestConsecutiveMessagesShareAName(t *testing.T) {
	store := streamsStore(t)
	base := time.Date(2026, time.August, 12, 18, 0, 0, 0, time.UTC)

	for i, text := range []string{"one", "two", "three"} {
		if err := store.SaveMessage(db.Message{
			ID: "run" + text, ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru",
			Content: text, IsGroup: true, Timestamp: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = messages
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	transcript := page.transcript(60)
	names := 0
	for _, line := range transcript {
		if strings.Contains(line.text, "Daru") {
			names++
		}
	}
	if names >= 3 {
		t.Errorf("the name appears %d times; consecutive messages should share one", names)
	}
	if names == 0 {
		t.Error("the sender's name never appears")
	}
}

func TestPollsRender(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "poll1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 19, 0, 0, 0, time.UTC),
		Poll:      db.Poll{Question: "Lab trip?", Options: []string{"Yes", "No", "Maybe"}},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	var poll db.Message
	for _, message := range messages {
		if message.ID == "poll1" {
			poll = message
		}
	}
	if !poll.IsPoll() {
		t.Fatal("poll did not survive the round trip")
	}
	if len(poll.Poll.Options) != 3 {
		t.Fatalf("got %d options, want 3", len(poll.Poll.Options))
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = []db.Message{poll}
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	rendered := ""
	for _, line := range page.transcript(60) {
		rendered += line.text + "\n"
	}
	for _, want := range []string{"[poll]", "Lab trip?", "Yes", "Maybe"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("transcript is missing %q", want)
		}
	}
}

// Scrolling the rail must not open anything. Opening every chat on the way
// past marks them all read on the phone and pulls a transcript each time.
func TestScrollingTheRailDoesNotOpenChats(t *testing.T) {
	page := fixturePage(t, 96, 24)
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	was := page.openJID
	if was == "" {
		t.Fatal("nothing open to begin with")
	}

	// Keyboard.
	moved, cmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	page = moved.(ConversationsPage)
	if page.cursor != 1 {
		t.Errorf("down left the highlight at %d, want 1", page.cursor)
	}
	if page.openJID != was {
		t.Errorf("scrolling changed the open chat to %s", page.openJID)
	}
	if cmd != nil {
		t.Error("scrolling should not fetch anything")
	}

	// Wheel over the rail.
	l := computeLayout(page.app.width, page.app.height, false)
	wheeled, wheelCmd := page.handleWheel(tea.Mouse{X: 2, Y: l.chatTop, Button: tea.MouseWheelDown})
	page = wheeled.(ConversationsPage)
	if page.openJID != was {
		t.Errorf("the wheel changed the open chat to %s", page.openJID)
	}
	if wheelCmd != nil {
		t.Error("the wheel should not fetch anything")
	}
}

// Enter is how the highlighted chat actually gets opened, now that moving on
// to it no longer does.
func TestEnterOpensTheHighlightedChat(t *testing.T) {
	page := fixturePage(t, 96, 24)
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	first := page.openJID
	moved, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	page = moved.(ConversationsPage)

	wanted := page.visible()[page.cursor].JID
	entered, cmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = entered.(ConversationsPage)

	if page.openJID == first {
		t.Error("enter did not open the highlighted chat")
	}
	if page.openJID != wanted {
		t.Errorf("opened %s, want %s", page.openJID, wanted)
	}
	if cmd == nil {
		t.Error("opening a chat should load its messages")
	}
}

// But only when there is nothing to send: enter with text in the box still
// sends it, so replying does not need a detour through tab.
func TestEnterStillSendsWhenSomethingIsTyped(t *testing.T) {
	page := fixturePage(t, 96, 24)
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	was := page.openJID
	moved, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	page = moved.(ConversationsPage)

	for _, r := range "hello" {
		typed, _ := page.handleKey(tea.KeyPressMsg{Text: string(r), Code: r})
		page = typed.(ConversationsPage)
	}

	entered, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = entered.(ConversationsPage)
	if page.openJID != was {
		t.Errorf("enter with text typed opened %s instead of sending", page.openJID)
	}
}

// The rail reorders whenever a message lands. The highlight has to follow the
// conversation it was on -- not the row number, which now holds something
// else, and not the open chat, which would yank it back from wherever the user
// had scrolled to.
func TestHighlightFollowsItsOwnChatAcrossAReorder(t *testing.T) {
	page := fixturePage(t, 96, 24)
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	moved, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	page = moved.(ConversationsPage)

	under := page.visible()[page.cursor].JID
	openWas := page.openJID
	if under == openWas {
		t.Fatal("this test needs the highlight somewhere other than the open chat")
	}

	// A message arrives elsewhere and the list comes back in a new order.
	reordered := make([]db.Chat, len(page.chats))
	copy(reordered, page.chats)
	for i, j := 0, len(reordered)-1; i < j; i, j = i+1, j-1 {
		reordered[i], reordered[j] = reordered[j], reordered[i]
	}

	next, _ := page.action(chatsLoadedMsg{chats: reordered})
	page = next.(ConversationsPage)

	if got := page.visible()[page.cursor].JID; got != under {
		t.Errorf("the highlight slid to %s, want it to stay on %s", got, under)
	}
	if page.openJID != openWas {
		t.Errorf("the open chat changed to %s", page.openJID)
	}
}

// A chat entry is four rows -- name, preview, a blank and a separator -- and
// all four are part of the thing you are clicking on.
//
// The row the highlight happens to be on is the one that matters here: it used
// to be skipped as "already open", which was true only while scrolling opened
// chats. The wheel drags the highlight to whatever you are scrolling past, so
// that left the chat under the pointer as the one click could not open.
func TestEveryRowOfAChatEntryOpensIt(t *testing.T) {
	store := fixtureStore(t)
	page := openConversationsPage(&app{messages: store, width: 200, height: 50})
	for i := range 12 {
		page.chats = append(page.chats, db.Chat{
			JID:  fmt.Sprintf("%d@s.whatsapp.net", i),
			Name: fmt.Sprintf("Chat %d", i),
			Kind: db.KindChat,
		})
	}
	page.openJID = page.chats[0].JID

	l := computeLayout(200, 50, false)
	const target = 7
	want := page.chats[target].JID

	for row, part := range []string{"name", "preview", "blank", "separator"} {
		fresh := page
		// The highlight sitting on the very chat being clicked is the case
		// that used to fail.
		fresh.cursor = target

		y := l.chatTop + target*chatEntryRows + row
		next, cmd := fresh.handleClick(tea.Mouse{X: 2, Y: y, Button: tea.MouseLeft})
		got := next.(ConversationsPage)

		if got.openJID != want {
			t.Errorf("clicking the %s row (y=%d) opened %q, want %q", part, y, got.openJID, want)
		}
		if cmd == nil {
			t.Errorf("clicking the %s row did not load the conversation", part)
		}
	}
}

// Clicking the chat already on screen should not refetch it, but it should
// still move the highlight there.
func TestClickingTheOpenChatMovesTheHighlightWithoutReloading(t *testing.T) {
	page := fixturePage(t, 96, 24)
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	// Highlight parked somewhere else, as if scrolled away and back.
	page.cursor = 1

	l := computeLayout(page.app.width, page.app.height, false)
	next, cmd := page.handleClick(tea.Mouse{X: 2, Y: l.chatTop, Button: tea.MouseLeft})
	got := next.(ConversationsPage)

	if cmd != nil {
		t.Error("the open chat was fetched again")
	}
	if got.cursor != 0 {
		t.Errorf("highlight is at %d, want it moved to the clicked row", got.cursor)
	}
}

// Now that the highlight and the open conversation can sit on different rows,
// a reader has to be able to tell which is which.
func TestRailShowsHighlightAndOpenChatApart(t *testing.T) {
	page := fixturePage(t, 96, 24)
	chat := page.chats[0]

	plainRow, _ := page.chatEntry(chat, false, false, 30)
	cursorRow, _ := page.chatEntry(chat, true, false, 30)
	openRow, _ := page.chatEntry(chat, false, true, 30)

	if cursorRow == plainRow {
		t.Error("the highlighted row looks identical to an ordinary one")
	}
	if openRow == plainRow {
		t.Error("the open chat looks identical to one that is not open")
	}
	if cursorRow == openRow {
		t.Error("the highlight and the open chat are indistinguishable")
	}
}

// The picker offers nine slots and fills them from what has actually been
// used, falling back to the defaults for whatever is left over.
func TestPaletteMergesHistoryOverDefaults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history []string
		want    []string // only the leading entries that must be exact
	}{
		{
			name: "nothing used yet falls back to the defaults",
			want: defaultPalette,
		},
		{
			name:    "a used emoji outranks every default",
			history: []string{"🥳"},
			want:    []string{"🥳", "👍", "❤️"},
		},
		{
			name:    "history order is kept",
			history: []string{"😢", "🔥", "👍"},
			want:    []string{"😢", "🔥", "👍", "❤️"},
		},
		{
			name:    "a default already in history is not repeated",
			history: []string{"👍"},
			want:    []string{"👍", "❤️", "😂"},
		},
	} {
		got := palette(tc.history)

		if len(got) != paletteSize {
			t.Errorf("%s: palette has %d entries, want %d", tc.name, len(got), paletteSize)
		}
		for i, want := range tc.want {
			if i < len(got) && got[i] != want {
				t.Errorf("%s: slot %d is %q, want %q", tc.name, i+1, got[i], want)
			}
		}

		seen := map[string]bool{}
		for _, emoji := range got {
			if seen[emoji] {
				t.Errorf("%s: %q appears twice", tc.name, emoji)
			}
			seen[emoji] = true
		}
	}
}

// More history than there are slots must not overflow the number keys.
func TestPaletteNeverExceedsTheNumberKeys(t *testing.T) {
	history := []string{"1️⃣", "2️⃣", "3️⃣", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣", "🔟", "🅰️"}

	got := palette(history)
	if len(got) != paletteSize {
		t.Fatalf("palette has %d entries, want %d", len(got), paletteSize)
	}
	if got[len(got)-1] != "9️⃣" {
		t.Errorf("last slot is %q, want the ninth most-used", got[len(got)-1])
	}
}

// The whole point: an emoji reached for through the custom box is counted like
// any other and climbs into the quick-pick.
func TestUsedEmojiClimbTheQuickPick(t *testing.T) {
	store := fixtureStore(t)

	// A custom one used often, and a default used once.
	const custom = "🫠"
	for range 3 {
		if err := store.RecordEmojiUse(custom); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordEmojiUse("😂"); err != nil {
		t.Fatal(err)
	}

	history, err := store.TopEmoji(paletteSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0] != custom || history[1] != "😂" {
		t.Fatalf("history is %v, want the custom one first", history)
	}

	got := palette(history)
	if got[0] != custom {
		t.Errorf("slot 1 is %q, want the most-used %q", got[0], custom)
	}
	if got[1] != "😂" {
		t.Errorf("slot 2 is %q, want %q", got[1], "😂")
	}
	// And the defaults still fill the rest rather than leaving gaps.
	if len(got) != paletteSize {
		t.Errorf("palette has %d entries, want %d", len(got), paletteSize)
	}
}

// Two emoji used equally often are ordered by which was reached for last.
func TestEqualUseBreaksTiesOnRecency(t *testing.T) {
	store := fixtureStore(t)

	for _, emoji := range []string{"🐢", "🐇"} {
		if err := store.RecordEmojiUse(emoji); err != nil {
			t.Fatal(err)
		}
	}
	// Both were used once, in the same second. Setting the timestamps apart
	// tests the ordering itself rather than making the test sleep for it.
	if err := store.Exec(`UPDATE emoji_uses SET last_used = 100 WHERE emoji = '🐢'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Exec(`UPDATE emoji_uses SET last_used = 200 WHERE emoji = '🐇'`); err != nil {
		t.Fatal(err)
	}

	history, err := store.TopEmoji(paletteSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0] != "🐇" {
		t.Errorf("history is %v, want the more recent one first", history)
	}
}

// The palette is six emoji; anything else has to be reachable or most of the
// keyboard's worth of reactions is simply unavailable.
func TestCustomReactionAcceptsAnyEmoji(t *testing.T) {
	page := fixturePage(t, 96, 24)
	page.selected = len(page.messages) - 1
	page.reacting = true

	press := func(p ConversationsPage, key tea.KeyPressMsg) ConversationsPage {
		t.Helper()
		next, _ := p.handleKey(key)
		return next.(ConversationsPage)
	}

	// "0" opens the custom slot without leaving the picker.
	page = press(page, tea.KeyPressMsg{Text: "0", Code: '0'})
	if !page.reactCustom {
		t.Fatal("0 did not open the custom reaction box")
	}
	if !page.reacting {
		t.Error("opening the custom box should stay inside the picker")
	}

	// A skin-toned thumbs-up: four runes, one character. Typed as a paste,
	// which is how one realistically arrives from a system emoji picker.
	const thumb = "👍🏽"
	pasted, _ := page.handlePaste(thumb)
	page = pasted.(ConversationsPage)
	if got := page.reactInput.string(); got != thumb {
		t.Fatalf("reaction box holds %q, want %q", got, thumb)
	}

	sent, cmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = sent.(ConversationsPage)
	if cmd == nil {
		t.Error("enter did not produce a send command")
	}
	if page.reacting || page.reactCustom {
		t.Error("the picker should close once the reaction is away")
	}
	if !strings.Contains(page.status, thumb) {
		t.Errorf("status is %q, want it to name the emoji sent", page.status)
	}
}

// WhatsApp allows one emoji per person per message, so a longer string is
// refused here rather than sent and quietly dropped by the server.
func TestCustomReactionRefusesMoreThanOneCharacter(t *testing.T) {
	page := fixturePage(t, 96, 24)
	page.selected = len(page.messages) - 1
	page.reacting, page.reactCustom = true, true

	typed, _ := page.handlePaste("😂😂")
	page = typed.(ConversationsPage)

	sent, cmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = sent.(ConversationsPage)
	if cmd != nil {
		t.Error("two emoji should not have been sent")
	}
	if !page.failed {
		t.Error("refusing should say so rather than silently doing nothing")
	}
	if !page.reactCustom {
		t.Error("the box should stay open so the mistake can be corrected")
	}
}

// Multi-rune emoji are one character: a heart carries a variation selector, a
// family is joined by zero-width joiners. Counting runes would reject them all.
func TestGraphemeCountTreatsEmojiAsOneCharacter(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"😂", 1},
		{"❤️", 1},   // heart + variation selector
		{"👍🏽", 1},   // thumbs-up + skin tone
		{"👨‍👩‍👧", 1}, // joined family
		{"😂😂", 2},
		{"", 0},
	} {
		if got := graphemeCount(tc.text); got != tc.want {
			t.Errorf("graphemeCount(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

// Escape out of the custom box goes back to the palette rather than closing
// the picker, so one mistyped character does not cost the whole thing.
func TestEscapeFromCustomReactionReturnsToPalette(t *testing.T) {
	page := fixturePage(t, 96, 24)
	page.selected = len(page.messages) - 1
	page.reacting, page.reactCustom = true, true

	typed, _ := page.handlePaste("x")
	page = typed.(ConversationsPage)

	backed, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	page = backed.(ConversationsPage)
	if page.reactCustom {
		t.Error("escape should leave the custom box")
	}
	if !page.reacting {
		t.Error("escape from the custom box should return to the palette")
	}
	if !page.reactInput.empty() {
		t.Error("the abandoned text should not be waiting next time")
	}
}

// View-once media is kept like anything else, so the reader has to be told
// which it is: the sender believes it is already gone.
func TestViewOnceIsLabelled(t *testing.T) {
	store := streamsStore(t)

	sent := time.Date(2026, time.August, 12, 20, 0, 0, 0, time.UTC)
	if err := store.SaveMessage(db.Message{
		ID: "vo1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", IsGroup: true,
		Timestamp: sent,
		Media:     db.Media{Kind: db.MediaImage, Mime: "image/jpeg", Size: 1000, ViewOnce: true},
	}); err != nil {
		t.Fatal(err)
	}
	// An ordinary photo alongside it, so a label that is simply always on would
	// not pass.
	if err := store.SaveMessage(db.Message{
		ID: "ord1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", IsGroup: true,
		Timestamp: sent.Add(time.Minute),
		Media:     db.Media{Kind: db.MediaImage, Mime: "image/jpeg", Size: 1000},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, 10)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]db.Message{}
	for _, message := range messages {
		byID[message.ID] = message
	}
	if !byID["vo1"].Media.ViewOnce {
		t.Error("the view-once flag did not survive the round trip")
	}
	if byID["ord1"].Media.ViewOnce {
		t.Error("an ordinary photo came back marked view once")
	}

	// The chip is what shows while there is no picture to draw.
	if got := mediaChip(byID["vo1"].Media); !strings.Contains(got, "[view once photo]") {
		t.Errorf("chip is %q, want it to say view once", got)
	}
	if got := mediaChip(byID["ord1"].Media); strings.Contains(got, "view once") {
		t.Errorf("ordinary photo chip is %q", got)
	}

	// And the rail preview, where the whole message is one line.
	if got := byID["vo1"].Preview(); got != "[view once photo]" {
		t.Errorf("preview is %q, want %q", got, "[view once photo]")
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = []db.Message{byID["vo1"]}
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	rendered := ""
	for _, line := range page.transcript(60) {
		rendered += stripANSIForTest.ReplaceAllString(line.text, "") + "\n"
	}
	if !strings.Contains(rendered, "view once") {
		t.Errorf("transcript does not say view once:\n%s", rendered)
	}
}

// When the picture draws inline there is no chip to carry the marker, so it
// has to be a line of its own or it vanishes exactly when it matters.
func TestViewOnceIsLabelledOnADrawnPicture(t *testing.T) {
	store := streamsStore(t)

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}
	page.messages = []db.Message{{
		ID: "vo2", ChatJID: groupJID, SenderJID: daruJID, Sender: "Daru", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 20, 0, 0, 0, time.UTC),
		Media: db.Media{
			Kind: db.MediaImage, Mime: "image/png", Size: 1000, ViewOnce: true,
			Thumbnail: testPNG(t, 24, 24),
		},
	}}

	var rendered, drew = "", false
	for _, line := range page.transcript(60) {
		rendered += stripANSIForTest.ReplaceAllString(line.text, "") + "\n"
		if strings.Contains(line.text, "\x1b[") && strings.Contains(line.text, "▀") {
			drew = true
		}
	}
	if !drew {
		t.Skip("the picture did not draw here, so there is no chipless case to check")
	}
	if !strings.Contains(rendered, "[view once]") {
		t.Errorf("a drawn view-once picture lost its label:\n%s", rendered)
	}
}

// A reaction on a photo has to appear, which means the reaction line comes
// after the picture rather than being lost behind it.
func TestReactionsShowOnMediaMessages(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "img1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 20, 0, 0, 0, time.UTC),
		Media:     db.Media{Kind: db.MediaImage, Mime: "image/jpeg", Size: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReaction(groupJID, db.Reaction{
		MessageID: "img1", SenderJID: christinaJID, Sender: "Christina",
		Emoji: "👍", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	var image db.Message
	for _, message := range messages {
		if message.ID == "img1" {
			image = message
		}
	}
	if len(image.Reactions) != 1 {
		t.Fatalf("got %d reactions on the photo, want 1", len(image.Reactions))
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = []db.Message{image}
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	rendered := ""
	for _, line := range page.transcript(60) {
		rendered += line.text + "\n"
	}
	if !strings.Contains(rendered, "👍 1") {
		t.Errorf("the photo's reaction is missing from:\n%s", rendered)
	}
}

// Scripts whose width the terminal and Bubble Tea disagree about are folded
// away; emoji are not, because both rulers agree they are two cells.
func TestPlainFoldsScriptsButKeepsEmoji(t *testing.T) {
	if !foldToASCII {
		t.Skip("folding disabled in this environment")
	}

	for _, sample := range []string{"മലയാളം ഗ്രൂപ്പ്", "日本語", "hello", "café"} {
		folded := plain(sample)
		if !isASCII(folded) {
			t.Errorf("plain(%q) = %q, which is still not ASCII", sample, folded)
		}
		if got := measure(folded); got != len(folded) {
			t.Errorf("plain(%q) measures %d but is %d bytes", sample, got, len(folded))
		}
	}

	for _, emoji := range []string{"👍", "🔥", "😂"} {
		if got := plain("ok " + emoji); got != "ok "+emoji {
			t.Errorf("plain kept %q, want the emoji left alone", got)
		}
	}

	// Mixed text keeps the emoji and folds the script around it.
	if got := plain("മല 👍"); got != "?? 👍" {
		t.Errorf("plain(%q) = %q, want the script folded and the emoji kept", "മല 👍", got)
	}
}

// --- LID / phone-number addressing ---------------------------------------

const (
	savedPN  = "31415@s.whatsapp.net"
	savedLID = "88881@lid"
)

// The bug this guards: WhatsApp addresses the same person by phone number in
// the address book and by LID in a group, so a saved contact showed up as a
// raw ID or a ~pushname.
func TestSavedContactResolvesWhenAddressedByLID(t *testing.T) {
	store := streamsStore(t)

	// The address book knows them by phone number.
	if err := store.SaveContact(savedPN, "Kurisu Makise"); err != nil {
		t.Fatal(err)
	}
	// A group message arrives addressed by LID, carrying only a push name.
	if err := store.SaveMessage(db.Message{
		ID: "lid1", ChatJID: groupJID, SenderJID: savedLID,
		PushName: "Christina", Content: "do not call me that", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	senderOf := func(id string) string {
		t.Helper()
		messages, err := store.Messages(groupJID, historyLimit)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.ID == id {
				return message.Sender
			}
		}
		t.Fatalf("message %s missing", id)
		return ""
	}

	// Before the pairing is known there is nothing to go on but the push name.
	if got := senderOf("lid1"); got != "~Christina" {
		t.Errorf("without the pairing the sender is %q, want %q", got, "~Christina")
	}

	// Learning the pairing must retro-fit the saved name.
	if err := store.LinkJIDs(savedLID, savedPN); err != nil {
		t.Fatal(err)
	}
	if got := senderOf("lid1"); got != "Kurisu Makise" {
		t.Errorf("sender is %q, want the saved contact name", got)
	}
}

// The pairing can be learned before the address book is read, so saving a
// contact afterwards has to reach both addresses too.
func TestContactSavedAfterLinkingReachesBothAddresses(t *testing.T) {
	store := streamsStore(t)

	if err := store.LinkJIDs(savedPN, savedLID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContact(savedPN, "Kurisu Makise"); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage(db.Message{
		ID: "lid2", ChatJID: groupJID, SenderJID: savedLID,
		PushName: "Christina", Content: "hello", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 12, 22, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "lid2" && message.Sender != "Kurisu Makise" {
			t.Errorf("sender is %q, want the saved contact name", message.Sender)
		}
	}
}

// A one-to-one chat whose JID is a LID must show the saved name in the rail.
func TestChatAddressedByLIDShowsSavedName(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContact(savedPN, "Kurisu Makise"); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkJIDs(savedLID, savedPN); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(db.Message{
		ID: "lid3", ChatJID: savedLID, SenderJID: savedLID,
		PushName: "Christina", Content: "hi",
		Timestamp: time.Date(2026, time.August, 12, 23, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range chats {
		if chat.JID == savedLID && chat.Name != "Kurisu Makise" {
			t.Errorf("chat name is %q, want the saved contact name", chat.Name)
		}
	}
}

// SaveContacts writes the address book in one transaction. The names still
// have to reach both addresses for everyone with a known LID pairing.
func TestBatchedContactSyncResolvesBothAddresses(t *testing.T) {
	store := streamsStore(t)

	if err := store.LinkJIDs(savedPN, savedLID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContacts(map[string]string{
		savedPN: "Kurisu Makise",
		daruJID: "Daru",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage(db.Message{
		ID: "batch1", ChatJID: groupJID, SenderJID: savedLID,
		PushName: "Christina", Content: "hi", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "batch1" && message.Sender != "Kurisu Makise" {
			t.Errorf("sender is %q, want the saved contact name", message.Sender)
		}
	}
}

// Mirroring must not clobber a name that was saved directly.
func TestMirroringDoesNotOverwriteASavedName(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContacts(map[string]string{
		savedPN:  "Kurisu Makise",
		savedLID: "Makise Kurisu",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkJIDs(savedPN, savedLID); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage(db.Message{
		ID: "keep1", ChatJID: savedLID, SenderJID: savedLID, Content: "hi",
		Timestamp: time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range chats {
		if chat.JID == savedLID && chat.Name != "Makise Kurisu" {
			t.Errorf("chat name is %q; mirroring overwrote the name saved for that address", chat.Name)
		}
	}
}

// The LID map lives in whatsmeow's own table. Importing it must work when the
// table is there, and be a harmless no-op before the first login.
func TestImportLIDMapReadsWhatsmeowsTable(t *testing.T) {
	store := streamsStore(t)

	// Before login there is no such table.
	if err := store.ImportLIDMap(); err != nil {
		t.Fatalf("import with no table should be a no-op: %v", err)
	}

	if err := store.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ('88881', '31415')`); err != nil {
		t.Fatal(err)
	}

	if err := store.ImportLIDMap(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := store.SaveContacts(map[string]string{savedPN: "Kurisu Makise"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage(db.Message{
		ID: "map1", ChatJID: groupJID, SenderJID: savedLID,
		PushName: "Christina", Content: "hi", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 13, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "map1" && message.Sender != "Kurisu Makise" {
			t.Errorf("sender is %q; the imported LID pairing was not used", message.Sender)
		}
	}
}

// With no saved name, the profile name must show with a tilde rather than
// pretending to be an address-book entry.
func TestProfileNameIsMarkedNotSaved(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "dad1", ChatJID: groupJID, SenderJID: "5551@s.whatsapp.net",
		PushName: "Rintaro | Mad Scientist", Content: "hi", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	senderOf := func() string {
		t.Helper()
		messages, err := store.Messages(groupJID, historyLimit)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.ID == "dad1" {
				return message.Sender
			}
		}
		t.Fatal("message missing")
		return ""
	}

	if got := senderOf(); got != "~Rintaro | Mad Scientist" {
		t.Errorf("sender is %q, want the profile name marked with a tilde", got)
	}

	// Once the address book arrives, the saved name takes over, unmarked.
	if err := store.SaveContacts(map[string]string{"5551@s.whatsapp.net": "Dad"}); err != nil {
		t.Fatal(err)
	}
	if got := senderOf(); got != "Dad" {
		t.Errorf("sender is %q, want the saved name", got)
	}
}

// Syncing the address book must clear names written by an older version that
// mistook profile names for saved ones.
func TestContactSyncReplacesStaleNames(t *testing.T) {
	store := streamsStore(t)

	// As an older build would have stored it: a profile name, as if saved.
	if err := store.SaveContacts(map[string]string{daruJID: "Super Hacka"}); err != nil {
		t.Fatal(err)
	}
	// A later sync knows about somebody else entirely.
	if err := store.SaveContacts(map[string]string{christinaJID: "Kurisu"}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(daruJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if !message.FromMe && strings.Contains(message.Sender, "Super Hacka") {
			t.Errorf("stale name %q survived a contact sync", message.Sender)
		}
	}
}

// --- search ---------------------------------------------------------------

// Typing in the search box narrows the rail, and everything that indexes chats
// has to follow it -- otherwise the cursor opens whichever chat used to be at
// that position.
func TestSearchFiltersAndIndexesTheSameList(t *testing.T) {
	store := streamsStore(t)
	if err := store.SaveContacts(map[string]string{
		daruJID: "Daru", christinaJID: "Christina",
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.chats = chats
	page.status = ""

	if len(page.visible()) != len(chats) {
		t.Fatalf("with no search the rail should show every chat")
	}

	page.filtering = true
	for _, r := range "chris" {
		next, _ := page.handleKey(tea.KeyPressMsg{Text: string(r), Code: r})
		page = next.(ConversationsPage)
	}

	matched := page.visible()
	if len(matched) != 1 {
		t.Fatalf("search matched %d chats, want 1", len(matched))
	}
	if matched[0].JID != christinaJID {
		t.Errorf("search matched %s, want Christina", matched[0].JID)
	}

	// The cursor addresses the filtered list, so opening index 0 opens the
	// match rather than whatever was first before.
	next, cmd := page.openChat(0)
	if opened, _ := next.(ConversationsPage).selectedChat(); opened.JID != christinaJID {
		t.Errorf("opened %s, want the searched-for chat", opened.JID)
	}
	loaded, ok := findMessagesLoaded(runCmd(cmd))
	if !ok || loaded.chatJID != christinaJID {
		t.Errorf("loaded %q, want %q", loaded.chatJID, christinaJID)
	}
}

// Escape throws the search away and puts the whole list back.
func TestEscapeClearsTheSearch(t *testing.T) {
	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.chats = chats
	page.filtering = true
	page.filter = typeText(textInput{}, "zzzz")

	if len(page.visible()) != 0 {
		t.Fatal("a search matching nothing should empty the rail")
	}

	next, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	page = next.(ConversationsPage)

	if len(page.visible()) != len(chats) {
		t.Errorf("escape left %d chats, want all %d back", len(page.visible()), len(chats))
	}
}

// --- replies and group members -------------------------------------------

// A reply used to name its author with a raw LID.
func TestReplyAuthorResolvesToAName(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContacts(map[string]string{savedPN: "Kurisu Makise"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkJIDPairs([][2]string{{savedLID, savedPN}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMessage(db.Message{
		ID: "rep1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru",
		Content: "agreed", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 13, 13, 0, 0, 0, time.UTC),
		Reply:     db.Reply{MessageID: "x", Sender: savedLID, Text: "the microwave"},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID != "rep1" {
			continue
		}
		if strings.Contains(message.Reply.Sender, "@lid") {
			t.Errorf("reply author is %q, still a raw LID", message.Reply.Sender)
		}
		if message.Reply.Sender != "Kurisu Makise" {
			t.Errorf("reply author is %q, want the saved name", message.Reply.Sender)
		}
	}
}

// Group metadata is the only place we learn both addresses for a member we
// have never messaged directly.
func TestGroupParticipantPairingNamesMembers(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContacts(map[string]string{savedPN: "Kurisu Makise"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(db.Message{
		ID: "gp1", ChatJID: groupJID, SenderJID: savedLID,
		PushName: "Christina", Content: "hello", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	senderOf := func() string {
		t.Helper()
		messages, err := store.Messages(groupJID, historyLimit)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.ID == "gp1" {
				return message.Sender
			}
		}
		t.Fatal("message missing")
		return ""
	}

	if got := senderOf(); got != "~Christina" {
		t.Errorf("before the group pairing the sender is %q, want the tilde form", got)
	}

	// What refreshGroups now does with GroupParticipant.LID / .PhoneNumber.
	if err := store.LinkJIDPairs([][2]string{{savedLID, savedPN}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	if got := senderOf(); got != "Kurisu Makise" {
		t.Errorf("sender is %q, want the saved name once the group pairing is known", got)
	}
}

// --- tabs, padding, double click -----------------------------------------

// A chat's kind used to be written only when its row was first created, so
// anything stored before the column existed stayed "chat" forever and the
// Status and Channels tabs were permanently empty.
func TestChatKindIsCorrectedOnLaterMessages(t *testing.T) {
	store := streamsStore(t)

	// As an older build left it.
	if err := store.Exec(`UPDATE chats SET kind = 'chat'`); err != nil {
		t.Fatal(err)
	}
	if chats, err := store.Chats(tabChannels.kinds()...); err != nil {
		t.Fatal(err)
	} else if len(chats) != 0 {
		t.Fatal("precondition: the channel should be misfiled as a chat")
	}

	// A newer message puts it back on the right tab.
	if err := store.SaveMessage(db.Message{
		ID: "n2", ChatJID: newsletterJID, SenderJID: newsletterJID,
		Content:   "issue 13",
		Timestamp: time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChannels.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].JID != newsletterJID {
		t.Errorf("Channels tab has %d chats, want the newsletter", len(chats))
	}
}

// Clicking a tab label has to land on it. The hit boxes are in screen columns,
// so they have to account for the panel border and the leading space.
func TestClickingATabSwitchesStream(t *testing.T) {
	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.chats = chats
	l := computeLayout(96, 24, false)

	// The rendered strip is " Chats  Status  Channels" inside the border, so
	// each label must be clickable where it is actually drawn.
	strip := stripped(page.tabStrip(l.railInner))
	for _, span := range tabSpans() {
		name := tabNames[span.tab]
		// span.start is a screen column: border (1) + offset into the strip.
		if got := strings.Index(strip, name) + 1; got != span.start {
			t.Errorf("%s is drawn at column %d but its hit box starts at %d", name, got, span.start)
		}
	}

	for _, span := range tabSpans() {
		next, _ := page.handleClick(tea.Mouse{
			X: span.start, Y: l.tabRow, Button: tea.MouseLeft,
		})
		if got := next.(ConversationsPage).tab; got != span.tab {
			t.Errorf("clicking %s selected tab %d, want %d", tabNames[span.tab], got, span.tab)
		}
	}
}

// Every chat block is the same height, so a click divides cleanly back into an
// index however much padding the entries carry.
func TestRailEntriesAreUniformHeight(t *testing.T) {
	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) < 3 {
		t.Fatalf("need at least three chats, got %d", len(chats))
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 40})
	page.chats = chats
	l := computeLayout(96, 40, false)

	for index := range 3 {
		row := l.chatTop + index*chatEntryRows
		next, _ := page.handleClick(tea.Mouse{X: 2, Y: row, Button: tea.MouseLeft})
		opened := next.(ConversationsPage)
		if opened.cursor != index {
			t.Errorf("clicking row %d opened chat %d, want %d", row, opened.cursor, index)
		}
	}
}

// Double clicking a message starts a reply to it, without reaching for ctrl+r.
func TestDoubleClickRepliesToAMessage(t *testing.T) {
	page := fixturePage(t, 96, 24)
	l := computeLayout(96, 24, false)
	row := l.transcriptTop + l.transcriptRows - 1

	click := func(p ConversationsPage) ConversationsPage {
		next, _ := p.handleClick(tea.Mouse{X: l.railTotal + 3, Y: row, Button: tea.MouseLeft})
		return next.(ConversationsPage)
	}

	first := click(page)
	if first.replyTo.ID != "" {
		t.Fatal("one click should select, not start a reply")
	}
	if first.selected < 0 {
		t.Fatal("one click should select a message")
	}

	second := click(first)
	if second.replyTo.ID == "" {
		t.Error("a second click on the same message should start a reply")
	}

	// A slow second click is two separate selections, not a double.
	slow := click(page)
	slow.lastClickAt = time.Now().Add(-2 * time.Second)
	if got := click(slow); got.replyTo.ID != "" {
		t.Error("clicks too far apart should not count as a double click")
	}
}

func stripped(s string) string { return stripANSIForTest.ReplaceAllString(s, "") }

// --- the rail reordering under the cursor --------------------------------

// Sending a message lifts that chat to the top of the rail. The open
// conversation must come with it, rather than the highlight staying on a row
// that now holds whichever chat was pushed down.
func TestOpenChatFollowsTheRailReordering(t *testing.T) {
	store := streamsStore(t)
	if err := store.SaveContacts(map[string]string{
		daruJID: "Daru", christinaJID: "Christina",
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) < 3 {
		t.Fatalf("need three chats, got %d", len(chats))
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	page.status = ""

	// Open the one sitting at the bottom.
	bottom := len(chats) - 1
	target := chats[bottom].JID
	next, _ := page.openChat(bottom)
	page = next.(ConversationsPage)

	if opened, _ := page.selectedChat(); opened.JID != target {
		t.Fatalf("opened %s, want %s", opened.JID, target)
	}

	// Sending to it lifts it to the top, so every other chat shifts down one.
	if err := store.SaveMessage(db.Message{
		ID: "lift1", ChatJID: target, SenderJID: "me@s.whatsapp.net", Sender: "You",
		FromMe: true, Content: "hello", Status: db.StatusSent,
		Timestamp: time.Date(2026, time.August, 15, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	reordered, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].JID != target {
		t.Fatalf("precondition: %s should now be first", target)
	}

	after, _ := page.action(chatsLoadedMsg{chats: reordered})
	page = after.(ConversationsPage)

	if opened, _ := page.selectedChat(); opened.JID != target {
		t.Errorf("after reordering the open chat is %s, want %s", opened.JID, target)
	}
	if page.cursor != 0 {
		t.Errorf("highlight is on row %d, want 0 where the chat moved to", page.cursor)
	}
}

// Searching moves the highlight, but must not silently swap the conversation
// being read.
func TestSearchDoesNotChangeTheOpenConversation(t *testing.T) {
	store := streamsStore(t)
	if err := store.SaveContacts(map[string]string{
		daruJID: "Daru", christinaJID: "Christina",
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	page.status = ""

	next, _ := page.openChat(0)
	page = next.(ConversationsPage)
	opened, _ := page.selectedChat()

	page.filtering = true
	for _, r := range "chris" {
		stepped, _ := page.handleKey(tea.KeyPressMsg{Text: string(r), Code: r})
		page = stepped.(ConversationsPage)
	}

	if still, _ := page.selectedChat(); still.JID != opened.JID {
		t.Errorf("searching changed the open conversation to %s, want %s", still.JID, opened.JID)
	}
}

// --- delete, forward, drag and drop --------------------------------------

// A deleted message keeps its place in the conversation, with a placeholder,
// and loses its text and any attachment.
func TestRevokedMessagesShowAPlaceholder(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "del1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru",
		Content: "something regrettable", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC),
		Media:     db.Media{Kind: db.MediaImage, Proto: []byte("x"), Path: "/tmp/x.png"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRevoked(groupJID, "del1"); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	var revoked db.Message
	var found bool
	for _, message := range messages {
		if message.ID == "del1" {
			revoked, found = message, true
		}
	}
	if !found {
		t.Fatal("a deleted message should stay in the transcript")
	}
	if !revoked.Revoked {
		t.Error("message is not marked revoked")
	}
	if revoked.Content != "" || len(revoked.Media.Proto) != 0 || revoked.Media.Path != "" {
		t.Error("deleting should drop the text and the attachment")
	}
	if revoked.Preview() != "[deleted]" {
		t.Errorf("preview is %q, want [deleted]", revoked.Preview())
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = []db.Message{revoked}
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	var rendered string
	for _, line := range page.transcript(60) {
		rendered += line.text + "\n"
	}
	if !strings.Contains(rendered, "[message deleted]") {
		t.Errorf("transcript is missing the placeholder:\n%s", rendered)
	}
	if strings.Contains(rendered, "something regrettable") {
		t.Error("the deleted text is still on screen")
	}
}

// Forwarding turns the rail into a picker, and choosing a chat sends there
// rather than opening it.
func TestForwardPicksADestination(t *testing.T) {
	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	page.status = ""
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)
	before, _ := page.selectedChat()

	page.messages = []db.Message{{
		ID: "fw1", ChatJID: before.JID, Content: "look at this",
		Timestamp: time.Now(),
	}}
	page.selected = 0

	next, _ := page.handleKey(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	page = next.(ConversationsPage)
	if !page.forwarding {
		t.Fatal("ctrl+w should start picking a destination")
	}
	if page.forwardMsg.ID != "fw1" {
		t.Errorf("holding message %q, want fw1", page.forwardMsg.ID)
	}

	// Choosing another chat forwards there and leaves the open one alone.
	page.cursor = 1
	sent, cmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = sent.(ConversationsPage)

	if page.forwarding {
		t.Error("picking a chat should end the forward")
	}
	if still, _ := page.selectedChat(); still.JID != before.JID {
		t.Errorf("forwarding changed the open conversation to %s", still.JID)
	}
	if cmd == nil {
		t.Error("expected a command to do the forwarding")
	}

	// Escape backs out without sending.
	page.forwarding = true
	cancelled, cancelCmd := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancelled.(ConversationsPage).forwarding {
		t.Error("escape should cancel the forward")
	}
	if cancelCmd != nil {
		t.Error("cancelling should not send anything")
	}
}

// Dropping a file onto the terminal arrives as a bracketed paste of its path.
func TestDroppedFileBecomesAnAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "divergence.png")
	if err := os.WriteFile(path, []byte("not really a png"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	// The shapes terminals actually send.
	for _, dropped := range []string{
		path,
		"'" + path + "'",
		"\"" + path + "\"",
		"file://" + path,
	} {
		next, _ := page.handlePaste(dropped)
		got := next.(ConversationsPage)
		if got.pending != path {
			t.Errorf("paste %q attached %q, want %q", dropped, got.pending, path)
		}
	}

	// Ordinary text is typed, not mistaken for a file.
	typed, _ := page.handlePaste("just some text")
	if got := typed.(ConversationsPage); got.pending != "" {
		t.Errorf("plain text was treated as a file: %q", got.pending)
	} else if got.input.string() != "just some text" {
		t.Errorf("pasted text became %q", got.input.string())
	}

	// A path that does not exist is text too.
	missing, _ := page.handlePaste(filepath.Join(dir, "nope.png"))
	if got := missing.(ConversationsPage); got.pending != "" {
		t.Error("a non-existent path should not attach")
	}
}

// Enter sends the dropped file, using anything typed as its caption.
func TestPendingAttachmentIsSentOnEnter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	dropped, _ := page.handlePaste(path)
	page = dropped.(ConversationsPage)
	page.input = typeText(textInput{}, "here you go")

	sent, cmd := page.submit()
	page = sent.(ConversationsPage)

	if page.pending != "" {
		t.Error("the attachment should be cleared once sent")
	}
	if page.input.string() != "" {
		t.Error("the caption should be cleared once sent")
	}
	if cmd == nil {
		t.Fatal("expected a command to upload the file")
	}

	// Escape drops it instead.
	again, _ := page.handlePaste(path)
	page = again.(ConversationsPage)
	cancelled, _ := page.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cancelled.(ConversationsPage).pending != "" {
		t.Error("escape should drop the pending attachment")
	}
}

// --- stickers and stale sender ids ---------------------------------------

// An animated sticker's webp cannot be decoded, so it has to be drawn from the
// still WhatsApp embeds in the message rather than falling back to [sticker].
func TestStickersDrawFromTheEmbeddedThumbnail(t *testing.T) {
	png := testPNG(t, 64, 64)

	store := streamsStore(t)
	if err := store.SaveMessage(db.Message{
		ID: "stick1", ChatJID: groupJID, SenderJID: daruJID, PushName: "Daru", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC),
		Media: db.Media{
			Kind: db.MediaSticker, Mime: "image/webp", Size: 40000,
			// No Path: the animated webp was never decodable anyway.
			Thumbnail: png,
		},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}

	var sticker db.Message
	for _, message := range messages {
		if message.ID == "stick1" {
			sticker = message
		}
	}
	if len(sticker.Media.Thumbnail) == 0 {
		t.Fatal("the thumbnail did not survive the round trip")
	}

	page := openConversationsPage(&app{messages: store, width: 96, height: 24})
	page.messages = []db.Message{sticker}
	page.chats = []db.Chat{{JID: groupJID, Name: "Lab", Kind: db.KindGroup, IsGroup: true}}

	var rendered string
	for _, line := range page.transcript(60) {
		rendered += line.text + "\n"
	}
	if strings.Contains(rendered, "[sticker]") {
		t.Errorf("the sticker fell back to a chip:\n%s", rendered)
	}
	if !strings.Contains(rendered, "▀") && !strings.Contains(rendered, "▄") {
		t.Errorf("no picture was drawn:\n%s", rendered)
	}
}

// Every sticker gets fetched, rather than competing with photos for the
// handful of automatic downloads.
func TestEveryStickerIsFetched(t *testing.T) {
	store := streamsStore(t)
	base := time.Date(2026, time.August, 17, 10, 0, 0, 0, time.UTC)

	var messages []db.Message
	for i := range autoDownloadLimit + 5 {
		messages = append(messages, db.Message{
			ID: fmt.Sprintf("s%d", i), ChatJID: groupJID,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Media:     db.Media{Kind: db.MediaSticker, Proto: []byte("x")},
		})
	}
	if len(messages) > stickerDownloadLimit {
		t.Fatalf("fixture needs fewer than %d stickers", stickerDownloadLimit)
	}

	cmd := autoDownload(&app{messages: store}, messages)
	if cmd == nil {
		t.Fatal("nothing was queued for download")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a batch, got %T", cmd())
	}
	if len(batch) != len(messages) {
		t.Errorf("queued %d downloads for %d stickers, want all of them", len(batch), len(messages))
	}
}

// Photos are still rationed, since they are large.
func TestPhotosAreStillRationed(t *testing.T) {
	store := streamsStore(t)
	base := time.Date(2026, time.August, 17, 11, 0, 0, 0, time.UTC)

	var messages []db.Message
	for i := range autoDownloadLimit + 5 {
		messages = append(messages, db.Message{
			ID: fmt.Sprintf("p%d", i), ChatJID: groupJID,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Media:     db.Media{Kind: db.MediaImage, Proto: []byte("x")},
		})
	}

	batch, ok := autoDownload(&app{messages: store}, messages)().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected a batch")
	}
	if len(batch) != autoDownloadLimit {
		t.Errorf("queued %d photo downloads, want %d", len(batch), autoDownloadLimit)
	}
}

// Sender JIDs written by older versions carried a device suffix, which matched
// no contact and left the message showing a raw ID.
func TestDeviceSuffixesAreStrippedFromOldRows(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContacts(map[string]string{savedPN: "Kurisu Makise"}); err != nil {
		t.Fatal(err)
	}
	// Write the row the way an older build did, suffix and all.
	if err := store.Exec(`INSERT INTO messages (id, chat_jid, sender_jid, sender, content, timestamp, from_me)
	                      VALUES ('old1', '` + groupJID + `', '31415:7@s.whatsapp.net', '', 'hello', 1786000000, 0)`); err != nil {
		t.Fatal(err)
	}

	// Migrating again is what a restart does.
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID != "old1" {
			continue
		}
		if strings.Contains(message.SenderJID, ":") {
			t.Errorf("sender jid is still %q", message.SenderJID)
		}
		if message.Sender != "Kurisu Makise" {
			t.Errorf("sender is %q, want the saved name", message.Sender)
		}
	}
}

// A name saved against the other address resolves even before mirroring runs.
func TestAliasResolvesWithoutMirroring(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveContacts(map[string]string{savedPN: "Kurisu Makise"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Exec(`INSERT INTO aliases (jid, alt) VALUES ('` + savedLID + `', '` + savedPN + `')`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(db.Message{
		ID: "alias1", ChatJID: groupJID, SenderJID: savedLID, PushName: "Christina",
		Content: "hi", IsGroup: true,
		Timestamp: time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "alias1" && message.Sender != "Kurisu Makise" {
			t.Errorf("sender is %q, want the name saved under the other address", message.Sender)
		}
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	return testPNGBytes(width, height)
}

func testPNGBytes(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 200, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// Media stored before the thumbnail column existed can have its still pulled
// back out of the protobuf we already kept.
func TestThumbnailBackfillFindsOlderMedia(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveMessage(db.Message{
		ID: "back1", ChatJID: groupJID, SenderJID: daruJID, IsGroup: true,
		Timestamp: time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC),
		Media: db.Media{
			Kind: db.MediaSticker, Proto: []byte("a stored protobuf"),
			// No Thumbnail: exactly how an older row looks.
		},
	}); err != nil {
		t.Fatal(err)
	}

	pending, err := store.MessagesMissingThumbnail(100)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, message := range pending {
		if message.ID == "back1" {
			found = true
			if len(message.Media.Proto) == 0 {
				t.Error("the stored protobuf should come back with it")
			}
		}
	}
	if !found {
		t.Fatal("the older sticker was not offered for backfill")
	}

	png := testPNG(t, 32, 32)
	if err := store.SaveThumbnail(groupJID, "back1", png); err != nil {
		t.Fatal(err)
	}

	// Once filled in it is no longer pending, and it renders.
	pending, err = store.MessagesMissingThumbnail(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range pending {
		if message.ID == "back1" {
			t.Error("still pending after the thumbnail was saved")
		}
	}

	messages, err := store.Messages(groupJID, historyLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "back1" && len(message.Media.Thumbnail) == 0 {
			t.Error("the thumbnail did not come back with the message")
		}
	}
}

// --- sending a file without a working drop -------------------------------

// Not every terminal turns a drop into a bracketed paste; some type the path
// straight into the message box. Sending it should still send the file.
func TestTypedPathIsSentAsAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reading.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	// Typed, not pasted: no PasteMsg involved at all.
	page.input = typeText(textInput{}, path)

	sent, cmd := page.submit()
	page = sent.(ConversationsPage)

	if cmd == nil {
		t.Fatal("expected a command to upload the file")
	}
	if page.input.string() != "" {
		t.Errorf("input still holds %q", page.input.string())
	}

	// Ordinary text is still a message.
	page.input = typeText(textInput{}, "just talking")
	next, cmd := page.submit()
	if cmd == nil {
		t.Fatal("expected a command to send the text")
	}
	if next.(ConversationsPage).pending != "" {
		t.Error("plain text was treated as a file")
	}
}

// The picker is the route that works whatever the terminal does.
func TestFilePickerAttachesAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pictures"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "pictures", "fern.webp")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := streamsStore(t)
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	page := openConversationsPage(&app{messages: store, width: 96, height: 30})
	page.chats = chats
	opened, _ := page.openChat(0)
	page = opened.(ConversationsPage)

	page.browseDir = dir
	started, _ := page.openBrowser()
	page = started.(ConversationsPage)
	if !page.browsing {
		t.Fatal("ctrl+u should open the picker")
	}

	for _, entry := range page.browseEntries {
		if entry.name == ".hidden" {
			t.Error("hidden files should be skipped")
		}
	}
	if page.browseEntries[0].name != ".." {
		t.Error("the parent directory should be first")
	}

	// Walk to the folder and into it.
	var found bool
	for i, entry := range page.browseEntries {
		if entry.name == "pictures" {
			page.browseCursor = i
			found = true
		}
	}
	if !found {
		t.Fatal("the folder is missing from the listing")
	}

	descended, _ := page.handleBrowseKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = descended.(ConversationsPage)
	if filepath.Base(page.browseDir) != "pictures" {
		t.Fatalf("did not descend, still in %s", page.browseDir)
	}

	for i, entry := range page.browseEntries {
		if entry.name == "fern.webp" {
			page.browseCursor = i
		}
	}
	picked, _ := page.handleBrowseKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	page = picked.(ConversationsPage)

	if page.browsing {
		t.Error("picking a file should close the picker")
	}
	if page.pending != target {
		t.Errorf("attached %q, want %q", page.pending, target)
	}

	// And escaping leaves nothing attached.
	page.pending = ""
	again, _ := page.openBrowser()
	cancelled, _ := again.(ConversationsPage).handleBrowseKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := cancelled.(ConversationsPage); got.browsing || got.pending != "" {
		t.Error("escape should close the picker without attaching")
	}
}

// Backspace climbs out of a directory.
func TestFilePickerGoesUp(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "sub")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}

	page := openConversationsPage(&app{width: 96, height: 30})
	page.browseDir = child
	started, _ := page.openBrowser()
	page = started.(ConversationsPage)

	up, _ := page.handleBrowseKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := up.(ConversationsPage).browseDir; got != dir {
		t.Errorf("went up to %q, want %q", got, dir)
	}
}

// --- names in the rail ---------------------------------------------------

// The rail used to fall straight from "no saved contact" to the raw JID, which
// meant a chat with someone unsaved showed as 132354500739299@lid.
func TestRailNamesUnsavedContacts(t *testing.T) {
	store := streamsStore(t)

	const (
		lidChat = "132354500739299@lid"
		pnChat  = "919876543210@s.whatsapp.net"
	)

	send := func(jid, pushName string, minute int) {
		t.Helper()
		if err := store.SaveMessage(db.Message{
			ID: jid + string(rune('a'+minute)), ChatJID: jid, SenderJID: jid,
			PushName: pushName, Content: "El Psy Kongroo!",
			Timestamp: time.Date(2026, time.August, 19, 12, minute, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}

	send(lidChat, "Okabe", 1)
	send(pnChat, "", 2)

	nameOf := func(jid string) string {
		t.Helper()
		chats, err := store.Chats(tabChats.kinds()...)
		if err != nil {
			t.Fatal(err)
		}
		for _, chat := range chats {
			if chat.JID == jid {
				return chat.Name
			}
		}
		t.Fatalf("chat %s missing", jid)
		return ""
	}

	// Somebody unsaved is shown by the name they chose, tilde-marked.
	if got := nameOf(lidChat); got != "~Okabe" {
		t.Errorf("LID chat is named %q, want %q", got, "~Okabe")
	}

	// With no push name either, a phone number still beats a raw JID.
	if got := nameOf(pnChat); got != "+919876543210" {
		t.Errorf("phone chat is named %q, want the number", got)
	}

	// A LID with nothing at all falls back to the paired phone number.
	if err := store.LinkJIDPairs([][2]string{{lidChat, pnChat}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContacts(map[string]string{pnChat: "Kurisu"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MirrorNamesAcrossAliases(); err != nil {
		t.Fatal(err)
	}

	// Once it is a saved contact, the saved name wins over the push name.
	if got := nameOf(lidChat); got != "Kurisu" {
		t.Errorf("LID chat is named %q, want the saved contact name", got)
	}
}

// A group keeps its own title rather than being named after a member.
func TestRailKeepsGroupTitles(t *testing.T) {
	store := streamsStore(t)

	if err := store.SaveChatName(groupJID, "Future Gadget Lab", true); err != nil {
		t.Fatal(err)
	}
	chats, err := store.Chats(tabChats.kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range chats {
		if chat.JID == groupJID && chat.Name != "Future Gadget Lab" {
			t.Errorf("group is named %q", chat.Name)
		}
	}
}
