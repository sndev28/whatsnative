package logger

import (
	"log"
	"log/slog"
	"os"
	"io"
	"fmt"

	waLog "go.mau.fi/whatsmeow/util/log"

)

func Logger (loggerName string, defaultLogger bool) (*slog.Logger, io.Closer) {
	file, err := os.OpenFile(loggerName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	log := slog.New(slog.NewTextHandler(file, nil))
	if (defaultLogger) {
		slog.SetDefault(log)
	}
	return log, file
}


type WaLogAdapter struct {
	Log *slog.Logger
}

func (a WaLogAdapter) Errorf(msg string, args ...any) { a.Log.Error(fmt.Sprintf(msg, args...)) }
func (a WaLogAdapter) Warnf(msg string, args ...any)  { a.Log.Warn(fmt.Sprintf(msg, args...)) }
func (a WaLogAdapter) Infof(msg string, args ...any)  { a.Log.Info(fmt.Sprintf(msg, args...)) }
func (a WaLogAdapter) Debugf(msg string, args ...any) { a.Log.Debug(fmt.Sprintf(msg, args...)) }
func (a WaLogAdapter) Sub(module string) waLog.Logger {
	return WaLogAdapter{Log: a.Log.With("module", module)}
}