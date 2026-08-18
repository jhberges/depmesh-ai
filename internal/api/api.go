// Package api serves the vet gate over HTTP for self-hosting: one instance run
// by a platform team inside its own network boundary, so developer machines and
// CI runners don't each need registry egress. This is the deployment model
// regulated organizations usually require.
//
//	GET /v1/vet/{ecosystem}/{package...}   → Outcome JSON (200 allowed, 409 blocked)
//	GET /healthz                           → 200 ok
//
// {package...} may contain slashes (npm scopes) and colons (maven
// coordinates). Query: ?enrich=false to skip deps.dev, ?version= to vet one
// exact release as well as the package.
//
// The version is a query parameter rather than a path segment precisely
// because {package...} is greedy: a Maven GAV in the path would be
// indistinguishable from the coordinate itself.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/upstream"
)

func Handler(g *gate.Gate) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/vet/{ecosystem}/{package...}", func(w http.ResponseWriter, r *http.Request) {
		ecosystem := r.PathValue("ecosystem")
		name := r.PathValue("package")
		enrich := r.URL.Query().Get("enrich") != "false"
		version := strings.TrimSpace(r.URL.Query().Get("version"))

		outcome, err := g.Vet(caller(r), ecosystem, name, version, enrich)
		var unavailable *sources.UnavailableError
		switch {
		case errors.As(err, &unavailable):
			// 502: upstream registry unreachable — explicitly not a verdict.
			writeError(w, http.StatusBadGateway,
				"registry unreachable — package could not be vetted; this is not approval or proof of non-existence")
			return
		case err != nil && outcome == nil:
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			// Outcome exists but auditing failed: fail closed. A decision
			// that cannot be recorded must not be handed out.
			writeError(w, http.StatusInternalServerError, "audit log write failed: "+err.Error())
			return
		}

		status := http.StatusOK
		if !outcome.Allowed() {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(outcome)
	})
	return mux
}

// caller identifies who to record for this request. A delegating client
// sends its own surface and identity, because "who asked" is the developer
// on the far end, not this server; a direct caller sends nothing and gets
// this machine's identity. The surface is mapped through a fixed vocabulary
// rather than copied, and the free-text fields are bounded, so a client
// cannot write arbitrary values into the audit log.
func caller(r *http.Request) audit.Caller {
	c := audit.Caller{
		Surface:  "api",
		Actor:    clean(r.Header.Get(upstream.ActorHeader)),
		Hostname: clean(r.Header.Get(upstream.HostnameHeader)),
	}
	switch strings.ToLower(r.Header.Get(upstream.SurfaceHeader)) {
	case "cli":
		c.Surface = "cli"
	case "mcp":
		c.Surface = "mcp"
	}
	return c
}

const maxIdentityLength = 128

func clean(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, value)
	if runes := []rune(value); len(runes) > maxIdentityLength {
		value = string(runes[:maxIdentityLength])
	}
	return value
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Server timeouts. The API is unauthenticated by design — it is meant to sit on
// a trusted network — so it has no say in who opens a connection to it, and a
// server with no timeouts at all is held open indefinitely by anyone who dribbles
// a request header one byte at a time.
//
// writeTimeout is the one that cannot be tightened casually: a single vet makes
// a registry call and then a deps.dev enrichment call, each on the 20s client in
// internal/sources. Below the sum of those, a slow-but-working registry stops
// looking slow and starts looking like a truncated response.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 90 * time.Second
	idleTimeout       = 120 * time.Second

	// shutdownGrace bounds the drain. Docker's default stop timeout is 10s
	// before SIGKILL, so this is deliberately shorter: a drain that outlives
	// the runtime's patience is the abrupt kill it was meant to replace.
	shutdownGrace = 8 * time.Second
)

func newServer(addr string, g *gate.Gate) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(g),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// ListenAndServe runs the API server on addr until it is interrupted, then
// drains in-flight requests before returning.
//
// The drain is not politeness. Auditing is fail-closed and rotation renames the
// live file, so a request killed between the rotate and the append is a decision
// that was neither served nor recorded — the one gap a compliance log must not
// have. Under a container runtime that window would otherwise open on every
// deploy, restart, and scale-down, which is to say constantly.
func ListenAndServe(addr string, g *gate.Gate) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := newServer(addr, g)
	failed := make(chan error, 1)
	go func() {
		// ErrServerClosed is what Shutdown causes, so it is the success path
		// here rather than a failure to report.
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
		close(failed)
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	// Stop catching the signal before draining: a second Ctrl-C now takes the
	// default disposition and kills the process, so an operator is never stuck
	// waiting out a drain that is going nowhere.
	stop()
	drain, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return server.Shutdown(drain)
}
