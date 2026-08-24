package client

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"whatsnative/db"
)

// testSession is a Session with nothing live behind it: storeConversationMeta
// and onMarkChatAsRead never touch WA, only the store and the event channel.
func testSession(t *testing.T) (*Session, *db.MessageStore) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	store, err := db.NewMessageStore(conn)
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}

	return &Session{
		messages: store,
		events:   make(chan any, 16),
		done:     make(chan struct{}),
	}, store
}

const daruJID = "234@s.whatsapp.net"

// seedHistoryMessage gives a chat a last_active timestamp, the way the one
// real message that always accompanies a history sync's conversation entry
// would. Chats() hides anything without one, and storeConversationMeta alone
// never sets it -- exactly like production, where the message loop in
// onHistorySync is what does.
func seedHistoryMessage(t *testing.T, store *db.MessageStore, chatJID string) {
	t.Helper()
	if err := store.SaveMessage(db.Message{
		ID: "seed", ChatJID: chatJID, SenderJID: chatJID,
		Content: "hi", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// The bug: after being offline a few days, everything a chat's phone-side
// history sync reports as newly caught up on came back marked unread here,
// even though it had genuinely been read on the phone. History sync's unread
// count is only trustworthy the very first time a chat is seen -- after that
// it is relative to this device's own checkpoint, not to what has been read.
func TestHistorySyncOnlySeedsUnreadOnce(t *testing.T) {
	session, store := testSession(t)
	jid, _ := types.ParseJID(daruJID)

	// First sync ever: the chat is new to us, so the count is the only signal
	// there is, and it should be trusted.
	session.storeConversationMeta(jid, &waHistorySync.Conversation{
		ID:          proto.String(daruJID),
		Name:        proto.String("Daru"),
		UnreadCount: proto.Uint32(4),
	})
	seedHistoryMessage(t, store, daruJID)

	chats, err := store.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Unread != 4 {
		t.Fatalf("chats = %+v, want one chat with 4 unread", chats)
	}

	// The chat gets read -- on the phone, synced back here as MarkChatAsRead,
	// not through anything typed in this app.
	if err := store.MarkRead(daruJID); err != nil {
		t.Fatal(err)
	}

	// A reconnect a few days later delivers another history sync. Its count
	// still reflects this device's old checkpoint from before it went
	// offline, so it is stale -- and must not overwrite the correct zero.
	session.storeConversationMeta(jid, &waHistorySync.Conversation{
		ID:          proto.String(daruJID),
		Name:        proto.String("Daru"),
		UnreadCount: proto.Uint32(4),
	})

	chats, err = store.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Unread != 0 {
		t.Fatalf("after the resync, chats = %+v, want unread still 0", chats)
	}
}

// The other half: what actually corrects a stale count, if the chat is read
// on the phone while this device is disconnected and no live BumpUnread ever
// ran to begin with.
func TestMarkChatAsReadClearsLocalUnread(t *testing.T) {
	session, store := testSession(t)
	jid, _ := types.ParseJID(daruJID)

	session.storeConversationMeta(jid, &waHistorySync.Conversation{
		ID:          proto.String(daruJID),
		Name:        proto.String("Daru"),
		UnreadCount: proto.Uint32(7),
	})
	seedHistoryMessage(t, store, daruJID)

	session.onMarkChatAsRead(&events.MarkChatAsRead{
		JID:    jid,
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)},
	})

	chats, err := store.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Unread != 0 {
		t.Fatalf("chats = %+v, want unread cleared to 0", chats)
	}

	select {
	case evt := <-session.events:
		if _, ok := evt.(ReadStateSynced); !ok {
			t.Errorf("emitted %T, want ReadStateSynced", evt)
		}
	default:
		t.Error("clearing unread should have told the UI to reload")
	}
}

// Marking a chat unread on purpose is the mirror action, and this device has
// no local "unread on purpose" state to set -- there is nothing to do here but
// leave the existing count alone rather than invent one.
func TestMarkChatAsUnreadDoesNothingLocally(t *testing.T) {
	session, store := testSession(t)
	jid, _ := types.ParseJID(daruJID)

	session.storeConversationMeta(jid, &waHistorySync.Conversation{
		ID:          proto.String(daruJID),
		Name:        proto.String("Daru"),
		UnreadCount: proto.Uint32(2),
	})
	seedHistoryMessage(t, store, daruJID)

	session.onMarkChatAsRead(&events.MarkChatAsRead{
		JID:    jid,
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)},
	})

	chats, err := store.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Unread != 2 {
		t.Fatalf("chats = %+v, want the count left at 2", chats)
	}

	select {
	case evt := <-session.events:
		t.Errorf("emitted %T for a mark-as-unread, want nothing", evt)
	default:
	}
}
