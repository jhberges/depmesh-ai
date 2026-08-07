// Package api serves the vet gate over HTTP for self-hosting: one instance run
// by a platform team inside its own network boundary, so developer machines and
// CI runners don't each need registry egress. This is the deployment model
// regulated organizations usually require.
//
//	GET /v1/vet/{ecosystem}/{package...}   → Outcome JSON (200 allowed, 409 blocked)
//	GET /healthz                           → 200 ok
//
// {package...} may contain slashes (npm scopes) and colons (maven
// coordinates). Query: ?enrich=false to skip deps.dev.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

		outcome, err := g.Vet(caller(r), ecosystem, name, "", enrich)
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

// ListenAndServe runs the API server on addr.
func ListenAndServe(addr string, g *gate.Gate) error {
	server := &http.Server{Addr: addr, Handler: Handler(g)}
	return server.ListenAndServe()
}
