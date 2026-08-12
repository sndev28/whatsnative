package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"whatsnative/logger"
)


type DBConn struct {
	Ctx context.Context
	Container *sqlstore.Container
}

func New(log *slog.Logger, dbName string) (DBConn, io.Closer) {
	ctx := context.Background()

	container, err := sqlstore.New(ctx, "sqlite3", "file:" + dbName + "?_foreign_keys=on", logger.WaLogAdapter{Log: log})
	if err != nil {
		log.Error(fmt.Sprintf("Db failed to initialize with error: %s", err))
		panic(err)
	}

	return DBConn{
		Container: container,
		Ctx: ctx,
	}, container

}