package client

import (
	"whatsnative/db"
	"whatsnative/logger"

	"go.mau.fi/whatsmeow"
)

// MediaDir is where downloaded photos, videos and documents are cached.
const MediaDir = "media"

func CreateClient(dbConn db.DBConn) *Session {

	deviceStore, err := dbConn.Container.GetFirstDevice(
		dbConn.Ctx,
	)

	if err != nil {
		panic(err)
	}

	// No `defer closer.Close()` here: the client keeps logging for its whole
	// life, so closing the file at the end of this function left it writing
	// into a closed handle. The Session owns the closer and shuts it down.
	clientLog, closer := logger.Logger("client.log", false)

	// Before the client exists, and so before anything can be decrypted.
	// libsignal's own logger writes to stdout, which is the terminal the UI
	// draws on -- a failed decryption would otherwise print itself straight
	// over the interface.
	logger.CaptureSignalLogs(clientLog.With("module", "signal"))

	client := whatsmeow.NewClient(deviceStore, logger.WaLogAdapter{Log: clientLog})
	// Without this, the very first sync of any app-state collection -- pins,
	// read state -- decodes correctly but throws the resulting events away
	// instead of telling us about them. That first sync is precisely the one
	// that carries the account's current state, so skipping it would mean
	// syncReadState's first run silently learns nothing.
	client.EmitAppStateEventsOnFullSync = true

	session := &Session{
		WA:        client,
		ctx:       dbConn.Ctx,
		messages:  dbConn.Messages,
		mediaDir:  MediaDir,
		logCloser: closer,
		// Buffered so a busy UI does not stall whatsmeow's own goroutines.
		events: make(chan any, 256),
		done:   make(chan struct{}),
	}

	client.AddEventHandler(session.handleEvent)

	return session
}
