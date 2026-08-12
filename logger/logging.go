package logger

import (
	"log"
	"log/slog"
	"os"
	"io"
)

func DbLogger () (*slog.Logger, io.Closer) {
	file, err := os.OpenFile("dbLogs.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	dbLog := slog.New(slog.NewTextHandler(file, nil))
	return dbLog, file
}