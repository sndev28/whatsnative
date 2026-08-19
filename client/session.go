package client

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"whatsnative/db"
)

// The events the UI reacts to.
//
// whatsmeow's own event types carry a lot of protocol detail the UI has no use
// for, so the session translates the handful that matter into these and sends
// them down one channel.
type (
	// QRCode is a new login code to draw. WhatsApp rotates it every 20s or so.
	QRCode struct{ Code string }

	// Connected means the socket is up and the session is usable.
	Connected struct{ PushName string }

	// LoggedOut means the phone unlinked this device; the session is dead.
	LoggedOut struct{}

	// NewMessage is a message that has just been written to the store.
	NewMessage struct{ Message db.Message }

	// ReactionChanged means somebody reacted to a message, or took it back.
	ReactionChanged struct{ ChatJID string }

	// ReceiptChanged means a message we sent was delivered or read.
	ReceiptChanged struct{ ChatJID string }

	// MessageRevoked means a message was deleted for everyone.
	MessageRevoked struct{ ChatJID string }

	// ThumbnailsReady means stills were recovered for older media messages.
	ThumbnailsReady struct{ Count int }

	// ContactsSynced means the address book has been read and names resolved.
	// The UI reloads on it, because anything drawn before is missing names.
	ContactsSynced struct{ Count int }

	// HistorySynced means the phone sent a backlog and stored chats changed.
	HistorySynced struct{}

	// Failure is a non-fatal error worth showing in the status line.
	Failure struct{ Err error }
)

// Session owns the whatsmeow client and turns its callbacks into a stream the
// UI can consume.
type Session struct {
	WA *whatsmeow.Client

	ctx      context.Context
	messages *db.MessageStore
	mediaDir string

	// logCloser outlives CreateClient on purpose: the client logs for as long
	// as it is connected, so the file cannot be closed until we shut down.
	logCloser io.Closer

	events chan any
	done   chan struct{}
	// closeOnce makes Close safe to call more than once, which matters when
	// both a defer and an error path reach for it.
	closeOnce sync.Once
}

// Events is the stream StartUI forwards into Bubble Tea.
func (s *Session) Events() <-chan any { return s.events }

// HasSession reports whether a previous login is stored, which decides whether
// the UI opens on the QR page or straight into the conversations.
func (s *Session) HasSession() bool { return s.WA.Store.ID != nil }

// Start connects, asking for a QR code first when there is no stored login.
func (s *Session) Start() error {
	if !s.HasSession() {
		// GetQRChannel must be called before Connect, otherwise the pairing
		// codes have nowhere to go.
		codes, err := s.WA.GetQRChannel(s.ctx)
		if err != nil {
			return fmt.Errorf("get qr channel: %w", err)
		}
		go s.forwardQR(codes)
	}

	if err := s.WA.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

// Close disconnects, releases anything blocked in emit, and closes the log.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.WA.Disconnect()
		if s.logCloser != nil {
			s.logCloser.Close()
		}
	})
}

// SendText delivers a message, quoting replyTo when it has an ID.
func (s *Session) SendText(ctx context.Context, chatJID, text string, replyTo db.Message) (db.Message, error) {
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return db.Message{}, fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	// A plain Conversation cannot carry a ContextInfo, so a reply has to go
	// out as an ExtendedTextMessage instead.
	outgoing := &waE2E.Message{Conversation: proto.String(text)}
	if quote := buildContextInfo(replyTo); quote != nil {
		outgoing = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        proto.String(text),
				ContextInfo: quote,
			},
		}
	}

	resp, err := s.WA.SendMessage(ctx, jid, outgoing)
	if err != nil {
		return db.Message{}, fmt.Errorf("send message: %w", err)
	}

	return s.storeOutgoing(jid, resp, text, db.Media{}, replyTo)
}

// SendReaction adds or replaces our reaction on a message. An empty emoji
// removes it, which is how WhatsApp models "unreact".
func (s *Session) SendReaction(ctx context.Context, chatJID string, target db.Message, emoji string) error {
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat jid %q: %w", chatJID, err)
	}

	// For our own messages the sender is us; for anyone else's it is whoever
	// sent it, which is what BuildReaction keys the reaction on.
	sender := chat
	if target.SenderJID != "" {
		if parsed, err := types.ParseJID(target.SenderJID); err == nil {
			sender = parsed
		}
	}

	reaction := s.WA.BuildReaction(chat, sender, types.MessageID(target.ID), emoji)
	if _, err := s.WA.SendMessage(ctx, chat, reaction); err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}

	own := ""
	if id := s.WA.Store.ID; id != nil {
		own = id.String()
	}
	return s.messages.SaveReaction(chatJID, db.Reaction{
		MessageID: target.ID,
		SenderJID: own,
		Sender:    "You",
		Emoji:     emoji,
		Timestamp: time.Now(),
	})
}

// Revoke deletes a message for everyone.
//
// WhatsApp only lets you revoke your own messages (and, in a group, anyone's
// if you are an admin), so the server may refuse; the local copy is only
// marked once it has accepted.
func (s *Session) Revoke(ctx context.Context, chatJID string, message db.Message) error {
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat jid %q: %w", chatJID, err)
	}

	sender := chat
	if message.SenderJID != "" {
		if parsed, err := types.ParseJID(message.SenderJID); err == nil {
			sender = parsed
		}
	}

	revoke := s.WA.BuildRevoke(chat, sender, types.MessageID(message.ID))
	if _, err := s.WA.SendMessage(ctx, chat, revoke); err != nil {
		return fmt.Errorf("revoke message: %w", err)
	}
	return s.messages.MarkRevoked(chatJID, message.ID)
}

// Forward sends an existing message on to another chat.
//
// Media is re-sent from the protobuf we stored rather than downloaded and
// uploaded again: the recipient can fetch it from WhatsApp's servers with the
// same keys, which is both faster and what the official clients do.
func (s *Session) Forward(ctx context.Context, toJID string, message db.Message) (db.Message, error) {
	jid, err := types.ParseJID(toJID)
	if err != nil {
		return db.Message{}, fmt.Errorf("parse jid %q: %w", toJID, err)
	}

	outgoing, err := forwardable(message)
	if err != nil {
		return db.Message{}, err
	}

	resp, err := s.WA.SendMessage(ctx, jid, outgoing)
	if err != nil {
		return db.Message{}, fmt.Errorf("forward message: %w", err)
	}

	media := message.Media
	if media.Kind != db.MediaNone {
		media.Proto = marshal(outgoing)
	}
	return s.storeOutgoing(jid, resp, message.Content, media, db.Message{})
}

// SendMedia uploads a file and sends it as the right kind of message for its
// type: photo, video, audio, sticker or document.
func (s *Session) SendMedia(ctx context.Context, chatJID, path, caption string, replyTo db.Message) (db.Message, error) {
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return db.Message{}, fmt.Errorf("parse jid %q: %w", chatJID, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return db.Message{}, fmt.Errorf("read %s: %w", path, err)
	}

	kind, mimeType := classify(path)
	upload, err := s.WA.Upload(ctx, data, uploadTypeFor(kind))
	if err != nil {
		return db.Message{}, fmt.Errorf("upload %s: %w", filepath.Base(path), err)
	}

	outgoing := buildMediaMessage(kind, mimeType, filepath.Base(path), caption, data, upload, buildContextInfo(replyTo))

	resp, err := s.WA.SendMessage(ctx, jid, outgoing)
	if err != nil {
		return db.Message{}, fmt.Errorf("send %s: %w", kind, err)
	}

	media := db.Media{
		Kind:  kind,
		Mime:  mimeType,
		Name:  filepath.Base(path),
		Size:  int64(len(data)),
		Proto: marshal(outgoing),
		// Already on disk, so the UI can preview it without downloading.
		Path: path,
	}
	return s.storeOutgoing(jid, resp, caption, media, replyTo)
}

// storeOutgoing writes a message we just sent, so it appears immediately
// rather than when the server echoes it back.
func (s *Session) storeOutgoing(jid types.JID, resp whatsmeow.SendResponse, text string, media db.Media, replyTo db.Message) (db.Message, error) {
	message := db.Message{
		ID:        string(resp.ID),
		ChatJID:   jid.String(),
		Sender:    "You",
		Content:   text,
		Timestamp: resp.Timestamp,
		FromMe:    true,
		IsGroup:   jid.Server == types.GroupServer,
		Status:    db.StatusSent,
		Media:     media,
	}
	if own := s.WA.Store.ID; own != nil {
		message.SenderJID = own.ToNonAD().String()
	}
	if replyTo.ID != "" {
		message.Reply = db.Reply{
			MessageID: replyTo.ID,
			Sender:    replyTo.Sender,
			Text:      replyTo.Preview(),
		}
	}

	if err := s.messages.SaveMessage(message); err != nil {
		return message, fmt.Errorf("store sent message: %w", err)
	}
	return message, nil
}

// Download fetches an attachment's bytes and caches them next to the database,
// returning the path. Already-downloaded media is returned as-is.
func (s *Session) Download(ctx context.Context, message db.Message) (string, error) {
	if message.Media.Path != "" {
		if _, err := os.Stat(message.Media.Path); err == nil {
			return message.Media.Path, nil
		}
	}
	if len(message.Media.Proto) == 0 {
		return "", fmt.Errorf("no stored attachment for message %s", message.ID)
	}

	var original waE2E.Message
	if err := proto.Unmarshal(message.Media.Proto, &original); err != nil {
		return "", fmt.Errorf("decode stored attachment: %w", err)
	}

	data, err := s.WA.DownloadAny(ctx, &original)
	if err != nil {
		return "", fmt.Errorf("download attachment: %w", err)
	}

	if err := os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return "", fmt.Errorf("create media dir: %w", err)
	}

	path := filepath.Join(s.mediaDir, downloadName(message))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write attachment: %w", err)
	}

	if err := s.messages.SaveMediaPath(message.ID, message.ChatJID, path); err != nil {
		return path, err
	}
	return path, nil
}

// downloadName builds a filename that is unique per message and keeps a
// sensible extension so external viewers know what to do with it.
func downloadName(message db.Message) string {
	if message.Media.Name != "" && filepath.Ext(message.Media.Name) != "" {
		return message.ID + "-" + filepath.Base(message.Media.Name)
	}

	extension := ".bin"
	if extensions, err := mime.ExtensionsByType(message.Media.Mime); err == nil && len(extensions) > 0 {
		extension = extensions[0]
	}
	return message.ID + extension
}

// emit hands an event to the UI.
//
// The select on done means a whatsmeow callback can never block forever on a
// channel nobody is reading, which is what would happen if the UI quit first.
func (s *Session) emit(event any) {
	select {
	case s.events <- event:
	case <-s.done:
	}
}

func (s *Session) forwardQR(codes <-chan whatsmeow.QRChannelItem) {
	for item := range codes {
		switch item.Event {
		case "code":
			s.emit(QRCode{Code: item.Code})
		case "error":
			s.emit(Failure{Err: item.Error})
		}
	}
}

// handleEvent runs on whatsmeow's own goroutines, so it only touches the store
// (which locks) and the event channel (which is a channel). It must never
// reach into the UI directly.
func (s *Session) handleEvent(rawEvent any) {
	switch event := rawEvent.(type) {
	case *events.Connected:
		s.onConnected()
	case *events.Message:
		s.onMessage(event)
	case *events.HistorySync:
		s.onHistorySync(event)
	case *events.Contact:
		s.onContact(event)
	case *events.Receipt:
		s.onReceipt(event)
	case *events.LoggedOut:
		s.emit(LoggedOut{})
	}
}

// onContact records a saved name as the contact list syncs, so names appear
// without waiting for the next connection.
func (s *Session) onContact(event *events.Contact) {
	name := event.Action.GetFullName()
	if name == "" {
		name = event.Action.GetFirstName()
	}
	if name == "" {
		return
	}

	if err := s.messages.SaveContact(event.JID.ToNonAD().String(), name); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	if err := s.messages.MirrorNamesAcrossAliases(); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	s.emit(ContactsSynced{Count: 1})
}

// onReceipt turns a delivery or read receipt into tick marks.
func (s *Session) onReceipt(event *events.Receipt) {
	var status string
	switch event.Type {
	case types.ReceiptTypeDelivered:
		status = db.StatusDelivered
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		status = db.StatusRead
	default:
		return
	}

	ids := make([]string, 0, len(event.MessageIDs))
	for _, id := range event.MessageIDs {
		ids = append(ids, string(id))
	}

	chatJID := event.Chat.String()
	if err := s.messages.SetMessageStatus(chatJID, ids, status); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	s.emit(ReceiptChanged{ChatJID: chatJID})
}

// storeConversationMeta records the name, pin and unread count the phone sends
// with a chat's history, which is the only place we learn them for a chat we
// have never had open.
func (s *Session) storeConversationMeta(chatJID types.JID, conversation *waHistorySync.Conversation) {
	kind := kindForJID(chatJID)

	name := conversation.GetName()
	if name == "" {
		name = conversation.GetDisplayName()
	}
	if name != "" {
		if err := s.messages.SaveChatName(chatJID.String(), name, kind == db.KindGroup); err != nil {
			s.emit(Failure{Err: err})
		}
	}

	muted := conversation.GetMuteEndTime() > uint64(time.Now().Unix())
	if err := s.messages.SetChatMeta(
		chatJID.String(), kind,
		conversation.GetPinned() > 0, muted, conversation.GetArchived(),
	); err != nil {
		s.emit(Failure{Err: err})
	}

	if unread := conversation.GetUnreadCount(); unread > 0 {
		if err := s.messages.SetUnread(chatJID.String(), int(unread)); err != nil {
			s.emit(Failure{Err: err})
		}
	}
}

// kindForJID decides which tab a chat belongs on.
func kindForJID(jid types.JID) string {
	switch {
	case jid.Server == types.NewsletterServer:
		return db.KindNewsletter
	case jid.Server == types.BroadcastServer && jid.User == "status":
		return db.KindStatus
	case jid.Server == types.GroupServer:
		return db.KindGroup
	default:
		return db.KindChat
	}
}

func (s *Session) onConnected() {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	s.syncAddressBook(ctx)
	s.refreshContacts(ctx)
	s.refreshGroups(ctx)
	s.refreshNewsletters(ctx)
	s.refreshChatSettings(ctx)
	s.backfillThumbnails()
	s.emit(Connected{PushName: s.WA.Store.PushName})
}

// syncAddressBook makes whatsmeow fetch the contact list.
//
// Saved names do not arrive with the message history: they come as an app
// state patch, and nothing pulls it on an already-paired session. Without this
// the only name we ever have for somebody is the one they chose themselves.
func (s *Session) syncAddressBook(ctx context.Context) {
	// If we hold no saved names at all, the patch has never landed, so ask for
	// the whole thing rather than the incremental update.
	named, err := s.messages.CountNamedContacts()
	if err != nil {
		s.emit(Failure{Err: err})
		return
	}

	fullSync := named == 0 || os.Getenv("WHATSNATIVE_RESYNC") != ""
	if err := s.WA.FetchAppState(ctx, appstate.WAPatchCriticalUnblockLow, fullSync, !fullSync); err != nil {
		// Not fatal: we fall back to profile names, marked with a tilde.
		s.emit(Failure{Err: fmt.Errorf("sync contact list: %w", err)})
	}
}

func (s *Session) refreshContacts(ctx context.Context) {
	contacts, err := s.WA.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		s.emit(Failure{Err: fmt.Errorf("load contacts: %w", err)})
		return
	}

	// Collect first, write once. Doing this a contact at a time took long
	// enough on a real account that the UI drew half-named chats while it ran.
	names := make(map[string]string, len(contacts))
	for jid, info := range contacts {
		if name := contactName(info); name != "" {
			names[jid.ToNonAD().String()] = name
		}
	}

	if err := s.messages.SaveContacts(names); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	// whatsmeow keeps its LID/phone-number pairs in the same database, so the
	// whole mapping comes across in one statement rather than one lookup per
	// contact.
	if err := s.messages.ImportLIDMap(); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	if err := s.messages.MirrorNamesAcrossAliases(); err != nil {
		s.emit(Failure{Err: err})
		return
	}

	s.emit(ContactsSynced{Count: len(names)})
}

// refreshGroups names the groups we are a member of. A group has a title of
// its own rather than a contact to look up.
func (s *Session) refreshGroups(ctx context.Context) {
	groups, err := s.WA.GetJoinedGroups(ctx)
	if err != nil {
		s.emit(Failure{Err: fmt.Errorf("load groups: %w", err)})
		return
	}

	// Every participant comes with both of their addresses. This is the only
	// place we learn the pairing for someone we have never messaged directly,
	// and without it group members show up as raw LIDs.
	var pairs [][2]string

	for _, group := range groups {
		// group.Name is promoted from the embedded types.GroupName struct.
		if err := s.messages.SaveChatName(group.JID.String(), group.Name, true); err != nil {
			s.emit(Failure{Err: err})
			return
		}

		for _, participant := range group.Participants {
			if participant.LID.IsEmpty() || participant.PhoneNumber.IsEmpty() {
				continue
			}
			pairs = append(pairs, [2]string{
				participant.LID.ToNonAD().String(),
				participant.PhoneNumber.ToNonAD().String(),
			})
		}
	}

	if err := s.messages.LinkJIDPairs(pairs); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	// Those pairings may name people the address book only knew by number.
	if err := s.messages.MirrorNamesAcrossAliases(); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	s.emit(ContactsSynced{Count: len(pairs)})
}

// refreshNewsletters names the channels we subscribe to and files them under
// their own kind, so they stop appearing among ordinary conversations.
func (s *Session) refreshNewsletters(ctx context.Context) {
	// Status updates are a chat like any other as far as the protocol is
	// concerned, so they need naming by hand.
	if err := s.messages.SaveChatName(types.StatusBroadcastJID.String(), "Status Updates", false); err != nil {
		s.emit(Failure{Err: err})
	}
	if err := s.messages.SetChatMeta(types.StatusBroadcastJID.String(), db.KindStatus, false, false, false); err != nil {
		s.emit(Failure{Err: err})
	}

	newsletters, err := s.WA.GetSubscribedNewsletters(ctx)
	if err != nil {
		// Not fatal: an account with no channels, or an older server, simply
		// leaves them unnamed.
		return
	}

	for _, newsletter := range newsletters {
		jid := newsletter.ID.String()
		if name := newsletter.ThreadMeta.Name.Text; name != "" {
			if err := s.messages.SaveChatName(jid, name, false); err != nil {
				s.emit(Failure{Err: err})
				return
			}
		}
		if err := s.messages.SetChatMeta(jid, db.KindNewsletter, false, false, false); err != nil {
			s.emit(Failure{Err: err})
			return
		}
	}
}

// backfillThumbnails recovers the still picture for media stored before we
// started keeping it.
//
// This is what makes older stickers appear: their webp is animated, which no
// Go decoder will open, but the protobuf we saved carries a PNG still.
func (s *Session) backfillThumbnails() {
	messages, err := s.messages.MessagesMissingThumbnail(2000)
	if err != nil {
		s.emit(Failure{Err: err})
		return
	}

	recovered := 0
	for _, message := range messages {
		var original waE2E.Message
		if err := proto.Unmarshal(message.Media.Proto, &original); err != nil {
			continue
		}

		thumbnail := describe(&original).Media.Thumbnail
		if len(thumbnail) == 0 {
			continue
		}
		if err := s.messages.SaveThumbnail(message.ChatJID, message.ID, thumbnail); err != nil {
			s.emit(Failure{Err: err})
			return
		}
		recovered++
	}

	if recovered > 0 {
		s.emit(ThumbnailsReady{Count: recovered})
	}
}

// refreshChatSettings reads the pin, mute and archive flags WhatsApp keeps for
// each chat we already know about.
func (s *Session) refreshChatSettings(ctx context.Context) {
	chats, err := s.messages.Chats(db.KindChat, db.KindGroup, db.KindStatus, db.KindNewsletter)
	if err != nil {
		s.emit(Failure{Err: err})
		return
	}

	for _, chat := range chats {
		jid, err := types.ParseJID(chat.JID)
		if err != nil {
			continue
		}
		settings, err := s.WA.Store.ChatSettings.GetChatSettings(ctx, jid)
		if err != nil || !settings.Found {
			continue
		}
		if err := s.messages.SetChatMeta(
			chat.JID, kindForJID(jid),
			settings.Pinned, settings.MutedUntil.After(time.Now()), settings.Archived,
		); err != nil {
			s.emit(Failure{Err: err})
			return
		}
	}
}

func (s *Session) onMessage(event *events.Message) {
	// A reaction arrives as an ordinary message whose payload points at the
	// message being reacted to, so it has to be peeled off before the normal
	// path treats it as something to display.
	if reaction := event.Message.GetReactionMessage(); reaction != nil {
		s.onReaction(event, reaction)
		return
	}

	// A revoke arrives as an ordinary message whose payload names the message
	// being deleted, so it has to be peeled off before the normal path treats
	// it as something to display.
	if protocol, ok := revokedBy(event.Message); ok {
		s.onRevoke(event, protocol)
		return
	}

	// Every message carries both addresses for its sender, and for a one-to-one
	// chat both addresses for the other party. That is the cheapest place to
	// learn the pairing, and it works even before contacts have been synced.
	s.linkFromMessage(event)

	described := describe(event.Message)
	if described.empty() {
		// Receipts, protocol messages: nothing to show.
		return
	}

	message := db.Message{
		ID:        string(event.Info.ID),
		ChatJID:   event.Info.Chat.String(),
		SenderJID: event.Info.Sender.ToNonAD().String(),
		Sender:    senderName(event.Info.IsFromMe, event.Info.PushName, event.Info.Sender),
		PushName:  event.Info.PushName,
		Content:   described.Text,
		Timestamp: event.Info.Timestamp,
		FromMe:    event.Info.IsFromMe,
		IsGroup:   event.Info.IsGroup,
		Media:     described.Media,
		Reply:     described.Reply,
		Poll:      described.Poll,
	}

	if err := s.messages.SaveMessage(message); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	if !message.FromMe {
		// The UI clears this again as soon as the chat is on screen.
		if err := s.messages.BumpUnread(message.ChatJID); err != nil {
			s.emit(Failure{Err: err})
		}
	}
	s.emit(NewMessage{Message: message})
}

// linkFromMessage records the LID/phone-number pairs a message reveals.
func (s *Session) linkFromMessage(event *events.Message) {
	if alt := event.Info.SenderAlt; !alt.IsEmpty() {
		if err := s.messages.LinkJIDs(
			event.Info.Sender.ToNonAD().String(), alt.ToNonAD().String(),
		); err != nil {
			s.emit(Failure{Err: err})
		}
	}

	if alt := event.Info.RecipientAlt; !alt.IsEmpty() && !event.Info.IsGroup {
		if err := s.messages.LinkJIDs(
			event.Info.Chat.ToNonAD().String(), alt.ToNonAD().String(),
		); err != nil {
			s.emit(Failure{Err: err})
		}
	}
}

func (s *Session) onRevoke(event *events.Message, protocol *waE2E.ProtocolMessage) {
	id := protocol.GetKey().GetID()
	if id == "" {
		return
	}

	chatJID := event.Info.Chat.String()
	if err := s.messages.MarkRevoked(chatJID, id); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	s.emit(MessageRevoked{ChatJID: chatJID})
}

func (s *Session) onReaction(event *events.Message, reaction *waE2E.ReactionMessage) {
	chatJID := event.Info.Chat.String()

	stored := db.Reaction{
		MessageID: reaction.GetKey().GetID(),
		SenderJID: event.Info.Sender.ToNonAD().String(),
		Sender:    senderName(event.Info.IsFromMe, event.Info.PushName, event.Info.Sender),
		Emoji:     reaction.GetText(),
		Timestamp: event.Info.Timestamp,
	}

	if err := s.messages.SaveReaction(chatJID, stored); err != nil {
		s.emit(Failure{Err: err})
		return
	}
	s.emit(ReactionChanged{ChatJID: chatJID})
}

// onHistorySync ingests the backlog the phone pushes after linking. Without
// it, a fresh login shows an empty list until somebody messages you.
func (s *Session) onHistorySync(event *events.HistorySync) {
	var stored int

	for _, conversation := range event.Data.GetConversations() {
		chatJID, err := types.ParseJID(conversation.GetID())
		if err != nil {
			continue
		}
		isGroup := chatJID.Server == types.GroupServer
		s.storeConversationMeta(chatJID, conversation)

		for _, historyMessage := range conversation.GetMessages() {
			webMessage := historyMessage.GetMessage()
			described := describe(webMessage.GetMessage())
			if described.empty() {
				continue
			}

			key := webMessage.GetKey()
			// In a group the sender is the key's participant; in a one-to-one
			// chat the only other party is the chat itself.
			sender := chatJID
			if participant, err := types.ParseJID(key.GetParticipant()); err == nil && !participant.IsEmpty() {
				sender = participant
			}

			message := db.Message{
				ID:        key.GetID(),
				ChatJID:   chatJID.String(),
				SenderJID: sender.ToNonAD().String(),
				Sender:    senderName(key.GetFromMe(), webMessage.GetPushName(), sender),
				PushName:  webMessage.GetPushName(),
				Content:   described.Text,
				Timestamp: time.Unix(int64(webMessage.GetMessageTimestamp()), 0),
				FromMe:    key.GetFromMe(),
				IsGroup:   isGroup,
				Media:     described.Media,
				Reply:     described.Reply,
				Poll:      described.Poll,
			}
			if err := s.messages.SaveMessage(message); err != nil {
				s.emit(Failure{Err: err})
				return
			}
			stored++
		}
	}

	if stored > 0 {
		s.emit(HistorySynced{})
	}
}

// senderName picks the best label we have for whoever sent a message.
func senderName(fromMe bool, pushName string, sender types.JID) string {
	switch {
	case fromMe:
		return "You"
	case pushName != "":
		return pushName
	case sender.User != "":
		return sender.User
	default:
		return "unknown"
	}
}

// contactName is the name *you* saved for someone.
//
// PushName is deliberately not in this list. It is the name the other person
// chose for their own profile, and treating it as an address-book entry both
// hides the tilde that marks an unsaved number and makes a missing contact
// list look like a working one.
func contactName(info types.ContactInfo) string {
	for _, candidate := range []string{info.FullName, info.FirstName, info.BusinessName} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// classify decides what kind of message a file should be sent as, from its
// extension. Stickers are the one case the extension alone decides: WhatsApp
// only accepts webp for them.
func classify(path string) (kind, mimeType string) {
	extension := strings.ToLower(filepath.Ext(path))
	mimeType = mime.TypeByExtension(extension)

	switch extension {
	case ".webp":
		return db.MediaSticker, "image/webp"
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp":
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return db.MediaImage, mimeType
	case ".mp4", ".mkv", ".mov", ".webm", ".avi":
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		return db.MediaVideo, mimeType
	case ".mp3", ".ogg", ".opus", ".m4a", ".wav", ".flac":
		if mimeType == "" {
			mimeType = "audio/ogg"
		}
		return db.MediaAudio, mimeType
	}

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return db.MediaDocument, mimeType
}

func uploadTypeFor(kind string) whatsmeow.MediaType {
	switch kind {
	case db.MediaImage, db.MediaSticker:
		// Stickers travel on the image key type.
		return whatsmeow.MediaImage
	case db.MediaVideo:
		return whatsmeow.MediaVideo
	case db.MediaAudio:
		return whatsmeow.MediaAudio
	default:
		return whatsmeow.MediaDocument
	}
}

// buildMediaMessage assembles the protobuf for an upload. Every media message
// repeats the same handful of fields from the upload response, because each
// kind has its own struct rather than a shared one.
func buildMediaMessage(
	kind, mimeType, name, caption string,
	data []byte,
	upload whatsmeow.UploadResponse,
	quote *waE2E.ContextInfo,
) *waE2E.Message {
	length := uint64(len(data))

	switch kind {
	case db.MediaImage:
		width, height := imageSize(data)
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(length),
			Width:         proto.Uint32(uint32(width)),
			Height:        proto.Uint32(uint32(height)),
			ContextInfo:   quote,
		}}

	case db.MediaSticker:
		width, height := imageSize(data)
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			Mimetype:      proto.String("image/webp"),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(length),
			Width:         proto.Uint32(uint32(width)),
			Height:        proto.Uint32(uint32(height)),
			ContextInfo:   quote,
		}}

	case db.MediaVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(length),
			ContextInfo:   quote,
		}}

	case db.MediaAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(length),
			ContextInfo:   quote,
		}}

	default:
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(length),
			ContextInfo:   quote,
		}}
	}
}
