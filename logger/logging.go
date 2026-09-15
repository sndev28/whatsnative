package logger

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"

	signallog "go.mau.fi/libsignal/logger"
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

// SignalLogAdapter routes libsignal's logging into a file.
//
// libsignal keeps its own logger, entirely separate from whatsmeow's, and
// falls back to one that writes with fmt.Println -- to stdout. Stdout is the
// terminal this app draws its interface on, so every line it logged was
// painted straight over the UI. Worse, its own level filter is dead code (the
// return in defaultLogger.log is commented out upstream), so it emits
// everything regardless of what it is configured with.
//
// Signal reports a failed decryption at Warning and recovers by retrying
// against previous session states, so these are routine events rather than
// something the reader needs shown. They belong in the log file with
// everything else. See CaptureSignalLogs.
type SignalLogAdapter struct {
	Log *slog.Logger
}

func (a SignalLogAdapter) Debug(caller, message string) {
	a.Log.Debug(message, "caller", caller)
}

func (a SignalLogAdapter) Info(caller, message string) {
	a.Log.Info(message, "caller", caller)
}

func (a SignalLogAdapter) Warning(caller, message string) {
	a.Log.Warn(message, "caller", caller)
}

func (a SignalLogAdapter) Error(caller, message string) {
	a.Log.Error(message, "caller", caller)
}

// Configure is part of libsignal's interface. Its own filtering is broken
// upstream, and slog handles levels for us anyway, so there is nothing to do.
func (a SignalLogAdapter) Configure(string) {}

// CaptureSignalLogs points libsignal at the given logger, so nothing it
// writes can reach the terminal.
//
// Must run before the first decryption. libsignal installs its stdout logger
// lazily on first use, and Setup after that point would leave whatever it had
// already printed on screen.
func CaptureSignalLogs(log *slog.Logger) {
	var sink signallog.Loggable = SignalLogAdapter{Log: log}
	signallog.Setup(&sink)
}