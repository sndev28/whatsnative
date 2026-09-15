package main

import (
	"whatsnative/logger"
	"whatsnative/ui"

	clientFactory "whatsnative/client"
	"whatsnative/db"
)

const DB_NAME string = "my_database.db"

func main() {
	appLog, logCloser := logger.Logger("logs.log", true)
	defer logCloser.Close()

	// Off unless WHATSNATIVE_PPROF names an address to listen on.
	startProfiler(appLog)

	dbLog, dbLogCloser := logger.Logger("db.log", false)
	defer dbLogCloser.Close()

	dbConn, dbCloser := db.New(dbLog, DB_NAME)
	defer dbCloser.Close()

	session := clientFactory.CreateClient(dbConn)
	defer session.Close()

	// Connect before the UI starts so the QR codes (or the reconnect) are
	// already on their way by the time the first frame is drawn.
	if err := session.Start(); err != nil {
		panic(err)
	}

	ui.StartUI(session, dbConn.Messages)
}
