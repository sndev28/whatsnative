package ui

import (
	"context"
	"fmt"
	"log/slog"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"whatsnative/client"
	"whatsnative/db"
)

const (
	// historyLimit is how many messages one conversation keeps in memory.
	historyLimit = 500
	// autoDownloadLimit caps how many pictures are fetched when a chat opens,
	// so opening a photo-heavy chat does not pull megabytes at once.
	autoDownloadLimit = 8
	// Stickers are small, so far more of them are fetched than photos -- but
	// still a bounded number, since tea.Batch runs them all at once and a
	// sticker-heavy chat would otherwise open fifty connections.
	stickerDownloadLimit = 30
	// The box an inline picture is drawn into, in terminal cells.
	thumbnailWidth  = 32
	thumbnailHeight = 9
	// Message text is indented by this much under its name line.
	messageIndent = 2
)

// tabKind separates the three streams WhatsApp mixes into one message feed.
type tabKind int

const (
	tabChats tabKind = iota
	tabStatus
	tabChannels
)

var tabNames = []string{"Chats", "Status", "Channels"}

// kinds is which stored chat kinds belong on a tab.
func (t tabKind) kinds() []string {
	switch t {
	case tabStatus:
		return []string{db.KindStatus}
	case tabChannels:
		return []string{db.KindNewsletter}
	default:
		return []string{db.KindChat, db.KindGroup}
	}
}

// tabSpan is where a tab's label sits on the header row, so a click can be
// turned back into a tab.
type tabSpan struct {
	tab   tabKind
	start int
	end   int
}

// tabSpans lays the labels out left to right in screen columns: one for the
// panel border, one for the leading space, then the labels two apart. Getting
// this off by one is how a click lands between the tabs and does nothing.
func tabSpans() []tabSpan {
	spans := make([]tabSpan, 0, len(tabNames))
	column := 2
	for i, name := range tabNames {
		width := len(name)
		spans = append(spans, tabSpan{tab: tabKind(i), start: column, end: column + width})
		column += width + 2
	}
	return spans
}

// groupWindow is how close together two messages from the same person have to
// be before the second one is tucked under the first without a new name.
const groupWindow = 5 * time.Minute

// doubleClickWindow is how quickly the second click has to land.
const doubleClickWindow = 500 * time.Millisecond

// paneFocus decides which pane the arrow keys drive.
type paneFocus int

const (
	focusChats paneFocus = iota
	focusMessages
)

// reactionPalette is the quick-pick list, chosen with the number keys.
var reactionPalette = []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}

// transcriptLine is one rendered row and the message it belongs to, so a mouse
// click on that row can be traced back to a message. Separators own nothing
// and carry -1.
type transcriptLine struct {
	text  string
	owner int
}

// ConversationsPage is the main screen: chats on the left, the open
// conversation on the right with its own input line underneath.
type ConversationsPage struct {
	Page
	app *app

	chats    []db.Chat
	messages []db.Message

	// openJID is the conversation on screen. The rail reorders under us --
	// sending a message lifts that chat to the top -- so the open chat is
	// tracked by identity, and cursor is only where the highlight sits.
	openJID  string
	cursor   int // highlighted row in the rail
	selected int // which message is picked out, -1 for none
	scroll   int // transcript rows scrolled up from the newest

	tab      tabKind
	focus    paneFocus
	replyTo  db.Message
	reacting bool

	// filtering routes typing to the chat search rather than the message box.
	filtering bool
	filter    textInput

	// forwarding turns the rail into a destination picker for one message.
	forwarding bool
	forwardMsg db.Message

	// pending is a file dropped onto the terminal, or picked with the browser,
	// waiting on enter.
	pending string

	// browsing is the built-in file picker, for terminals that will not turn a
	// drop into a paste.
	browsing      bool
	browseDir     string
	browseEntries []browseEntry
	browseCursor  int

	// Bubble Tea reports single clicks only, so a double click is two of them
	// on the same message inside doubleClickWindow.
	lastClickAt    time.Time
	lastClickOwner int

	input  textInput
	status string
	failed bool
}

func openConversationsPage(a *app) ConversationsPage {
	return ConversationsPage{
		Page:           Page{pageTitle: "Conversations"},
		app:            a,
		selected:       -1,
		lastClickOwner: -1,
		status:         "Loading conversations…",
	}
}

// The messages the commands below hand back to action.
type (
	chatsLoadedMsg struct {
		chats []db.Chat
		err   error
	}

	messagesLoadedMsg struct {
		chatJID  string
		messages []db.Message
		err      error
	}

	messageSentMsg struct{ err error }

	mediaReadyMsg struct {
		messageID string
		chatJID   string
		path      string
		err       error
	}

	mediaOpenedMsg struct{ err error }

	reactionSentMsg struct{ err error }

	receiptSentMsg struct{ err error }
)

// loadChats reads the conversation list in a command rather than inside
// render, so that render stays a pure function of the page's own state.
func loadChats(a *app, kinds ...string) tea.Cmd {
	return func() tea.Msg {
		chats, err := a.messages.Chats(kinds...)
		return chatsLoadedMsg{chats: chats, err: err}
	}
}

// markRead clears a chat's unread count and reloads the list, which is what
// opening a conversation should do.
//
// The receipt WhatsApp needs travels separately, in sendReceipt: clearing the
// badge is local and instant, and it should not wait on the network.
func markRead(a *app, chatJID string, kinds []string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			if err := a.messages.MarkRead(chatJID); err != nil {
				return chatsLoadedMsg{err: err}
			}
			chats, err := a.messages.Chats(kinds...)
			return chatsLoadedMsg{chats: chats, err: err}
		},
		sendReceipt(a, chatJID),
	)
}

// sendReceipt tells WhatsApp the chat has been seen, so the phone drops its
// own badge and the sender gets blue ticks.
func sendReceipt(a *app, chatJID string) tea.Cmd {
	// The layout tests drive a page against the store alone, with nothing
	// connected behind it.
	if a.session == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		return receiptSentMsg{err: a.session.MarkRead(ctx, chatJID)}
	}
}

func loadMessages(a *app, chatJID string) tea.Cmd {
	return func() tea.Msg {
		messages, err := a.messages.Messages(chatJID, historyLimit)
		return messagesLoadedMsg{chatJID: chatJID, messages: messages, err: err}
	}
}

// sendText runs the network call in a command so the UI keeps redrawing while
// WhatsApp is being talked to.
func sendText(a *app, chatJID, text string, replyTo db.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := a.session.SendText(ctx, chatJID, text, replyTo)
		return messageSentMsg{err: err}
	}
}

func sendMedia(a *app, chatJID, path, caption string, replyTo db.Message) tea.Cmd {
	return func() tea.Msg {
		// Uploads are slower than text, so they get a longer leash.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if _, err := os.Stat(path); err != nil {
			return messageSentMsg{err: fmt.Errorf("no such file: %s", path)}
		}

		_, err := a.session.SendMedia(ctx, chatJID, path, caption, replyTo)
		return messageSentMsg{err: err}
	}
}

func downloadMedia(a *app, message db.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		path, err := a.session.Download(ctx, message)
		return mediaReadyMsg{messageID: message.ID, chatJID: message.ChatJID, path: path, err: err}
	}
}

// openMedia downloads an attachment if needed, then hands it to the desktop.
func openMedia(a *app, message db.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		path, err := a.session.Download(ctx, message)
		if err != nil {
			return mediaOpenedMsg{err: err}
		}
		return mediaOpenedMsg{err: openExternally(path)}
	}
}

func revokeMessage(a *app, chatJID string, target db.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		return messageSentMsg{err: a.session.Revoke(ctx, chatJID, target)}
	}
}

func forwardMessage(a *app, toJID string, target db.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		_, err := a.session.Forward(ctx, toJID, target)
		return messageSentMsg{err: err}
	}
}

func sendReaction(a *app, chatJID string, target db.Message, emoji string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		return reactionSentMsg{err: a.session.SendReaction(ctx, chatJID, target, emoji)}
	}
}

// autoDownload fetches the newest few pictures so they can be shown inline
// without the user asking. Videos and documents are left alone: they are big,
// and a terminal cannot show them anyway.
func autoDownload(a *app, messages []db.Message) tea.Cmd {
	var commands []tea.Cmd
	photos, stickers := 0, 0

	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Media.Path != "" || len(message.Media.Proto) == 0 {
			continue
		}

		switch message.Media.Kind {
		case db.MediaSticker:
			// A sticker already draws from its embedded still, so this is only
			// about getting the full-quality file for the static ones.
			if stickers < stickerDownloadLimit {
				stickers++
				commands = append(commands, downloadMedia(a, message))
			}
		case db.MediaImage:
			if photos < autoDownloadLimit {
				photos++
				commands = append(commands, downloadMedia(a, message))
			}
		}
	}

	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}

func isPicture(media db.Media) bool {
	return media.Kind == db.MediaImage || media.Kind == db.MediaSticker
}

// visible is the chat list after the search box has had its say. Everything
// that indexes chats -- the cursor, clicks, opening -- goes through here, so
// the numbering can never disagree with what is drawn.
func (c ConversationsPage) visible() []db.Chat {
	query := strings.ToLower(strings.TrimSpace(c.filter.string()))
	if query == "" {
		return c.chats
	}

	matched := make([]db.Chat, 0, len(c.chats))
	for _, chat := range c.chats {
		haystack := strings.ToLower(plain(chat.Name) + " " + plain(chat.LastMessage))
		if strings.Contains(haystack, query) {
			matched = append(matched, chat)
		}
	}
	return matched
}

// selectedChat is the conversation being shown on the right.
//
// It is looked up by JID rather than by row, so reordering the rail cannot
// swap the open conversation for whichever chat was pushed into that slot.
func (c ConversationsPage) selectedChat() (db.Chat, bool) {
	if c.openJID != "" {
		for _, chat := range c.chats {
			if chat.JID == c.openJID {
				return chat, true
			}
		}
	}

	// Nothing opened yet: fall back to whatever the highlight is on.
	chats := c.visible()
	if c.cursor < 0 || c.cursor >= len(chats) {
		return db.Chat{}, false
	}
	return chats[c.cursor], true
}

// rowOf is where a chat sits in the rail as currently filtered, or -1.
func (c ConversationsPage) rowOf(jid string) int {
	for i, chat := range c.visible() {
		if chat.JID == jid {
			return i
		}
	}
	return -1
}

func (c ConversationsPage) selectedMessage() (db.Message, bool) {
	if c.selected < 0 || c.selected >= len(c.messages) {
		return db.Message{}, false
	}
	return c.messages[c.selected], true
}

// firstChat is the topmost chat drawn, scrolled just enough to keep the cursor
// on screen.
func (c ConversationsPage) firstChat(visible int) int {
	if c.cursor < visible {
		return 0
	}
	return c.cursor - visible + 1
}

// --- rendering -----------------------------------------------------------

func (c ConversationsPage) render() string {
	l := computeLayout(c.app.width, c.app.height, c.replyTo.ID != "")

	rail := frame(c.railLines(l), l.railInner, c.focus == focusChats)
	conversation := frame(c.conversationLines(l), l.convInner, c.focus == focusMessages)

	// Both frames are the same height, so the panes join row by row.
	rows := make([]string, 0, l.height)
	for i := range rail {
		rows = append(rows, rail[i]+conversation[i])
	}
	rows = append(rows, c.statusLine(l))

	return strings.Join(rows, "\n")
}

// railLines fills the left panel: the tab strip, then one block per chat with
// a divider between them.
func (c ConversationsPage) railLines(l layout) []string {
	if c.browsing {
		return c.browseLines(l)
	}

	width := l.railInner

	lines := []string{cell(c.tabStrip(width), width), rule(width)}

	chats := c.visible()
	if len(chats) == 0 {
		empty := "Nothing here yet"
		if c.filter.string() != "" {
			empty = "No chats match"
		}
		lines = append(lines, cell(" "+mutedStyle.Render(empty), width))
		return append(lines, blanks(l.contentRows-len(lines), width)...)
	}

	first := c.firstChat(l.visibleChats())

	for i := first; i < len(chats) && len(lines) < l.contentRows; i++ {
		name, preview := c.chatEntry(chats[i], i == c.cursor, width)
		// Every entry is the same height, blank line and divider included, so
		// a click can be turned back into a chat by simple division.
		lines = append(lines, name, preview, cell("", width), rule(width))
	}

	if len(lines) > l.contentRows {
		lines = lines[:l.contentRows]
	}
	return append(lines, blanks(l.contentRows-len(lines), width)...)
}

// tabStrip draws the three streams WhatsApp mixes together, with the open one
// highlighted -- or the search box, while one is being typed.
func (c ConversationsPage) tabStrip(width int) string {
	if c.forwarding {
		return " " + reactStyle.Render("forward to which chat?")
	}
	if c.filtering || c.filter.string() != "" {
		return " " + accentStyle.Render("find ") + c.filter.render(max(width-7, 6))
	}

	parts := make([]string, 0, len(tabNames))
	for i, name := range tabNames {
		if tabKind(i) == c.tab {
			parts = append(parts, accentStyle.Bold(true).Render(name))
			continue
		}
		parts = append(parts, mutedStyle.Render(name))
	}
	return " " + strings.Join(parts, "  ")
}

func (c ConversationsPage) chatEntry(chat db.Chat, selected bool, width int) (string, string) {
	stamp := chat.LastActive.Format("15:04")
	if time.Since(chat.LastActive) > 24*time.Hour {
		stamp = chat.LastActive.Format("02 Jan")
	}

	// The marker is always two cells: a bar for the open chat, a caret for a
	// pinned one, and both together when they coincide.
	bar, pin, style := " ", " ", titleStyle
	if selected {
		bar, style = accentStyle.Render("▌"), nameStyle
	}
	if chat.Pinned {
		pin = accentStyle.Render("^")
	}
	marker := bar + pin

	// The unread badge and the stamp both sit on the right, so the name gets
	// whatever is left.
	badge := ""
	if chat.Unread > 0 {
		badge = accentStyle.Bold(true).Render(fmt.Sprintf("(%d)", min(chat.Unread, 99))) + " "
	}
	right := badge + mutedStyle.Render(stamp) + " "
	rightWidth := measure(right)

	name := plain(chat.Name)
	nameRow := cell(marker+style.Render(truncate(name, max(width-rightWidth-2, 4))), width-rightWidth) + right

	preview := plain(oneLine(chat.LastMessage))
	previewStyle := mutedStyle
	if chat.Muted {
		previewStyle = ruleStyle
	}
	second := "  " + previewStyle.Render(truncate(preview, max(width-3, 4)))
	return cell(nameRow, width), cell(second, width)
}

// conversationLines fills the right panel: chat name, the transcript, then the
// reply bar and the input line underneath it.
func (c ConversationsPage) conversationLines(l layout) []string {
	width := l.convInner

	chat, ok := c.selectedChat()
	if !ok {
		lines := []string{
			cell(" "+mutedStyle.Render("No conversation open"), width),
			rule(width),
		}
		return append(lines, blanks(l.contentRows-len(lines), width)...)
	}

	title := " " + titleStyle.Render(truncate(plain(chat.Name), max(width-14, 8)))
	switch {
	case chat.Kind == db.KindNewsletter:
		title += mutedStyle.Render("  channel")
	case chat.Kind == db.KindStatus:
		title += mutedStyle.Render("  status")
	case chat.IsGroup:
		title += mutedStyle.Render("  group")
	}

	lines := []string{cell(title, width), rule(width)}

	// The transcript is bottom aligned so the newest message sits nearest the
	// input, the way every chat client works.
	transcript := c.transcript(width)
	start, end, blank := c.window(len(transcript), l.transcriptRows)

	lines = append(lines, blanks(blank, width)...)
	for _, line := range transcript[start:end] {
		lines = append(lines, cell(line.text, width))
	}

	lines = append(lines, rule(width))
	if l.replying {
		quote := " " + peerStyle.Render("reply to "+plain(c.replyTo.Sender)) +
			mutedStyle.Render("  "+plain(oneLine(c.replyTo.Preview())))
		lines = append(lines, cell(quote, width))
	}
	lines = append(lines, cell(" "+accentStyle.Render("›")+" "+c.input.render(max(width-3, 8)), width))

	return lines
}

// window is the slice of transcript lines currently on screen, plus how many
// blank rows pad it to the top. render and the mouse handler share it so a
// click lands on the row the user actually sees.
func (c ConversationsPage) window(total, rows int) (start, end, blank int) {
	scroll := min(c.scroll, max(total-1, 0))
	end = total - scroll
	start = max(end-rows, 0)
	return start, end, rows - (end - start)
}

// transcript renders the conversation. Consecutive messages from one person
// are tucked under a single name, which is what makes a busy group readable.
func (c ConversationsPage) transcript(width int) []transcriptLine {
	var (
		lines   []transcriptLine
		lastDay string
	)

	for index, message := range c.messages {
		day := message.Timestamp.Format("Mon 2 Jan 2006")
		newDay := day != lastDay

		showHeader := true
		if index > 0 && !newDay {
			previous := c.messages[index-1]
			sameSender := previous.SenderJID == message.SenderJID && previous.FromMe == message.FromMe
			showHeader = !(sameSender && message.Timestamp.Sub(previous.Timestamp) < groupWindow)
		}

		// Space between speakers, not between one person's run of messages.
		if index > 0 && showHeader {
			lines = append(lines, transcriptLine{text: "", owner: -1})
		}

		if newDay {
			lastDay = day
			lines = append(lines, transcriptLine{
				text:  " " + mutedStyle.Render(truncate(day, width-2)),
				owner: -1,
			})
		}

		lines = append(lines, c.messageLines(message, index, width, showHeader)...)
	}
	return lines
}

func (c ConversationsPage) messageLines(message db.Message, index, width int, showHeader bool) []transcriptLine {
	gutter := c.gutter(index)
	indent := gutter + strings.Repeat(" ", messageIndent)

	body := max(width-messageIndent-2, 12)
	if message.FromMe {
		// Room for the tick marks at the end of the last line.
		body = max(body-3, 8)
	}

	var lines []transcriptLine
	add := func(text string) {
		lines = append(lines, transcriptLine{text: text, owner: index})
	}

	if showHeader {
		style := senderStyle(message.SenderJID)
		if message.FromMe {
			style = nameStyle
		}
		add(gutter + " " + style.Render(truncate(plain(message.Sender), max(body-8, 6))) +
			mutedStyle.Render("  "+message.Timestamp.Format("15:04")))
	}

	if message.Revoked {
		add(indent + mutedStyle.Render("[message deleted]"))
		return lines
	}

	if message.Reply.MessageID != "" {
		quote := "| " + plain(message.Reply.Sender) + ": " + plain(oneLine(message.Reply.Text))
		add(indent + mutedStyle.Render(truncate(quote, body)))
	}

	picture := c.pictureRows(message, indent, body)
	chip := ""
	if message.HasMedia() && picture == nil {
		chip = mediaChip(message.Media)
		if isPicture(message.Media) && message.Media.Path != "" {
			// We have the file and still could not draw it, which is worth
			// saying rather than looking like it is still downloading.
			chip += "  no preview"
		}
	}

	text := plain(message.Content)
	if text == "" && chip != "" {
		text, chip = chip, ""
	}

	if text != "" {
		for _, line := range wrap(text, body) {
			add(indent + line)
		}
	}
	if chip != "" {
		add(indent + mutedStyle.Render(truncate(chip, body)))
	}
	// A picture drawn inline carries no chip, so the marker would be lost with
	// it -- and this is the one case where the reader most needs telling, since
	// the sender believes nobody can still see this.
	if len(picture) > 0 && message.Media.ViewOnce {
		add(indent + reactStyle.Render(truncate("[view once]", body)))
	}
	for _, row := range picture {
		add(row)
	}

	// A poll is a question and its options; the votes are encrypted per
	// recipient, so there are no tallies to show.
	if message.IsPoll() {
		add(indent + reactStyle.Render(truncate("[poll] "+plain(message.Poll.Question), body)))
		for _, option := range message.Poll.Options {
			add(indent + mutedStyle.Render(truncate("  - "+plain(option), body)))
		}
	}

	// Ticks go on the last line of our own messages.
	if message.FromMe && len(lines) > 0 {
		if tick := tickMark(message.Status); tick != "" {
			last := len(lines) - 1
			lines[last].text = cell(lines[last].text, width-4) + tick
		}
	}

	if summary := reactionSummary(message.Reactions); summary != "" {
		add(indent + summary)
	}
	return lines
}

// tickMark shows how far a message we sent has got.
func tickMark(status string) string {
	switch status {
	case db.StatusSent:
		return mutedStyle.Render(" ✓ ")
	case db.StatusDelivered:
		return mutedStyle.Render(" ✓✓")
	case db.StatusRead:
		return accentStyle.Render(" ✓✓")
	default:
		return ""
	}
}

// gutter is the left bar marking which message is picked out.
func (c ConversationsPage) gutter(index int) string {
	if index != c.selected {
		return " "
	}
	if c.focus == focusMessages {
		return accentStyle.Render("▌")
	}
	return mutedStyle.Render("▌")
}

// pictureRows draws a photo or sticker inline, or returns nil when there is
// nothing drawable: no attachment, not downloaded yet, or a format that will
// not decode (animated stickers being the usual one).
func (c ConversationsPage) pictureRows(message db.Message, indent string, body int) []string {
	if !isPicture(message.Media) {
		return nil
	}

	box := min(thumbnailWidth, body)

	var rows []string
	if message.Media.Path != "" {
		if drawn, err := renderImage(message.Media.Path, box, thumbnailHeight); err == nil {
			rows = drawn
		}
	}
	// Most stickers are animated webp, which no Go decoder will open, and a
	// picture that has not been fetched yet has nothing on disk at all. Both
	// fall back to the still WhatsApp embeds in the message itself, which
	// arrives with the message and needs no download.
	if rows == nil {
		if drawn, err := renderThumbnail(message.ID, message.Media.Thumbnail, box, thumbnailHeight); err == nil {
			rows = drawn
		}
	}
	if rows == nil {
		return nil
	}

	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, indent+row)
	}
	return lines
}

func mediaChip(media db.Media) string {
	// View-once media goes in the chip's own brackets rather than beside them,
	// so it reads as what the thing is and cannot be mistaken for a filename.
	once := ""
	if media.ViewOnce {
		once = "view once "
	}

	label := "attachment"
	switch media.Kind {
	case db.MediaImage:
		label = "[" + once + "photo]"
	case db.MediaSticker:
		label = "[sticker]"
	case db.MediaVideo:
		label = "[" + once + "video]"
	case db.MediaAudio:
		label = "[" + once + orDefault(plain(media.Name), "audio") + "]"
	case db.MediaDocument:
		label = "[doc] " + orDefault(plain(media.Name), "document")
	}

	if size := humanSize(media.Size); size != "" {
		label += "  " + size
	}
	if media.Path == "" {
		label += "  ctrl+o"
	}
	return label
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// reactionSummary collapses reactions into "+1 2  <3", keeping the order they
// first appeared in so the line does not jump around as more arrive.
func reactionSummary(reactions []db.Reaction) string {
	if len(reactions) == 0 {
		return ""
	}

	counts := map[string]int{}
	var order []string
	for _, reaction := range reactions {
		if reaction.Emoji == "" {
			continue
		}
		label := reactionLabel(reaction.Emoji)
		if counts[label] == 0 {
			order = append(order, label)
		}
		counts[label]++
	}

	parts := make([]string, 0, len(order))
	for _, label := range order {
		// Always show the count, so a single reaction is still legible as one.
		parts = append(parts, fmt.Sprintf("%s %d", label, counts[label]))
	}
	if len(parts) == 0 {
		return ""
	}
	return reactStyle.Render(strings.Join(parts, "  "))
}

func (c ConversationsPage) statusLine(l layout) string {
	if c.reacting {
		parts := make([]string, 0, len(reactionPalette))
		for i, emoji := range reactionPalette {
			parts = append(parts, fmt.Sprintf("%s %s", titleStyle.Render(fmt.Sprint(i+1)), reactionLabel(emoji)))
		}
		return cell(" "+strings.Join(parts, "   ")+mutedStyle.Render("    esc cancels"), l.width)
	}

	if c.status != "" {
		style := mutedStyle
		if c.failed {
			style = warnStyle
		}
		return cell(" "+style.Render(truncate(c.status, l.width-2)), l.width)
	}

	if c.filtering {
		return cell(" "+mutedStyle.Render("Type to search chats · enter to keep · esc to clear"), l.width)
	}

	if c.forwarding {
		return cell(" "+reactStyle.Render("Pick a chat to forward to · enter to send · esc to cancel"), l.width)
	}
	if c.pending != "" {
		return cell(" "+accentStyle.Render("Ready to send "+plain(filepath.Base(c.pending))+" · enter, or esc to drop"), l.width)
	}

	if c.browsing {
		return cell(" "+accentStyle.Render("Browsing files · enter opens · backspace up · esc cancels"), l.width)
	}

	help := "ctrl+f find · ctrl+u send file · dbl-click reply · ctrl+e react · ctrl+d delete · ctrl+w forward"
	return cell(" "+mutedStyle.Render(truncate(help, l.width-2)), l.width)
}

// note and fail set the status line; keeping the text unstyled means it can be
// measured and cut safely before any colour is applied.
func (c *ConversationsPage) note(text string) {
	c.status, c.failed = text, false
}

func (c *ConversationsPage) fail(text string) {
	c.status, c.failed = text, true
}

// --- updating ------------------------------------------------------------

func (c ConversationsPage) action(event tea.Msg) (PageInterface, tea.Cmd) {
	switch msg := event.(type) {
	case chatsLoadedMsg:
		if msg.err != nil {
			c.fail("error: " + msg.err.Error())
			return c, nil
		}
		c.chats = msg.chats
		// Follow the open conversation to wherever it has moved, rather than
		// leaving the highlight on a row that now holds a different chat.
		if row := c.rowOf(c.openJID); row >= 0 {
			c.cursor = row
		} else {
			c.cursor = min(c.cursor, max(len(c.visible())-1, 0))
		}
		c.note("")

		if chat, ok := c.selectedChat(); ok && len(c.messages) == 0 {
			c.openJID = chat.JID
			return c, loadMessages(c.app, chat.JID)
		}
		return c, nil

	case messagesLoadedMsg:
		chat, ok := c.selectedChat()
		if !ok || msg.chatJID != chat.JID {
			// A load for a chat the cursor has since moved off.
			return c, nil
		}
		if msg.err != nil {
			c.fail("error: " + msg.err.Error())
			return c, nil
		}
		c.messages = msg.messages
		if c.selected >= len(c.messages) {
			c.selected = -1
		}
		return c, autoDownload(c.app, c.messages)

	case mediaReadyMsg:
		if msg.err != nil {
			// Not worth a banner: a failed thumbnail just leaves the chip.
			return c, nil
		}
		c.attachMedia(msg.messageID, msg.chatJID, msg.path)
		return c, nil

	case mediaOpenedMsg:
		if msg.err != nil {
			c.fail("error: " + msg.err.Error())
			return c, nil
		}
		c.note("Opened in your default application")
		return c, nil

	case messageSentMsg:
		if msg.err != nil {
			c.fail("send failed: " + msg.err.Error())
			return c, nil
		}
		c.note("")
		// Answering a chat means you read it. Without this the phone keeps its
		// badge and draws its unread line above our own reply.
		if chat, ok := c.selectedChat(); ok {
			return c, tea.Batch(c.reload(), sendReceipt(c.app, chat.JID))
		}
		return c, c.reload()

	case reactionSentMsg:
		if msg.err != nil {
			c.fail("reaction failed: " + msg.err.Error())
			return c, nil
		}
		c.note("")
		return c, c.reload()

	case receiptSentMsg:
		// Not worth a banner over the conversation the user is reading: the
		// receipt is retried the next time the chat is opened.
		if msg.err != nil {
			slog.Warn("could not send read receipt", "error", msg.err)
		}
		return c, nil

	case client.NewMessage:
		chat, ok := c.selectedChat()
		if ok && msg.Message.ChatJID == chat.JID {
			c.scroll = 0
			return c, tea.Batch(
				loadMessages(c.app, chat.JID),
				markRead(c.app, chat.JID, c.tab.kinds()),
			)
		}
		return c, loadChats(c.app, c.tab.kinds()...)

	case client.ReceiptChanged:
		chat, ok := c.selectedChat()
		if ok && msg.ChatJID == chat.JID {
			return c, loadMessages(c.app, chat.JID)
		}
		return c, nil

	case client.MessageRevoked:
		chat, ok := c.selectedChat()
		if ok && msg.ChatJID == chat.JID {
			return c, loadMessages(c.app, chat.JID)
		}
		return c, loadChats(c.app, c.tab.kinds()...)

	case client.ReactionChanged:
		chat, ok := c.selectedChat()
		if ok && msg.ChatJID == chat.JID {
			return c, loadMessages(c.app, chat.JID)
		}
		return c, nil

	case client.HistorySynced:
		c.note("Synced history from phone")
		return c, c.reload()

	case client.Connected:
		c.note("Connected as " + plain(msg.PushName))
		// Reload both panes: anything drawn before the connection completed
		// was named from whatever had been synced at the time.
		return c, c.reload()

	case client.ThumbnailsReady:
		c.note(fmt.Sprintf("Recovered %d previews", msg.Count))
		return c, c.reload()

	case client.ContactsSynced:
		// Names are resolved when a chat is queried, so everything on screen
		// has to be re-read now the address book is in.
		c.note(fmt.Sprintf("Synced %d contacts", msg.Count))
		return c, c.reload()

	case client.LoggedOut:
		return openLoginPage(c.app), nil

	case client.Failure:
		c.fail("error: " + msg.Err.Error())
		return c, nil

	case tea.PasteMsg:
		return c.handlePaste(msg.Content)

	case tea.MouseClickMsg:
		return c.handleClick(tea.Mouse(msg))

	case tea.MouseWheelMsg:
		return c.handleWheel(tea.Mouse(msg))

	case tea.KeyPressMsg:
		return c.handleKey(msg)
	}

	return c, nil
}

// reload refreshes both panes from disk.
func (c ConversationsPage) reload() tea.Cmd {
	chat, ok := c.selectedChat()
	if !ok {
		return loadChats(c.app, c.tab.kinds()...)
	}
	return tea.Batch(loadChats(c.app, c.tab.kinds()...), loadMessages(c.app, chat.JID))
}

// attachMedia records a freshly downloaded file against its message, so the
// next render draws the picture instead of the chip.
func (c *ConversationsPage) attachMedia(messageID, chatJID, path string) {
	for i := range c.messages {
		if c.messages[i].ID == messageID && c.messages[i].ChatJID == chatJID {
			c.messages[i].Media.Path = path
			return
		}
	}
}

func (c ConversationsPage) handleKey(key tea.KeyPressMsg) (PageInterface, tea.Cmd) {
	// The reaction picker takes over the number keys while it is open.
	if c.reacting {
		return c.handleReactionKey(key)
	}
	// So does the search box, for everything except the keys that leave it.
	if c.filtering {
		return c.handleFilterKey(key)
	}
	// While picking a forward destination, the arrows move the rail and enter
	// commits, rather than typing into the message box.
	if c.forwarding {
		return c.handleForwardKey(key)
	}
	// Same for the file picker.
	if c.browsing {
		return c.handleBrowseKey(key)
	}

	switch key.String() {
	case "ctrl+d":
		message, ok := c.selectedMessage()
		chat, chatOK := c.selectedChat()
		if !ok || !chatOK {
			c.note("Pick a message first: tab, then arrows or click")
			return c, nil
		}
		if message.Revoked {
			return c, nil
		}
		c.note("Deleting…")
		return c, revokeMessage(c.app, chat.JID, message)

	case "ctrl+w":
		message, ok := c.selectedMessage()
		if !ok {
			c.note("Pick a message first: tab, then arrows or click")
			return c, nil
		}
		c.forwarding = true
		c.forwardMsg = message
		c.focus = focusChats
		c.note("Pick a chat to forward to, or esc to cancel")
		return c, nil

	case "ctrl+u":
		return c.openBrowser()

	case "ctrl+f":
		c.filtering = true
		c.focus = focusChats
		return c, nil

	case "ctrl+t":
		return c.selectTab(tabKind((int(c.tab) + 1) % len(tabNames)))

	case "tab":
		if c.focus == focusChats {
			c.focus = focusMessages
			if c.selected < 0 && len(c.messages) > 0 {
				c.selected = len(c.messages) - 1
			}
		} else {
			c.focus = focusChats
		}
		return c, nil

	case "esc":
		c.replyTo = db.Message{}
		c.selected = -1
		if c.pending != "" {
			c.pending = ""
			c.note("Dropped the attachment")
			return c, nil
		}
		if c.filter.string() != "" {
			// Clearing the search is the more useful meaning of esc when one
			// is in force.
			c.filter = c.filter.clear()
			c.cursor = 0
		}
		c.note("")
		return c, nil

	case "up":
		if c.focus == focusChats {
			return c.moveCursor(-1)
		}
		return c.moveSelection(-1)

	case "down":
		if c.focus == focusChats {
			return c.moveCursor(1)
		}
		return c.moveSelection(1)

	case "pgup":
		c.scroll += 5
		return c, nil
	case "pgdown":
		c.scroll = max(c.scroll-5, 0)
		return c, nil

	case "ctrl+r":
		if message, ok := c.selectedMessage(); ok {
			c.replyTo = message
		} else {
			c.note("Pick a message first: tab, then ↑↓ or click")
		}
		return c, nil

	case "ctrl+e":
		if _, ok := c.selectedMessage(); ok {
			c.reacting = true
		} else {
			c.note("Pick a message first: tab, then ↑↓ or click")
		}
		return c, nil

	case "ctrl+o":
		message, ok := c.selectedMessage()
		if !ok || !message.HasMedia() {
			c.note("Pick a message with an attachment first")
			return c, nil
		}
		c.note("Opening…")
		return c, openMedia(c.app, message)

	case "enter":
		return c.submit()
	}

	c.input = c.input.update(key)
	return c, nil
}

// handleFilterKey drives the chat search. Enter and tab leave it in place so
// the result can be navigated; esc throws it away.
func (c ConversationsPage) handleFilterKey(key tea.KeyPressMsg) (PageInterface, tea.Cmd) {
	switch key.String() {
	case "esc":
		c.filtering = false
		c.filter = c.filter.clear()
		c.cursor = 0
		return c, nil

	case "enter", "tab", "down", "up":
		c.filtering = false
		return c, nil

	case "ctrl+f":
		c.filtering = false
		return c, nil
	}

	c.filter = c.filter.update(key)
	// The list underneath just changed, so start again at the top of it.
	c.cursor = 0
	return c, nil
}

// handleForwardKey drives the destination picker.
func (c ConversationsPage) handleForwardKey(key tea.KeyPressMsg) (PageInterface, tea.Cmd) {
	switch key.String() {
	case "esc", "ctrl+w":
		c.forwarding = false
		c.forwardMsg = db.Message{}
		c.note("")
		return c, nil

	case "up":
		c.cursor = max(c.cursor-1, 0)
		return c, nil
	case "down":
		c.cursor = min(c.cursor+1, max(len(c.visible())-1, 0))
		return c, nil

	case "enter":
		return c.commitForward(c.cursor)
	}
	return c, nil
}

// commitForward sends the held message to the chat at a rail row.
func (c ConversationsPage) commitForward(index int) (PageInterface, tea.Cmd) {
	chats := c.visible()
	if index < 0 || index >= len(chats) {
		return c, nil
	}

	target := chats[index]
	message := c.forwardMsg

	c.forwarding = false
	c.forwardMsg = db.Message{}
	// Put the highlight back where the open conversation is.
	if row := c.rowOf(c.openJID); row >= 0 {
		c.cursor = row
	}
	c.note("Forwarding to " + plain(target.Name) + "…")

	return c, forwardMessage(c.app, target.JID, message)
}

func (c ConversationsPage) handleReactionKey(key tea.KeyPressMsg) (PageInterface, tea.Cmd) {
	name := key.String()
	if name == "esc" {
		c.reacting = false
		return c, nil
	}

	for i, emoji := range reactionPalette {
		if name != fmt.Sprint(i+1) {
			continue
		}
		c.reacting = false

		message, ok := c.selectedMessage()
		chat, chatOK := c.selectedChat()
		if !ok || !chatOK {
			return c, nil
		}
		c.note("Reacting " + emoji + "…")
		return c, sendReaction(c.app, chat.JID, message, emoji)
	}
	return c, nil
}

// handlePaste deals with pasted text, and with files dropped onto the window:
// terminals deliver a drop as a bracketed paste of the path.
func (c ConversationsPage) handlePaste(content string) (PageInterface, tea.Cmd) {
	if path, ok := droppedFile(content); ok {
		c.pending = path
		c.note("Attached " + filepath.Base(path) + " — enter to send, esc to drop it")
		return c, nil
	}

	// Ordinary paste: type it into whichever box has the keyboard.
	for _, r := range content {
		key := tea.KeyPressMsg{Text: string(r), Code: r}
		if c.filtering {
			c.filter = c.filter.update(key)
			continue
		}
		c.input = c.input.update(key)
	}
	return c, nil
}

// droppedFile recognises a path a terminal handed us for a dropped file.
//
// They vary: some quote it, some send a file:// URL, some escape the spaces.
// It only counts if it actually exists, so ordinary pasted text is never
// mistaken for an attachment.
func droppedFile(content string) (string, bool) {
	candidate := strings.TrimSpace(content)
	if candidate == "" || strings.ContainsAny(candidate, "\n\r") {
		return "", false
	}

	candidate = strings.Trim(candidate, "'\"")
	if url, found := strings.CutPrefix(candidate, "file://"); found {
		if unescaped, err := neturl.PathUnescape(url); err == nil {
			candidate = unescaped
		} else {
			candidate = url
		}
	}
	// A dragged path with spaces often arrives shell-escaped.
	candidate = strings.ReplaceAll(candidate, "\\ ", " ")
	candidate = expandHome(candidate)

	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", false
	}
	return candidate, true
}

// submit sends whatever is in the input: a slash command attaches a file,
// anything else is a text message.
func (c ConversationsPage) submit() (PageInterface, tea.Cmd) {
	text := strings.TrimSpace(c.input.string())
	chat, ok := c.selectedChat()
	if !ok || (text == "" && c.pending == "") {
		return c, nil
	}

	// Not every terminal turns a drop into a bracketed paste; some just type
	// the path in. If what was typed is a file that exists, treat it as one.
	if c.pending == "" {
		if path, isFile := droppedFile(text); isFile {
			c.pending = path
			c.input = c.input.clear()
			text = ""
		}
	}

	// A dropped file goes with whatever was typed as its caption.
	if c.pending != "" {
		path := c.pending
		replyTo := c.replyTo

		c.pending = ""
		c.input = c.input.clear()
		c.replyTo = db.Message{}
		c.scroll = 0
		c.note("Uploading " + filepath.Base(path) + "…")

		return c, sendMedia(c.app, chat.JID, path, text, replyTo)
	}

	replyTo := c.replyTo
	c.input = c.input.clear()
	c.replyTo = db.Message{}
	c.scroll = 0

	if path, caption, isAttachment := parseSendCommand(text); isAttachment {
		if path == "" {
			c.fail("usage: /send <path> [caption]")
			return c, nil
		}
		c.note("Uploading " + filepath.Base(path) + "…")
		return c, sendMedia(c.app, chat.JID, path, caption, replyTo)
	}

	c.note("Sending…")
	return c, sendText(c.app, chat.JID, text, replyTo)
}

// parseSendCommand recognises the slash commands that attach a file. The kind
// of message to send is decided from the file's extension, so the aliases all
// behave the same; they exist because they are easier to remember.
func parseSendCommand(text string) (path, caption string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}

	fields := strings.Fields(text)
	switch fields[0] {
	case "/send", "/file", "/img", "/photo", "/video", "/doc", "/sticker":
	default:
		return "", "", false
	}

	if len(fields) < 2 {
		return "", "", true
	}
	return expandHome(fields[1]), strings.Join(fields[2:], " "), true
}

// expandHome turns a leading ~ into the user's home directory, which a shell
// would have done before the program ever saw the path.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// selectTab switches stream and starts again at the top of that list.
func (c ConversationsPage) selectTab(next tabKind) (PageInterface, tea.Cmd) {
	if next == c.tab {
		return c, nil
	}

	c.tab = next
	c.cursor = 0
	c.openJID = ""
	c.scroll = 0
	c.selected = -1
	c.replyTo = db.Message{}
	c.chats = nil
	c.messages = nil

	return c, loadChats(c.app, next.kinds()...)
}

// moveCursor changes the open chat and loads its messages.
func (c ConversationsPage) moveCursor(delta int) (PageInterface, tea.Cmd) {
	next := min(max(c.cursor+delta, 0), max(len(c.visible())-1, 0))
	if next == c.cursor {
		return c, nil
	}
	return c.openChat(next)
}

func (c ConversationsPage) openChat(index int) (PageInterface, tea.Cmd) {
	chats := c.visible()
	if index < 0 || index >= len(chats) {
		return c, nil
	}

	c.cursor = index
	c.openJID = chats[index].JID
	c.scroll = 0
	c.selected = -1
	c.replyTo = db.Message{}
	// Blank the old chat's messages so the previous transcript is not shown
	// under the new title while the load is in flight.
	c.messages = nil

	jid := chats[index].JID
	return c, tea.Batch(loadMessages(c.app, jid), markRead(c.app, jid, c.tab.kinds()))
}

func (c ConversationsPage) moveSelection(delta int) (PageInterface, tea.Cmd) {
	if len(c.messages) == 0 {
		return c, nil
	}

	if c.selected < 0 {
		c.selected = len(c.messages) - 1
		return c, nil
	}
	c.selected = min(max(c.selected+delta, 0), len(c.messages)-1)
	return c, nil
}

func (c ConversationsPage) handleClick(mouse tea.Mouse) (PageInterface, tea.Cmd) {
	if mouse.Button != tea.MouseLeft {
		return c, nil
	}
	l := computeLayout(c.app.width, c.app.height, c.replyTo.ID != "")

	// The tab strip is the rail's first row. Without this, the Status and
	// Channels tabs could only be reached with ctrl+t.
	if mouse.Y == l.tabRow && l.inRail(mouse.X) {
		for _, span := range tabSpans() {
			if mouse.X >= span.start && mouse.X < span.end {
				return c.selectTab(span.tab)
			}
		}
		return c, nil
	}

	if l.inRail(mouse.X) {
		index, ok := l.chatAtRow(mouse.Y, c.firstChat(l.visibleChats()))
		if !ok || index >= len(c.visible()) {
			return c, nil
		}
		if c.forwarding {
			return c.commitForward(index)
		}

		c.focus = focusChats
		if index == c.cursor {
			return c, nil
		}
		return c.openChat(index)
	}

	owner, ok := c.messageAtRow(l, mouse.Y)
	if !ok {
		return c, nil
	}

	c.focus = focusMessages
	c.selected = owner

	// A second click on the same message starts a reply to it, which saves
	// reaching for ctrl+r.
	now := time.Now()
	if owner == c.lastClickOwner && now.Sub(c.lastClickAt) < doubleClickWindow {
		if message, ok := c.selectedMessage(); ok {
			c.replyTo = message
		}
		// Reset, so a third click does not count as another double.
		c.lastClickAt = time.Time{}
		c.lastClickOwner = -1
		return c, nil
	}

	c.lastClickAt = now
	c.lastClickOwner = owner
	return c, nil
}

func (c ConversationsPage) handleWheel(mouse tea.Mouse) (PageInterface, tea.Cmd) {
	l := computeLayout(c.app.width, c.app.height, c.replyTo.ID != "")

	// Scrolling over the rail moves through chats; over the transcript it
	// scrolls the messages.
	if l.inRail(mouse.X) {
		switch mouse.Button {
		case tea.MouseWheelUp:
			return c.moveCursor(-1)
		case tea.MouseWheelDown:
			return c.moveCursor(1)
		}
		return c, nil
	}

	switch mouse.Button {
	case tea.MouseWheelUp:
		c.scroll += 3
	case tea.MouseWheelDown:
		c.scroll = max(c.scroll-3, 0)
	}
	return c, nil
}

// messageAtRow maps a screen row back to the message drawn on it, repeating
// the windowing render used so the two cannot disagree.
func (c ConversationsPage) messageAtRow(l layout, screenY int) (int, bool) {
	if screenY < l.transcriptTop || screenY >= l.transcriptTop+l.transcriptRows {
		return 0, false
	}

	transcript := c.transcript(l.convInner)
	start, end, blank := c.window(len(transcript), l.transcriptRows)

	offset := screenY - l.transcriptTop - blank
	if offset < 0 || start+offset >= end {
		return 0, false
	}

	owner := transcript[start+offset].owner
	if owner < 0 {
		return 0, false
	}
	return owner, true
}
