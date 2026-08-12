package main

import (
	"whatsnative/ui"
	"whatsnative/logger"
)


func main() {
	dbLog, closer := logger.DbLogger()
	defer closer.Close()

	dbLog.Info("Logger working")

	ui.StartUI()
}