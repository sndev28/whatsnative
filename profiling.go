package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// startProfiler exposes Go's pprof endpoints when WHATSNATIVE_PPROF is set to
// an address, e.g. WHATSNATIVE_PPROF=localhost:6060.
//
// Off unless asked for. This is a diagnostic hatch, not a feature: it serves
// the process's full memory and goroutine state to anything that can reach
// the address, so it stays bound to wherever the operator points it and is
// absent entirely by default.
//
// When memory climbs, this is what turns guesswork into an answer:
//
//	go tool pprof -top http://localhost:6060/debug/pprof/heap
//
// Use inuse_space to see what is currently held and alloc_space to see what
// has churned through; the two tell very different stories, and for this app
// -- which allocates hard during history sync and then frees nearly all of it
// -- the difference is the whole picture.
func startProfiler(log *slog.Logger) {
	address := os.Getenv("WHATSNATIVE_PPROF")
	if address == "" {
		return
	}

	mux := http.NewServeMux()
	// Registered by hand rather than by importing net/http/pprof for its side
	// effect: that would attach these handlers to the default mux, which is
	// process-global, and leave them reachable even with profiling off.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("profiler listening", "address", address)
		// Logged rather than fatal: failing to profile is never a reason to
		// stop the app the user actually asked to run.
		if err := server.ListenAndServe(); err != nil {
			log.Error("profiler stopped", "error", err)
		}
	}()
}
