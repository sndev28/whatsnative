package client

import (
	"fmt"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"whatsnative/db"
)

// content is what the store keeps from a protobuf message: a line of text, an
// attachment, a poll, and the message being replied to.
type content struct {
	Text  string
	Media db.Media
	Reply db.Reply
	Poll  db.Poll
}

// empty reports whether there is nothing worth storing.
func (c content) empty() bool {
	return c.Text == "" && c.Media.Kind == db.MediaNone && c.Poll.Question == ""
}

// maxUnwrapDepth bounds the peeling in unwrap. Two layers is normal -- a
// view-once photo in a disappearing chat -- and nothing legitimate goes deep.
const maxUnwrapDepth = 8

// unwrap peels the containers WhatsApp puts a real message inside, and reports
// whether any of them was a view-once envelope.
//
// A view-once photo does not arrive as a Message with an ImageMessage on it:
// it arrives as a Message whose only populated field holds another Message,
// and the photo is in there. Nothing at the top level is set, so without this
// the message describes as empty and is dropped on the floor. Disappearing
// messages and documents-with-captions are wrapped the same way.
func unwrap(m *waE2E.Message) (payload *waE2E.Message, viewOnce bool) {
	for range maxUnwrapDepth {
		var inner *waE2E.Message
		switch {
		case m.GetViewOnceMessage() != nil:
			inner, viewOnce = m.GetViewOnceMessage().GetMessage(), true
		case m.GetViewOnceMessageV2() != nil:
			inner, viewOnce = m.GetViewOnceMessageV2().GetMessage(), true
		case m.GetViewOnceMessageV2Extension() != nil:
			inner, viewOnce = m.GetViewOnceMessageV2Extension().GetMessage(), true
		case m.GetEphemeralMessage() != nil:
			inner = m.GetEphemeralMessage().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil:
			inner = m.GetDocumentWithCaptionMessage().GetMessage()
		case m.GetGroupMentionedMessage() != nil:
			inner = m.GetGroupMentionedMessage().GetMessage()
		case m.GetLottieStickerMessage() != nil:
			inner = m.GetLottieStickerMessage().GetMessage()
		}
		if inner == nil {
			return m, viewOnce
		}
		m = inner
	}
	return m, viewOnce
}

// describe walks a protobuf message. Everything WhatsApp can send arrives as
// one big oneof-style struct where exactly one field is set, so this is a walk
// through the kinds we handle.
func describe(m *waE2E.Message) content {
	// Unwrapping here rather than at the call sites means history sync, quoted
	// replies and thumbnail backfill all get it too, and none of them can be
	// forgotten later.
	m, viewOnce := unwrap(m)

	text, media, reply := describeParts(m)
	if media.Kind != db.MediaNone {
		media.ViewOnce = viewOnce
	}
	return content{Text: text, Media: media, Reply: reply, Poll: describePoll(m)}
}

// describePoll pulls out a poll's question and options. Votes are encrypted
// per recipient, so only the poll itself can be shown, not the tallies.
func describePoll(m *waE2E.Message) db.Poll {
	poll := m.GetPollCreationMessage()
	if poll == nil {
		return db.Poll{}
	}

	options := make([]string, 0, len(poll.GetOptions()))
	for _, option := range poll.GetOptions() {
		options = append(options, option.GetOptionName())
	}
	return db.Poll{Question: poll.GetName(), Options: options}
}

func describeParts(m *waE2E.Message) (text string, media db.Media, reply db.Reply) {
	if m == nil {
		return "", db.Media{}, db.Reply{}
	}

	if poll := m.GetPollCreationMessage(); poll != nil {
		return "", db.Media{}, quotedFrom(poll.GetContextInfo())
	}

	switch {
	case m.GetConversation() != "":
		// Plain text cannot carry a ContextInfo, so it is never a reply.
		return m.GetConversation(), db.Media{}, db.Reply{}

	case m.GetExtendedTextMessage() != nil:
		extended := m.GetExtendedTextMessage()
		return extended.GetText(), db.Media{}, quotedFrom(extended.GetContextInfo())

	case m.GetImageMessage() != nil:
		image := m.GetImageMessage()
		return image.GetCaption(), db.Media{
			Kind:      db.MediaImage,
			Mime:      image.GetMimetype(),
			Size:      int64(image.GetFileLength()),
			Proto:     marshal(m),
			Thumbnail: image.GetJPEGThumbnail(),
		}, quotedFrom(image.GetContextInfo())

	case m.GetStickerMessage() != nil:
		sticker := m.GetStickerMessage()
		// Most stickers are animated webp, which no Go decoder will open, so
		// the embedded still is the only thing we can actually draw.
		return "", db.Media{
			Kind:      db.MediaSticker,
			Mime:      sticker.GetMimetype(),
			Size:      int64(sticker.GetFileLength()),
			Proto:     marshal(m),
			Thumbnail: sticker.GetPngThumbnail(),
		}, quotedFrom(sticker.GetContextInfo())

	case m.GetVideoMessage() != nil:
		video := m.GetVideoMessage()
		return video.GetCaption(), db.Media{
			Kind:      db.MediaVideo,
			Mime:      video.GetMimetype(),
			Size:      int64(video.GetFileLength()),
			Proto:     marshal(m),
			Thumbnail: video.GetJPEGThumbnail(),
		}, quotedFrom(video.GetContextInfo())

	case m.GetAudioMessage() != nil:
		audio := m.GetAudioMessage()
		name := "audio"
		if audio.GetPTT() {
			name = "voice note"
		}
		return "", db.Media{
			Kind:  db.MediaAudio,
			Mime:  audio.GetMimetype(),
			Name:  name,
			Size:  int64(audio.GetFileLength()),
			Proto: marshal(m),
		}, quotedFrom(audio.GetContextInfo())

	case m.GetDocumentMessage() != nil:
		document := m.GetDocumentMessage()
		return document.GetCaption(), db.Media{
			Kind:  db.MediaDocument,
			Mime:  document.GetMimetype(),
			Name:  document.GetFileName(),
			Size:  int64(document.GetFileLength()),
			Proto: marshal(m),
		}, quotedFrom(document.GetContextInfo())

	case m.GetLocationMessage() != nil:
		location := m.GetLocationMessage()
		return withCaption("[location]", location.GetName()), db.Media{}, db.Reply{}

	case m.GetContactMessage() != nil:
		contact := m.GetContactMessage()
		return withCaption("[contact]", contact.GetDisplayName()), db.Media{}, db.Reply{}
	}

	return "", db.Media{}, db.Reply{}
}

// quotedFrom pulls the reply target out of a ContextInfo. The quoted text is
// summarised the same way as any other message, so a reply to a photo shows
// "[photo]" rather than nothing.
func quotedFrom(info *waE2E.ContextInfo) db.Reply {
	if info == nil || info.GetStanzaID() == "" {
		return db.Reply{}
	}

	quoted := describe(info.GetQuotedMessage())
	preview := quoted.Text
	if preview == "" {
		preview = db.Message{Media: quoted.Media, Poll: quoted.Poll}.Preview()
	}

	// The participant arrives as a raw JID, sometimes with a device suffix.
	// Trimming it is what lets the name lookup match a contact or an alias.
	sender := info.GetParticipant()
	if parsed, err := types.ParseJID(sender); err == nil {
		sender = parsed.ToNonAD().String()
	}

	return db.Reply{
		MessageID: info.GetStanzaID(),
		Sender:    sender,
		Text:      preview,
	}
}

// buildContextInfo turns a stored message back into the quote WhatsApp expects
// on an outgoing reply.
func buildContextInfo(replyTo db.Message) *waE2E.ContextInfo {
	if replyTo.ID == "" {
		return nil
	}

	// The recipient renders the quote from what we send here, so a short
	// stand-in message is enough; we do not have to reproduce the original.
	quoted := &waE2E.Message{Conversation: proto.String(replyTo.Preview())}

	return &waE2E.ContextInfo{
		StanzaID:      proto.String(replyTo.ID),
		Participant:   proto.String(replyTo.SenderJID),
		QuotedMessage: quoted,
	}
}

// revokedBy reports whether a message is a delete-for-everyone.
//
// The nil check is the whole point. ProtocolMessage_REVOKE is zero, and
// GetType on a nil ProtocolMessage returns the zero value, so asking only
// "is the type REVOKE?" is true of every ordinary message ever sent.
func revokedBy(m *waE2E.Message) (*waE2E.ProtocolMessage, bool) {
	protocol := m.GetProtocolMessage()
	if protocol == nil {
		return nil, false
	}
	return protocol, protocol.GetType() == waE2E.ProtocolMessage_REVOKE
}

// forwardable rebuilds a stored message as something we can send on, flagged
// as a forward so the recipient sees it as one.
func forwardable(message db.Message) (*waE2E.Message, error) {
	if message.Revoked {
		return nil, fmt.Errorf("that message was deleted")
	}

	if len(message.Media.Proto) > 0 {
		var original waE2E.Message
		if err := proto.Unmarshal(message.Media.Proto, &original); err != nil {
			return nil, fmt.Errorf("decode stored attachment: %w", err)
		}
		markForwarded(&original)
		return &original, nil
	}

	if message.Content == "" {
		return nil, fmt.Errorf("nothing to forward")
	}
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(message.Content),
			ContextInfo: forwardContext(nil),
		},
	}, nil
}

// markForwarded sets the forwarded flag on whichever media message is present.
// Each kind has its own struct rather than a shared one, so this is a walk.
func markForwarded(m *waE2E.Message) {
	switch {
	case m.GetImageMessage() != nil:
		m.ImageMessage.ContextInfo = forwardContext(m.ImageMessage.GetContextInfo())
	case m.GetVideoMessage() != nil:
		m.VideoMessage.ContextInfo = forwardContext(m.VideoMessage.GetContextInfo())
	case m.GetAudioMessage() != nil:
		m.AudioMessage.ContextInfo = forwardContext(m.AudioMessage.GetContextInfo())
	case m.GetDocumentMessage() != nil:
		m.DocumentMessage.ContextInfo = forwardContext(m.DocumentMessage.GetContextInfo())
	case m.GetStickerMessage() != nil:
		m.StickerMessage.ContextInfo = forwardContext(m.StickerMessage.GetContextInfo())
	}
}

// forwardContext marks a context as forwarded, bumping the hop count that
// WhatsApp uses to show "forwarded many times".
func forwardContext(existing *waE2E.ContextInfo) *waE2E.ContextInfo {
	context := existing
	if context == nil {
		context = &waE2E.ContextInfo{}
	}
	// A forward carries no quote: the reply it may have been is not ours.
	context.StanzaID = nil
	context.Participant = nil
	context.QuotedMessage = nil

	context.IsForwarded = proto.Bool(true)
	context.ForwardingScore = proto.Uint32(context.GetForwardingScore() + 1)
	return context
}

func marshal(m *waE2E.Message) []byte {
	// A message that will not marshal simply cannot be re-downloaded later;
	// that is worth degrading for, not failing the whole save for.
	data, err := proto.Marshal(m)
	if err != nil {
		return nil
	}
	return data
}

func withCaption(kind, caption string) string {
	if caption == "" {
		return kind
	}
	return kind + " " + caption
}
