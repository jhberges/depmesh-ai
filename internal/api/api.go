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

	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/sources"
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

		outcome, err := g.Vet("api", ecosystem, name, enrich)
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
