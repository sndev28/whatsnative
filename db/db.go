package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"whatsnative/logger"
)

// DBConn is everything the rest of the app needs from storage: whatsmeow's
// container for protocol state, and our own store for chat history.
type DBConn struct {
	Ctx       context.Context
	Container *sqlstore.Container
	Messages  *MessageStore
}

// New opens the database and builds both stores on top of one connection pool.
//
// They deliberately share a single *sql.DB: two pools against one SQLite file
// would compete for the write lock and start returning "database is locked".
func New(log *slog.Logger, dbName string) (DBConn, io.Closer) {
	ctx := context.Background()

	// WAL lets the UI read while whatsmeow writes; the busy timeout makes a
	// blocked writer wait rather than fail immediately.
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", dbName)

	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Error(fmt.Sprintf("Db failed to initialize with error: %s", err))
		panic(err)
	}

	// sql.Open is lazy, so nothing has actually touched the file yet. Ping
	// surfaces a bad path or unreadable file here instead of much later.
	if err := conn.Ping(); err != nil {
		log.Error(fmt.Sprintf("Db failed to connect with error: %s", err))
		panic(err)
	}

	container := sqlstore.NewWithDB(conn, "sqlite3", logger.WaLogAdapter{Log: log})

	// sqlstore.New runs migrations for you; NewWithDB leaves that to the
	// caller, since it cannot assume it owns the database.
	if err := container.Upgrade(ctx); err != nil {
		log.Error(fmt.Sprintf("Db failed to upgrade with error: %s", err))
		panic(err)
	}

	messages, err := NewMessageStore(conn)
	if err != nil {
		log.Error(fmt.Sprintf("Db failed to create message store with error: %s", err))
		panic(err)
	}

	// The returned closer is now the connection itself rather than the
	// container: closing the pool is what actually releases the file.
	return DBConn{
		Ctx:       ctx,
		Container: container,
		Messages:  messages,
	}, conn
}
