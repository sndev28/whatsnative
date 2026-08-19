package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Chat kinds. Status updates and newsletters arrive in the same message
// stream as ordinary chats, but belong on their own tabs.
const (
	KindChat       = "chat"
	KindGroup      = "group"
	KindStatus     = "status"
	KindNewsletter = "newsletter"
)

// Delivery states for our own messages, in the order they progress.
const (
	StatusPending   = ""
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
)

// Media kinds, stored as strings so the column stays readable in sqlite3.
const (
	MediaNone     = ""
	MediaImage    = "image"
	MediaVideo    = "video"
	MediaAudio    = "audio"
	MediaDocument = "document"
	MediaSticker  = "sticker"
)

// Reaction is one emoji from one person on one message.
type Reaction struct {
	MessageID string
	SenderJID string
	Sender    string
	Emoji     string
	Timestamp time.Time
}

// Media describes an attachment. Proto holds the marshalled protobuf message
// the attachment came in, which is what whatsmeow needs to decrypt and fetch
// the actual bytes later; Path is set once we have them on disk.
type Media struct {
	Kind  string
	Mime  string
	Name  string
	Size  int64
	Proto []byte
	Path  string
	// Thumbnail is the small still picture WhatsApp embeds in the message
	// itself. It needs no download, and it is the only way to show an
	// animated sticker, whose webp no Go decoder will open.
	Thumbnail []byte
}

// Reply is the quoted message shown above a reply.
type Reply struct {
	MessageID string
	Sender    string
	Text      string
}

// Message is one chat message as whatsnative stores it locally.
type Message struct {
	ID        string
	ChatJID   string
	SenderJID string
	Sender    string
	Content   string
	Timestamp time.Time
	FromMe    bool
	IsGroup   bool

	// PushName is what the sender calls themselves. For someone not in the
	// address book it is all we have, and the UI marks it with a tilde.
	PushName string
	// Status is the delivery state of a message we sent.
	Status string
	// Revoked means the message was deleted for everyone. The row is kept so
	// the conversation still reads in order, with a placeholder in its place.
	Revoked bool

	Media     Media
	Reply     Reply
	Poll      Poll
	Reactions []Reaction
}

// Poll is a poll message: a question and the options offered.
type Poll struct {
	Question string
	Options  []string
}

// IsPoll reports whether this message is a poll.
func (m Message) IsPoll() bool { return m.Poll.Question != "" }

// Kind classifies which tab this message's chat belongs on, from its JID.
// Status updates and newsletters share the message stream with real chats.
func (m Message) Kind() string {
	switch {
	case strings.HasSuffix(m.ChatJID, "@newsletter"):
		return KindNewsletter
	case strings.HasPrefix(m.ChatJID, "status@"):
		return KindStatus
	case m.IsGroup:
		return KindGroup
	default:
		return KindChat
	}
}

// HasMedia reports whether this message carries an attachment.
func (m Message) HasMedia() bool { return m.Media.Kind != MediaNone }

// Chat is one conversation as it appears in the conversation list.
type Chat struct {
	JID         string
	Name        string
	Kind        string
	IsGroup     bool
	LastMessage string
	LastActive  time.Time
	Pinned      bool
	Muted       bool
	Unread      int
}

// MessageStore is the local chat history.
//
// whatsmeow's container persists protocol state only: keys, sessions,
// contacts. Nothing keeps the messages themselves, so anything the UI wants to
// show has to be written here by us.
type MessageStore struct {
	conn *sql.DB

	// SQLite serialises writers by itself, but taking the lock keeps the
	// several statements inside SaveMessage from interleaving with a save
	// running on one of whatsmeow's other goroutines.
	mu sync.Mutex
}

const messageSchema = `
CREATE TABLE IF NOT EXISTS chats (
	jid          TEXT PRIMARY KEY,
	name         TEXT    NOT NULL DEFAULT '',
	is_group     INTEGER NOT NULL DEFAULT 0,
	last_message TEXT    NOT NULL DEFAULT '',
	last_active  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS contacts (
	jid  TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT ''
);

-- WhatsApp addresses the same person two ways: by phone number
-- (user@s.whatsapp.net) and by LID (user@lid). Which one turns up depends on
-- the chat, so we remember the pairing and write every name under both.
CREATE TABLE IF NOT EXISTS aliases (
	jid TEXT PRIMARY KEY,
	alt TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
	id         TEXT    NOT NULL,
	chat_jid   TEXT    NOT NULL,
	sender_jid TEXT    NOT NULL DEFAULT '',
	sender     TEXT    NOT NULL DEFAULT '',
	content    TEXT    NOT NULL DEFAULT '',
	timestamp  INTEGER NOT NULL,
	from_me    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (id, chat_jid)
);

CREATE INDEX IF NOT EXISTS messages_by_chat ON messages (chat_jid, timestamp);

CREATE TABLE IF NOT EXISTS reactions (
	message_id TEXT    NOT NULL,
	chat_jid   TEXT    NOT NULL,
	sender_jid TEXT    NOT NULL,
	sender     TEXT    NOT NULL DEFAULT '',
	emoji      TEXT    NOT NULL DEFAULT '',
	timestamp  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (message_id, chat_jid, sender_jid)
);

CREATE INDEX IF NOT EXISTS reactions_by_chat ON reactions (chat_jid, message_id);
`

// addedColumns are applied on top of the base schema, per table.
//
// SQLite has no ADD COLUMN IF NOT EXISTS, so they go on one at a time and only
// when missing. This is what lets a database from an earlier version pick up
// new features instead of being thrown away.
var addedColumns = map[string][]struct{ name, definition string }{
	"messages": {
		{"media_kind", "TEXT NOT NULL DEFAULT ''"},
		{"media_mime", "TEXT NOT NULL DEFAULT ''"},
		{"media_name", "TEXT NOT NULL DEFAULT ''"},
		{"media_size", "INTEGER NOT NULL DEFAULT 0"},
		{"media_proto", "BLOB"},
		{"media_path", "TEXT NOT NULL DEFAULT ''"},
		{"reply_id", "TEXT NOT NULL DEFAULT ''"},
		{"reply_sender", "TEXT NOT NULL DEFAULT ''"},
		{"reply_text", "TEXT NOT NULL DEFAULT ''"},
		{"push_name", "TEXT NOT NULL DEFAULT ''"},
		{"status", "TEXT NOT NULL DEFAULT ''"},
		{"poll_question", "TEXT NOT NULL DEFAULT ''"},
		{"poll_options", "TEXT NOT NULL DEFAULT ''"},
		{"revoked", "INTEGER NOT NULL DEFAULT 0"},
		{"media_thumb", "BLOB"},
	},
	"chats": {
		{"kind", "TEXT NOT NULL DEFAULT 'chat'"},
		{"pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"unread", "INTEGER NOT NULL DEFAULT 0"},
		{"muted", "INTEGER NOT NULL DEFAULT 0"},
		{"archived", "INTEGER NOT NULL DEFAULT 0"},
	},
}

// NewMessageStore creates or migrates the whatsnative tables. It shares the
// connection with whatsmeow, which owns the whatsmeow_* tables.
func NewMessageStore(conn *sql.DB) (*MessageStore, error) {
	store := &MessageStore{conn: conn}
	if err := store.Migrate(); err != nil {
		return nil, err
	}
	return store, nil
}

// Migrate creates or updates the whatsnative tables. It runs at every startup,
// and every step is written to be safe to repeat.
func (s *MessageStore) Migrate() error {
	conn := s.conn
	if _, err := conn.Exec(messageSchema); err != nil {
		return fmt.Errorf("create message schema: %w", err)
	}
	for table := range addedColumns {
		if err := migrateTable(conn, table); err != nil {
			return err
		}
	}

	// Sender JIDs written by earlier versions kept the device suffix that
	// whatsmeow puts on an address (user:3@server). Nothing joins against
	// those, which is why older messages kept showing a raw ID or a ~name.
	for _, column := range []string{"sender_jid", "reply_sender"} {
		if _, err := conn.Exec(fmt.Sprintf(
			`UPDATE messages
			 SET %[1]s = substr(%[1]s, 1, instr(%[1]s, ':') - 1) || substr(%[1]s, instr(%[1]s, '@'))
			 WHERE instr(%[1]s, ':') > 0 AND instr(%[1]s, '@') > instr(%[1]s, ':')`,
			column,
		)); err != nil {
			return fmt.Errorf("normalise %s: %w", column, err)
		}
	}

	// Chats stored before the kind column existed all defaulted to "chat",
	// which left the Status and Channels tabs permanently empty. The JID says
	// which stream a chat belongs to, so classify them all from that.
	if _, err := conn.Exec(
		`UPDATE chats SET kind = CASE
		     WHEN jid LIKE '%@newsletter' THEN 'newsletter'
		     WHEN jid LIKE 'status@%'     THEN 'status'
		     WHEN jid LIKE '%@g.us'       THEN 'group'
		     ELSE 'chat'
		 END
		 WHERE kind <> CASE
		     WHEN jid LIKE '%@newsletter' THEN 'newsletter'
		     WHEN jid LIKE 'status@%'     THEN 'status'
		     WHEN jid LIKE '%@g.us'       THEN 'group'
		     ELSE 'chat'
		 END`,
	); err != nil {
		return fmt.Errorf("classify chats: %w", err)
	}

	return nil
}

func migrateTable(conn *sql.DB, table string) error {
	rows, err := conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s table: %w", table, err)
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan column info: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate column info: %w", err)
	}

	for _, column := range addedColumns[table] {
		if existing[column.name] {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column.name, column.definition)
		if _, err := conn.Exec(statement); err != nil {
			return fmt.Errorf("add column %s: %w", column.name, err)
		}
	}
	return nil
}

// SaveMessage records a message and moves its chat's preview line forward.
//
// Saving a message we already have is a no-op, which matters because our own
// outgoing messages come back through the event stream from the phone and any
// other linked devices.
func (s *MessageStore) SaveMessage(m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`INSERT OR IGNORE INTO messages
		     (id, chat_jid, sender_jid, sender, content, timestamp, from_me,
		      media_kind, media_mime, media_name, media_size, media_proto, media_path,
		      media_thumb, reply_id, reply_sender, reply_text,
		      push_name, status, poll_question, poll_options)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.SenderJID, m.Sender, m.Content, m.Timestamp.Unix(), m.FromMe,
		m.Media.Kind, m.Media.Mime, m.Media.Name, m.Media.Size, m.Media.Proto, m.Media.Path,
		m.Media.Thumbnail,
		m.Reply.MessageID, m.Reply.Sender, m.Reply.Text,
		m.PushName, m.Status, m.Poll.Question, encodeOptions(m.Poll.Options),
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	// The WHERE guard keeps back-filled history from overwriting the preview
	// with something older than what is already there.
	_, err = s.conn.Exec(
		`INSERT INTO chats (jid, is_group, kind, last_message, last_active)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (jid) DO UPDATE SET
		     is_group     = excluded.is_group,
		     kind         = excluded.kind,
		     last_message = excluded.last_message,
		     last_active  = excluded.last_active
		 WHERE excluded.last_active >= chats.last_active`,
		m.ChatJID, m.IsGroup, m.Kind(), m.Preview(), m.Timestamp.Unix(),
	)
	if err != nil {
		return fmt.Errorf("touch chat: %w", err)
	}
	return nil
}

// Preview is the one-line summary shown in the chat list.
func (m Message) Preview() string {
	if m.Revoked {
		return "[deleted]"
	}
	if m.IsPoll() {
		return "[poll] " + m.Poll.Question
	}
	if m.Content != "" {
		return m.Content
	}
	switch m.Media.Kind {
	case MediaImage:
		return "[photo]"
	case MediaVideo:
		return "[video]"
	case MediaAudio:
		return "[audio]"
	case MediaSticker:
		return "[sticker]"
	case MediaDocument:
		if m.Media.Name != "" {
			return "[document] " + m.Media.Name
		}
		return "[document]"
	}
	return ""
}

// MarkRevoked records that a message was deleted for everyone.
//
// The row stays: dropping it would renumber the conversation under whatever
// the user was looking at, and WhatsApp itself leaves a placeholder.
func (s *MessageStore) MarkRevoked(chatJID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`UPDATE messages SET revoked = 1, content = '', media_proto = NULL, media_path = ''
		 WHERE chat_jid = ? AND id = ?`,
		chatJID, messageID,
	)
	if err != nil {
		return fmt.Errorf("mark revoked: %w", err)
	}

	// The chat's preview line still shows the deleted text otherwise.
	_, err = s.conn.Exec(
		`UPDATE chats SET last_message = '[deleted]'
		 WHERE jid = ? AND last_message <> '' AND last_active = (
		     SELECT timestamp FROM messages WHERE chat_jid = ? AND id = ?
		 )`,
		chatJID, chatJID, messageID,
	)
	if err != nil {
		return fmt.Errorf("clear preview: %w", err)
	}
	return nil
}

// MessagesMissingThumbnail returns media messages we hold a protobuf for but
// no still picture.
//
// The thumbnail column arrived after these rows were written, and the still is
// sitting inside the protobuf we already stored, so it can be pulled out after
// the fact rather than waiting for the message to be sent again.
func (s *MessageStore) MessagesMissingThumbnail(limit int) ([]Message, error) {
	rows, err := s.conn.Query(
		`SELECT id, chat_jid, media_proto
		 FROM messages
		 WHERE media_kind <> '' AND media_proto IS NOT NULL
		   AND (media_thumb IS NULL OR length(media_thumb) = 0)
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages missing thumbnails: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ChatJID, &message.Media.Proto); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}

// SaveThumbnail records the still picture for a message.
func (s *MessageStore) SaveThumbnail(chatJID, messageID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`UPDATE messages SET media_thumb = ? WHERE chat_jid = ? AND id = ?`,
		data, chatJID, messageID,
	)
	if err != nil {
		return fmt.Errorf("save thumbnail: %w", err)
	}
	return nil
}

// SaveMediaPath records where a downloaded attachment landed, so the next
// render can show it without fetching again.
func (s *MessageStore) SaveMediaPath(messageID, chatJID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`UPDATE messages SET media_path = ? WHERE id = ? AND chat_jid = ?`,
		path, messageID, chatJID,
	)
	if err != nil {
		return fmt.Errorf("save media path: %w", err)
	}
	return nil
}

// SaveReaction records one person's reaction to a message. An empty emoji is
// how WhatsApp signals that a reaction was removed.
func (s *MessageStore) SaveReaction(chatJID string, r Reaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Emoji == "" {
		_, err := s.conn.Exec(
			`DELETE FROM reactions WHERE message_id = ? AND chat_jid = ? AND sender_jid = ?`,
			r.MessageID, chatJID, r.SenderJID,
		)
		if err != nil {
			return fmt.Errorf("remove reaction: %w", err)
		}
		return nil
	}

	_, err := s.conn.Exec(
		`INSERT INTO reactions (message_id, chat_jid, sender_jid, sender, emoji, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (message_id, chat_jid, sender_jid) DO UPDATE SET
		     emoji     = excluded.emoji,
		     sender    = excluded.sender,
		     timestamp = excluded.timestamp`,
		r.MessageID, chatJID, r.SenderJID, r.Sender, r.Emoji, r.Timestamp.Unix(),
	)
	if err != nil {
		return fmt.Errorf("save reaction: %w", err)
	}
	return nil
}

// SaveChatName records a chat's own title, which is what groups have instead
// of a contact to look up.
func (s *MessageStore) SaveChatName(jid, name string, isGroup bool) error {
	if name == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`INSERT INTO chats (jid, name, is_group) VALUES (?, ?, ?)
		 ON CONFLICT (jid) DO UPDATE SET
		     name     = excluded.name,
		     is_group = excluded.is_group`,
		jid, name, isGroup,
	)
	if err != nil {
		return fmt.Errorf("save chat name: %w", err)
	}
	return nil
}

// SaveContact records a display name for a one-to-one JID.
//
// Contacts get their own table rather than rows in chats: an address book has
// thousands of entries, and they should not show up as conversations until
// somebody actually sends something.
//
// The name is also written against the JID's other address, if we know it, so
// a message that arrives by LID still finds a contact saved by phone number.
func (s *MessageStore) SaveContact(jid, name string) error {
	if name == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveContactLocked(jid, name)
}

func (s *MessageStore) saveContactLocked(jid, name string) error {
	_, err := s.conn.Exec(
		`INSERT INTO contacts (jid, name) VALUES (?, ?)
		 ON CONFLICT (jid) DO UPDATE SET name = excluded.name`,
		jid, name,
	)
	if err != nil {
		return fmt.Errorf("save contact: %w", err)
	}

	// Carry the name across to the other address for the same person, without
	// disturbing a name already saved against that address.
	_, err = s.conn.Exec(
		`INSERT INTO contacts (jid, name)
		 SELECT alt, ? FROM aliases WHERE jid = ?
		 ON CONFLICT (jid) DO UPDATE SET name = excluded.name
		 WHERE contacts.name = ''`,
		name, jid,
	)
	if err != nil {
		return fmt.Errorf("mirror contact to alias: %w", err)
	}
	return nil
}

// CountNamedContacts is how many address-book names we hold. Zero means the
// contact list has not reached us, which is worth a full resync.
func (s *MessageStore) CountNamedContacts() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	if err := s.conn.QueryRow(`SELECT count(*) FROM contacts WHERE name <> ''`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count contacts: %w", err)
	}
	return count, nil
}

// Exec runs a statement against the shared connection. It exists for tests
// that need to stand up whatsmeow's own tables.
func (s *MessageStore) Exec(statement string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(statement)
	return err
}

// SaveContacts replaces the address book in one transaction.
//
// It replaces rather than merges so that names written by an older version --
// which mistook profile names for saved ones -- do not linger and keep
// masquerading as address-book entries. An empty map is ignored, so a failed
// sync cannot wipe what we have.
//
// One statement per contact was also taking seconds on a real account, and the
// UI resolved names gradually while it ran: some chats named, some not, and a
// chat re-opened later looking different. Batching removes that window.
func (s *MessageStore) SaveContacts(names map[string]string) error {
	if len(names) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin contacts: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM contacts`); err != nil {
		return fmt.Errorf("clear contacts: %w", err)
	}

	statement, err := tx.Prepare(
		`INSERT INTO contacts (jid, name) VALUES (?, ?)
		 ON CONFLICT (jid) DO UPDATE SET name = excluded.name`,
	)
	if err != nil {
		return fmt.Errorf("prepare contacts: %w", err)
	}
	defer statement.Close()

	for jid, name := range names {
		if name == "" {
			continue
		}
		if _, err := statement.Exec(jid, name); err != nil {
			return fmt.Errorf("save contact %s: %w", jid, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit contacts: %w", err)
	}
	return nil
}

// ImportLIDMap copies whatsmeow's own LID/phone-number pairs into our alias
// table in a single statement.
//
// whatsmeow keeps that mapping in the same database, so reading it directly is
// both the fastest and the most current source: asking it one JID at a time
// was what made naming feel like it healed itself as you clicked around.
func (s *MessageStore) ImportLIDMap() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var present int
	err := s.conn.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'whatsmeow_lid_map'`,
	).Scan(&present)
	if err != nil {
		return fmt.Errorf("look for lid map: %w", err)
	}
	if present == 0 {
		// No login yet, or an older whatsmeow: nothing to import.
		return nil
	}

	// The map stores bare user parts, so the servers go back on here.
	for _, statement := range []string{
		// The WHERE true is not idle: without it SQLite cannot tell where the
		// SELECT ends and the upsert clause begins.
		`INSERT INTO aliases (jid, alt)
		 SELECT lid || '@lid', pn || '@s.whatsapp.net' FROM whatsmeow_lid_map
		 WHERE true
		 ON CONFLICT (jid) DO UPDATE SET alt = excluded.alt`,
		`INSERT INTO aliases (jid, alt)
		 SELECT pn || '@s.whatsapp.net', lid || '@lid' FROM whatsmeow_lid_map
		 WHERE true
		 ON CONFLICT (jid) DO UPDATE SET alt = excluded.alt`,
	} {
		if _, err := s.conn.Exec(statement); err != nil {
			return fmt.Errorf("import lid map: %w", err)
		}
	}
	return nil
}

// MirrorNamesAcrossAliases gives every known name to the other address for the
// same person, so a contact saved by phone number is found by LID and back.
func (s *MessageStore) MirrorNamesAcrossAliases() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`INSERT INTO contacts (jid, name)
		 SELECT a.alt, c.name
		 FROM aliases a
		 JOIN contacts c ON c.jid = a.jid
		 WHERE c.name <> ''
		 ON CONFLICT (jid) DO UPDATE SET name = excluded.name
		 WHERE contacts.name = ''`,
	)
	if err != nil {
		return fmt.Errorf("mirror names: %w", err)
	}
	return nil
}

// LinkJIDPairs records many address pairs in one transaction. Group metadata
// hands us both addresses for every member, and a large account has thousands.
func (s *MessageStore) LinkJIDPairs(pairs [][2]string) error {
	if len(pairs) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin aliases: %w", err)
	}
	defer tx.Rollback()

	statement, err := tx.Prepare(
		`INSERT INTO aliases (jid, alt) VALUES (?, ?)
		 ON CONFLICT (jid) DO UPDATE SET alt = excluded.alt`,
	)
	if err != nil {
		return fmt.Errorf("prepare aliases: %w", err)
	}
	defer statement.Close()

	for _, pair := range pairs {
		if pair[0] == "" || pair[1] == "" || pair[0] == pair[1] {
			continue
		}
		if _, err := statement.Exec(pair[0], pair[1]); err != nil {
			return fmt.Errorf("link %s: %w", pair[0], err)
		}
		if _, err := statement.Exec(pair[1], pair[0]); err != nil {
			return fmt.Errorf("link %s: %w", pair[1], err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit aliases: %w", err)
	}
	return nil
}

// LinkJIDs records that two addresses belong to the same person, and copies
// whichever name is already known across to the other.
//
// This is what makes saved contacts resolve: whatsmeow hands us a phone number
// in one place and a LID in another, and only one of them matches the address
// book.
func (s *MessageStore) LinkJIDs(first, second string) error {
	if first == "" || second == "" || first == second {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`INSERT INTO aliases (jid, alt) VALUES (?, ?), (?, ?)
		 ON CONFLICT (jid) DO UPDATE SET alt = excluded.alt`,
		first, second, second, first,
	)
	if err != nil {
		return fmt.Errorf("link jids: %w", err)
	}

	// Whichever side already has a name, give it to the other -- but only
	// where the other has none, so a name saved for that exact address wins.
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		var name string
		err := s.conn.QueryRow(`SELECT name FROM contacts WHERE jid = ?`, pair[0]).Scan(&name)
		if err == sql.ErrNoRows || name == "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("read contact for alias: %w", err)
		}

		var existing string
		err = s.conn.QueryRow(`SELECT name FROM contacts WHERE jid = ?`, pair[1]).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read alias contact: %w", err)
		}
		if existing != "" {
			continue
		}

		if err := s.saveContactLocked(pair[1], name); err != nil {
			return err
		}
	}
	return nil
}

// Chats returns conversations of the given kinds that have at least one
// message: pinned ones first, then most recently active.
//
// The display name falls back from the chat's own title to the contact name to
// the raw JID, which is what gives groups and newsletters their proper names.
func (s *MessageStore) Chats(kinds ...string) ([]Chat, error) {
	if len(kinds) == 0 {
		kinds = []string{KindChat, KindGroup}
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	arguments := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		arguments = append(arguments, kind)
	}

	rows, err := s.conn.Query(
		`SELECT c.jid,
		        COALESCE(
		            -- A group or channel has a title of its own.
		            NULLIF(c.name, ''),
		            -- Then the address book, under either of the person's
		            -- two addresses.
		            NULLIF(ct.name, ''),
		            NULLIF(ac.name, ''),
		            -- Then what they call themselves, marked as unsaved the
		            -- same way the transcript marks it.
		            NULLIF((SELECT '~' || m.push_name FROM messages m
		                    WHERE m.chat_jid = c.jid AND m.from_me = 0
		                      AND m.push_name <> ''
		                    ORDER BY m.timestamp DESC LIMIT 1), ''),
		            -- Failing all that, a phone number reads better than a
		            -- LID, which is meaningless to a person.
		            CASE WHEN c.jid LIKE '%@s.whatsapp.net'
		                 THEN '+' || substr(c.jid, 1, instr(c.jid, '@') - 1) END,
		            CASE WHEN a.alt LIKE '%@s.whatsapp.net'
		                 THEN '+' || substr(a.alt, 1, instr(a.alt, '@') - 1) END,
		            c.jid),
		        c.kind, c.is_group, c.last_message, c.last_active,
		        c.pinned, c.muted, c.unread
		 FROM chats c
		 LEFT JOIN contacts ct ON ct.jid = c.jid
		 LEFT JOIN aliases  a  ON a.jid  = c.jid
		 LEFT JOIN contacts ac ON ac.jid = a.alt
		 WHERE c.last_active > 0 AND c.kind IN (`+placeholders+`)
		 ORDER BY c.pinned DESC, c.last_active DESC`,
		arguments...,
	)
	if err != nil {
		return nil, fmt.Errorf("query chats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var (
			chat       Chat
			lastActive int64
		)
		if err := rows.Scan(
			&chat.JID, &chat.Name, &chat.Kind, &chat.IsGroup,
			&chat.LastMessage, &lastActive, &chat.Pinned, &chat.Muted, &chat.Unread,
		); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chat.LastActive = time.Unix(lastActive, 0)
		chats = append(chats, chat)
	}
	// rows.Err reports failures that ended the loop early; without it a
	// truncated result set looks identical to an empty one.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return chats, nil
}

// SetChatMeta records the flags WhatsApp keeps about a chat.
func (s *MessageStore) SetChatMeta(jid, kind string, pinned, muted, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(
		`INSERT INTO chats (jid, kind, pinned, muted, archived)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (jid) DO UPDATE SET
		     kind = excluded.kind, pinned = excluded.pinned,
		     muted = excluded.muted, archived = excluded.archived`,
		jid, kind, pinned, muted, archived,
	)
	if err != nil {
		return fmt.Errorf("set chat meta: %w", err)
	}
	return nil
}

// SetUnread replaces a chat's unread count, which is what history sync gives
// us for chats we have never opened.
func (s *MessageStore) SetUnread(jid string, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(`UPDATE chats SET unread = ? WHERE jid = ?`, count, jid)
	if err != nil {
		return fmt.Errorf("set unread: %w", err)
	}
	return nil
}

// BumpUnread counts one more unread message in a chat.
func (s *MessageStore) BumpUnread(jid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(`UPDATE chats SET unread = unread + 1 WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("bump unread: %w", err)
	}
	return nil
}

// MarkRead clears a chat's unread count, when the user opens it.
func (s *MessageStore) MarkRead(jid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.conn.Exec(`UPDATE chats SET unread = 0 WHERE jid = ?`, jid)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// statusRank orders the delivery states so a receipt can only move a message
// forward. Receipts do arrive out of order.
func statusRank(status string) int {
	switch status {
	case StatusRead:
		return 3
	case StatusDelivered:
		return 2
	case StatusSent:
		return 1
	default:
		return 0
	}
}

// SetMessageStatus advances the delivery state of our own messages.
func (s *MessageStore) SetMessageStatus(chatJID string, ids []string, status string) error {
	if len(ids) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	arguments := []any{status, chatJID}
	for _, id := range ids {
		arguments = append(arguments, id)
	}
	arguments = append(arguments, statusRank(status))

	_, err := s.conn.Exec(
		`UPDATE messages SET status = ?
		 WHERE chat_jid = ? AND from_me = 1 AND id IN (`+placeholders+`)
		   AND CASE status
		         WHEN 'read' THEN 3 WHEN 'delivered' THEN 2 WHEN 'sent' THEN 1 ELSE 0
		       END < ?`,
		arguments...,
	)
	if err != nil {
		return fmt.Errorf("set message status: %w", err)
	}
	return nil
}

func encodeOptions(options []string) string {
	if len(options) == 0 {
		return ""
	}
	data, err := json.Marshal(options)
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeOptions(encoded string) []string {
	if encoded == "" {
		return nil
	}
	var options []string
	if err := json.Unmarshal([]byte(encoded), &options); err != nil {
		return nil
	}
	return options
}

// Messages returns the newest limit messages for a chat, oldest first, with
// their reactions attached.
func (s *MessageStore) Messages(chatJID string, limit int) ([]Message, error) {
	// The sender's display name is resolved here rather than trusted from the
	// stored column: what we captured at receive time is whatever push name
	// happened to be attached, which for one-to-one chats is often nothing at
	// all, leaving a bare phone number on screen.
	rows, err := s.conn.Query(
		`SELECT m.id, m.chat_jid, m.sender_jid,
		        CASE
		            WHEN m.from_me = 1 THEN 'You'
            -- A saved contact wins. Otherwise fall back to what the sender
		            -- calls themselves, marked with a tilde so it is clear the
		            -- number is not in the address book.
		            WHEN NULLIF(sc.name, '') IS NOT NULL THEN sc.name
		            WHEN NULLIF(sac.name, '') IS NOT NULL THEN sac.name
		            WHEN COALESCE(ch.is_group, 0) = 0 AND NULLIF(cc.name, '') IS NOT NULL THEN cc.name
		            WHEN NULLIF(m.push_name, '') IS NOT NULL THEN '~' || m.push_name
		            WHEN NULLIF(m.sender, '') IS NOT NULL THEN m.sender
		            ELSE m.sender_jid
		        END,
		        m.content, m.timestamp, m.from_me,
		        m.media_kind, m.media_mime, m.media_name, m.media_size, m.media_proto, m.media_path,
		        m.media_thumb,
		        m.reply_id,
		        CASE
		            WHEN m.reply_sender = '' THEN ''
		            WHEN NULLIF(rc.name, '') IS NOT NULL THEN rc.name
		            WHEN NULLIF(rac.name, '') IS NOT NULL THEN rac.name
		            ELSE COALESCE(
		                (SELECT '~' || pm.push_name FROM messages pm
		                 WHERE pm.sender_jid = m.reply_sender AND pm.push_name <> ''
		                 ORDER BY pm.timestamp DESC LIMIT 1),
		                m.reply_sender)
		        END,
		        m.reply_text,
		        m.push_name, m.status, m.poll_question, m.poll_options, m.revoked
		 FROM messages m
		 LEFT JOIN chats ch    ON ch.jid = m.chat_jid
		 LEFT JOIN contacts sc ON sc.jid = m.sender_jid
		 LEFT JOIN contacts cc ON cc.jid = m.chat_jid
		 LEFT JOIN contacts rc ON rc.jid = m.reply_sender
		 -- The same person under their other address, in case the name was
		 -- saved against that one and has not been mirrored across yet.
		 LEFT JOIN aliases  sa  ON sa.jid  = m.sender_jid
		 LEFT JOIN contacts sac ON sac.jid = sa.alt
		 LEFT JOIN aliases  ra  ON ra.jid  = m.reply_sender
		 LEFT JOIN contacts rac ON rac.jid = ra.alt
		 WHERE m.chat_jid = ?
		 ORDER BY m.timestamp DESC, m.id DESC
		 LIMIT ?`,
		chatJID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var (
			message     Message
			timestamp   int64
			mediaBlob   []byte
			thumbnail   []byte
			pollOptions string
		)
		if err := rows.Scan(
			&message.ID, &message.ChatJID, &message.SenderJID,
			&message.Sender, &message.Content, &timestamp, &message.FromMe,
			&message.Media.Kind, &message.Media.Mime, &message.Media.Name,
			&message.Media.Size, &mediaBlob, &message.Media.Path, &thumbnail,
			&message.Reply.MessageID, &message.Reply.Sender, &message.Reply.Text,
			&message.PushName, &message.Status, &message.Poll.Question, &pollOptions,
			&message.Revoked,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		message.Timestamp = time.Unix(timestamp, 0)
		message.Media.Proto = mediaBlob
		message.Media.Thumbnail = thumbnail
		message.Poll.Options = decodeOptions(pollOptions)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	// The query is newest-first so that LIMIT keeps the recent messages;
	// reverse it back into reading order.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if err := s.attachReactions(chatJID, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// attachReactions fills in Reactions on each message with one query, rather
// than one query per message.
func (s *MessageStore) attachReactions(chatJID string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	rows, err := s.conn.Query(
		`SELECT message_id, sender_jid, sender, emoji, timestamp
		 FROM reactions WHERE chat_jid = ? ORDER BY timestamp`,
		chatJID,
	)
	if err != nil {
		return fmt.Errorf("query reactions: %w", err)
	}
	defer rows.Close()

	byMessage := map[string][]Reaction{}
	for rows.Next() {
		var (
			reaction  Reaction
			timestamp int64
		)
		if err := rows.Scan(&reaction.MessageID, &reaction.SenderJID, &reaction.Sender, &reaction.Emoji, &timestamp); err != nil {
			return fmt.Errorf("scan reaction: %w", err)
		}
		reaction.Timestamp = time.Unix(timestamp, 0)
		byMessage[reaction.MessageID] = append(byMessage[reaction.MessageID], reaction)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate reactions: %w", err)
	}

	// Index by ID rather than nesting loops, so this stays linear.
	for i := range messages {
		messages[i].Reactions = byMessage[messages[i].ID]
	}
	return nil
}

// Message looks up a single message, used when a reaction or reply refers to
// something we need the text of.
func (s *MessageStore) Message(chatJID, messageID string) (Message, bool, error) {
	row := s.conn.QueryRow(
		`SELECT id, chat_jid, sender_jid, sender, content, timestamp, from_me,
		        media_kind, media_mime, media_name, media_size, media_path
		 FROM messages WHERE chat_jid = ? AND id = ?`,
		chatJID, messageID,
	)

	var (
		message   Message
		timestamp int64
	)
	err := row.Scan(
		&message.ID, &message.ChatJID, &message.SenderJID,
		&message.Sender, &message.Content, &timestamp, &message.FromMe,
		&message.Media.Kind, &message.Media.Mime, &message.Media.Name,
		&message.Media.Size, &message.Media.Path,
	)
	if err == sql.ErrNoRows {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, fmt.Errorf("query message: %w", err)
	}

	message.Timestamp = time.Unix(timestamp, 0)
	return message, true, nil
}
