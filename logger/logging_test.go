package logger

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	signallog "go.mau.fi/libsignal/logger"
)

// The bug this guards: libsignal's fallback logger writes with fmt.Println,
// and stdout is the terminal the UI draws on. A failed decryption -- which is
// routine, and which the library recovers from by retrying older session
// states -- printed itself straight over the interface.
//
// The test captures the real stdout rather than trusting the adapter in
// isolation, because "nothing reaches the terminal" is the actual claim.
func TestSignalLogsNeverReachStdout(t *testing.T) {
	realStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = realStdout })

	var captured bytes.Buffer
	var logFile strings.Builder
	CaptureSignalLogs(slog.New(slog.NewTextHandler(&logFile, nil)))

	// The exact call libsignal makes on a MAC mismatch.
	signallog.Warning("Unable to verify ciphertext MAC; mismatching MAC in signal message")
	signallog.Error("something worse")
	signallog.Info("routine chatter")

	write.Close()
	if _, err := io.Copy(&captured, read); err != nil {
		t.Fatal(err)
	}

	if captured.Len() != 0 {
		t.Errorf("libsignal wrote %q to stdout, which is the terminal the UI draws on", captured.String())
	}
	if !strings.Contains(logFile.String(), "mismatching MAC") {
		t.Errorf("the message did not reach the log file either:\n%s", logFile.String())
	}
}

// Every level has to be routed. Missing one would leave that level falling
// through to the stdout logger.
func TestSignalAdapterRoutesEveryLevel(t *testing.T) {
	var out strings.Builder
	adapter := SignalLogAdapter{
		Log: slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	adapter.Debug("Caller.go:1", "debug line")
	adapter.Info("Caller.go:2", "info line")
	adapter.Warning("Caller.go:3", "warning line")
	adapter.Error("Caller.go:4", "error line")
	adapter.Configure("all") // must not panic

	for _, want := range []string{"debug line", "info line", "warning line", "error line"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%q never reached the log:\n%s", want, out.String())
		}
	}
	// The caller is what makes a libsignal line traceable back to a source
	// line, so it has to survive.
	if !strings.Contains(out.String(), "Caller.go:3") {
		t.Error("the caller was dropped, leaving nothing to trace the line back to")
	}
}
