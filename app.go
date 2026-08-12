package main

import (

	"whatsnative/ui"
	"whatsnative/logger"
	_ "github.com/mattn/go-sqlite3"

	"whatsnative/db"


)

const DB_NAME string = "my_database.db"




func main() {
	dbLog, logCloser := logger.DbLogger()
	defer logCloser.Close()

	dbConn, dbCloser := db.New(dbLog, DB_NAME)
	defer dbCloser.Close()

	ui.StartUI(dbConn)
}