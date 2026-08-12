package main

import (
	_ "github.com/mattn/go-sqlite3"

	"whatsnative/ui"
	"whatsnative/logger"

	"whatsnative/db"
	clientFactory "whatsnative/client"


)

const DB_NAME string = "my_database.db"


func main() {
	_, logCloser := logger.Logger("logs.log", true)
	defer logCloser.Close()
	
	dbLog, dbLogCloser := logger.Logger("db.log", false)
	defer dbLogCloser.Close()

	dbConn, dbCloser := db.New(dbLog, DB_NAME)
	defer dbCloser.Close()

	client := clientFactory.CreateClient(dbConn)
	defer client.Disconnect()

	ui.StartUI(client)
}