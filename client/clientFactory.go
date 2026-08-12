package client

import (
	"whatsnative/db"
	"whatsnative/logger"

	"go.mau.fi/whatsmeow"
)


func CreateClient (dbConn db.DBConn) (*whatsmeow.Client) {

	deviceStore, err := dbConn.Container.GetFirstDevice(
		dbConn.Ctx,
	)

	if err != nil {
		panic(err)
	}

	clientLog, closer := logger.Logger("client.log", false)
	defer closer.Close()

	client := whatsmeow.NewClient(deviceStore, logger.WaLogAdapter{Log: clientLog})
	// client.AddEventHandler(eventHandler)

	return client
}
